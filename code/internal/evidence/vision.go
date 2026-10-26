package evidence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"ghostaudit/internal/gemini"
)

// VisionPromptVersion is bumped whenever the prompt/schema semantics change, which
// invalidates cached extractions.
const VisionPromptVersion = "v1"

var visionSchema = map[string]any{
	"type": "OBJECT",
	"properties": map[string]any{
		"document_type": map[string]any{"type": "STRING"},
		"system_id":     map[string]any{"type": "STRING"},
		"captured_at":   map[string]any{"type": "STRING"},
		"visible_text":  map[string]any{"type": "STRING"},
		"records": map[string]any{
			"type": "ARRAY",
			"items": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"resource_id": map[string]any{"type": "STRING"},
					"record_type": map[string]any{"type": "STRING"},
					"system_id":   map[string]any{"type": "STRING"},
					"observed_at": map[string]any{"type": "STRING"},
					"fields": map[string]any{
						"type": "ARRAY",
						"items": map[string]any{
							"type": "OBJECT",
							"properties": map[string]any{
								"key":   map[string]any{"type": "STRING"},
								"value": map[string]any{"type": "STRING"},
							},
							"required": []string{"key", "value"},
						},
					},
				},
				"required": []string{"fields"},
			},
		},
	},
	"required": []string{"document_type", "visible_text", "records"},
}

// visionPrompt is built per run so the list of known system ids stays dynamic: it comes
// from whatever the non-vision evidence and activity stream already contain.
func visionPrompt(systems []string) string {
	return `You are a meticulous data-extraction engine reading ONE business artifact (an application
screenshot, invoice, support record, handover document, or retention register).
Transcribe facts exactly as shown. Never infer, guess, correct, or add anything that is not visible.
Do not judge whether data was deleted; just report what is on the page.

Return JSON with:
- document_type: short snake_case label of what the artifact is (e.g. admin_console, backup_console, invoice, retention_register).
- system_id: the platform system this artifact belongs to, chosen ONLY from this list when it clearly matches, otherwise "": ` +
		strings.Join(systems, ", ") + `.
- captured_at: ISO-8601 UTC timestamp if a document date/time is visible, otherwise "".
- visible_text: a faithful plain-text transcription of ALL visible text, row by row.
- records: one entry per distinct entity shown. Make one record for EVERY table row, and one record for each
  detail / side panel or header card describing a single entity. For each record:
    resource_id: the exact identifier shown for the entity (like account_5762, backup_9287, support_9538, search_1234, data_flow_77, export_12, audit_record_9); "" if none is shown.
    record_type: the entity type as shown (e.g. customer, archive, ticket, connector, export, audit_event), snake_case.
    system_id: as above but for this record, or "".
    observed_at: the record's own date/time as ISO-8601 if shown, else "".
    fields: every labelled value as {key,value}: use snake_case keys (email, phone, full_name, customer_reference for ids like user_11, account, path, region, status, created, retention_end, ...). Copy values verbatim (emails, phones, hashes, names, ids, dates).
Include user references such as user_11 as customer_reference. Do not omit any email address, phone number, name or id that appears.`
}

type visionDoc struct {
	DocumentType string `json:"document_type"`
	SystemID     string `json:"system_id"`
	CapturedAt   string `json:"captured_at"`
	VisibleText  string `json:"visible_text"`
	Records      []struct {
		ResourceID string `json:"resource_id"`
		RecordType string `json:"record_type"`
		SystemID   string `json:"system_id"`
		ObservedAt string `json:"observed_at"`
		Fields     []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"fields"`
	} `json:"records"`
}

// VisionJob is one image/PDF to read.
type VisionJob struct {
	Path string // for logging
	Data []byte
	MIME string
	SHA  string
}

// VisionResult carries the parsed extraction (or an error) for a job.
type VisionResult struct {
	Ex       Extracted
	Err      error
	CacheHit bool
}

// Vision runs jobs against Gemini with a content-addressed persistent cache, so an
// unchanged file is never sent twice (cost + determinism) and changed files are re-read.
type Vision struct {
	Client      *gemini.Client
	DB          *sql.DB // must have the persistent cache DB attached as "cache"; used outside any open transaction
	Systems     []string
	Concurrency int
	RunID       string
}

func (v *Vision) cacheKey(sha string) (key, promptHash string) {
	p := visionPrompt(v.Systems)
	h := sha256.Sum256([]byte(VisionPromptVersion + "|" + p))
	promptHash = hex.EncodeToString(h[:8])
	return sha + "|" + v.Client.Model + "|" + promptHash, promptHash
}

// Run executes all jobs and returns results keyed by job path.
func (v *Vision) Run(ctx context.Context, jobs []VisionJob) map[string]VisionResult {
	sort.Strings(v.Systems)
	results := map[string]VisionResult{}
	var mu sync.Mutex
	var pending []VisionJob

	for _, j := range jobs {
		key, _ := v.cacheKey(j.SHA)
		var resp string
		err := v.DB.QueryRow(`SELECT response_json FROM cache.vision_cache WHERE cache_key=?`, key).Scan(&resp)
		if err == nil {
			ex, perr := parseVision(resp)
			results[j.Path] = VisionResult{Ex: ex, Err: perr, CacheHit: true}
			v.logUsage(j.Path, 0, 0, 0, 0, true)
			continue
		}
		pending = append(pending, j)
	}

	conc := v.Concurrency
	if conc < 1 {
		conc = 4
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	type done struct {
		job  VisionJob
		resp string
		u    gemini.Usage
		err  error
	}
	ch := make(chan done)
	for _, j := range pending {
		wg.Add(1)
		go func(j VisionJob) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resp, u, err := v.Client.GenerateJSON(ctx, visionPrompt(v.Systems),
				[]gemini.File{{MIME: j.MIME, Data: j.Data}}, visionSchema)
			ch <- done{j, resp, u, err}
		}(j)
	}
	go func() { wg.Wait(); close(ch) }()

	// Single consumer goroutine = the only one that touches the DB, so the
	// single-connection SQLite handle never contends.
	for d := range ch {
		mu.Lock()
		if d.err != nil {
			results[d.job.Path] = VisionResult{Err: d.err}
		} else {
			ex, perr := parseVision(d.resp)
			results[d.job.Path] = VisionResult{Ex: ex, Err: perr}
			if perr == nil {
				key, ph := v.cacheKey(d.job.SHA)
				_, _ = v.DB.Exec(`INSERT OR REPLACE INTO cache.vision_cache
				(cache_key, sha256, model, prompt_hash, response_json, input_tokens, output_tokens, cost_usd, created_at)
				VALUES (?,?,?,?,?,?,?,?,?)`, key, d.job.SHA, v.Client.Model, ph, d.resp,
					d.u.InputTokens, d.u.OutputTokens, d.u.CostUSD, time.Now().UTC().Format(time.RFC3339))
			}
			v.logUsage(d.job.Path, d.u.InputTokens, d.u.OutputTokens, d.u.Latency.Milliseconds(), d.u.CostUSD, false)
		}
		mu.Unlock()
	}
	return results
}

func (v *Vision) logUsage(ref string, in, out int, latMS int64, cost float64, hit bool) {
	h := 0
	if hit {
		h = 1
	}
	_, _ = v.DB.Exec(`INSERT INTO cache.llm_usage
		(run_id, ts, purpose, ref, provider, model, input_tokens, output_tokens, latency_ms, cost_usd, cache_hit)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, v.RunID, time.Now().UTC().Format(time.RFC3339), "vision_extract", ref,
		"google", v.Client.Model, in, out, latMS, cost, h)
}

func parseVision(resp string) (Extracted, error) {
	var d visionDoc
	if err := json.Unmarshal([]byte(resp), &d); err != nil {
		return Extracted{}, fmt.Errorf("vision json: %w", err)
	}
	ex := Extracted{DocTS: NormTS(d.CapturedAt)}
	snap := SnapKey{System: d.SystemID, Collection: d.DocumentType, TS: ex.DocTS}
	for i, r := range d.Records {
		raw := map[string]any{}
		for _, f := range r.Fields {
			if f.Key != "" {
				raw[f.Key] = f.Value
			}
		}
		if r.ResourceID != "" {
			raw["resource_id"] = r.ResourceID
		}
		sys := r.SystemID
		if sys == "" {
			sys = d.SystemID
		}
		ex.Records = append(ex.Records, Record{
			Snap: snap, System: sys, Resource: r.ResourceID, Collection: d.DocumentType,
			RecordType: r.RecordType, ObservedAt: firstNonEmpty(NormTS(r.ObservedAt), ex.DocTS),
			Locator: fmt.Sprintf("vision record %d", i+1), Raw: raw,
		})
	}
	if strings.TrimSpace(d.VisibleText) != "" {
		ex.Records = append(ex.Records, Record{
			Snap: snap, System: d.SystemID, Collection: "visible_text", ObservedAt: ex.DocTS,
			Locator: "vision transcription", Raw: map[string]any{"text": d.VisibleText},
		})
	}
	return ex, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
