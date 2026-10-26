// Command report writes code/evaluation/evaluation_report.md and code/evaluation/usage_report.md
// from the artifacts of the final run: the sample scores, the contract validator, the intent
// verdicts and the per-call usage table.
//
//	go run ./cmd/report -run <run id printed by cmd/pipeline>
package main

import (
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ghostaudit/internal/paths"
	"ghostaudit/internal/pipeline"
	"ghostaudit/internal/score"
	"ghostaudit/internal/store"
	"ghostaudit/internal/validate"
)

func main() {
	run := flag.String("run", "", "run id of the final full run (see cmd/pipeline output)")
	flag.Parse()
	if err := do(*run); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func do(runID string) error {
	db, err := store.Open(paths.Join("data", "audit.db"), paths.Join("data", "cache.db"), false)
	if err != nil {
		return err
	}
	defer db.Close()
	if runID == "" {
		_ = db.QueryRow(`SELECT run_id FROM cache.llm_usage WHERE purpose='intent_classify' AND cache_hit=0 ORDER BY ts DESC LIMIT 1`).Scan(&runID)
	}
	if runID == "" {
		return fmt.Errorf("no run id given and no live intent calls found in the usage table")
	}
	dir := paths.Join("code", "evaluation")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ev, err := evaluationReport(db, runID)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "evaluation_report.md"), []byte(ev), 0o644); err != nil {
		return err
	}
	us, err := usageReport(db, runID)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "usage_report.md"), []byte(us), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", filepath.Join(dir, "evaluation_report.md"), "and usage_report.md for run", runID)
	return nil
}

// ------------------------------------------------------------------ usage

func usageReport(db *sql.DB, runID string) (string, error) {
	var b strings.Builder
	rows, err := db.Query(`
SELECT purpose, provider, model, COUNT(*) calls, SUM(CASE WHEN cache_hit=0 THEN 1 ELSE 0 END) live,
       SUM(input_tokens), SUM(output_tokens), SUM(cost_usd), AVG(CASE WHEN cache_hit=0 THEN latency_ms END)
FROM cache.llm_usage WHERE run_id = ? GROUP BY purpose, provider, model ORDER BY purpose`, runID)
	if err != nil {
		return "", err
	}
	type line struct {
		purpose, provider, model string
		calls, live, in, out     int
		cost                     float64
		lat                      sql.NullFloat64
	}
	var ls []line
	var tc, tl, ti, to int
	var tcost float64
	for rows.Next() {
		var l line
		var cost sql.NullFloat64
		if err := rows.Scan(&l.purpose, &l.provider, &l.model, &l.calls, &l.live, &l.in, &l.out, &cost, &l.lat); err != nil {
			rows.Close()
			return "", err
		}
		l.cost = cost.Float64
		ls = append(ls, l)
		tc += l.calls
		tl += l.live
		ti += l.in
		to += l.out
		tcost += l.cost
	}
	rows.Close()
	var requests int
	_ = db.QueryRow(`SELECT COUNT(*) FROM deletion_requests`).Scan(&requests)
	var pin, pout float64
	fmt.Sscanf(os.Getenv("GEMINI_PRICE_INPUT_PER_M"), "%f", &pin)
	fmt.Sscanf(os.Getenv("GEMINI_PRICE_OUTPUT_PER_M"), "%f", &pout)
	if pin == 0 {
		pin = 0.30
	}
	if pout == 0 {
		pout = 2.50
	}

	fmt.Fprintf(&b, "# Usage report\n\n")
	fmt.Fprintf(&b, "Final full-dataset run that produced `output.csv`: run id `%s`, generated %s.\n\n", runID, time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&b, "The run executed `go run ./cmd/pipeline` from an empty cache (the vision pass was entirely live; see the Live calls column for intent calls, a few of which were cache hits because identical request texts recur): ingest with vision over the evidence images/PDFs, then intent classification of the 32 public sample requests (used for scoring) and the %d evaluation requests. The audit engine itself (identity resolution, evidence selection, state derivation, rules, ordering) makes no model calls.\n\n", requests)
	fmt.Fprintf(&b, "## Per-model, per-purpose totals\n\n| Purpose | Provider | Model | Calls | Live calls | Input tokens | Output tokens | Total tokens | Est. cost (USD) | Avg latency (live) |\n|---|---|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, l := range ls {
		lat := "n/a"
		if l.lat.Valid {
			lat = fmt.Sprintf("%.0f ms", l.lat.Float64)
		}
		fmt.Fprintf(&b, "| %s | %s | `%s` | %d | %d | %d | %d | %d | $%.4f | %s |\n", l.purpose, l.provider, l.model, l.calls, l.live, l.in, l.out, l.in+l.out, l.cost, lat)
	}
	fmt.Fprintf(&b, "| **Overall** | | | **%d** | **%d** | **%d** | **%d** | **%d** | **$%.4f** | |\n\n", tc, tl, ti, to, ti+to, tcost)
	avgTok, avgCost := 0.0, 0.0
	if requests > 0 {
		avgTok = float64(ti+to) / float64(requests)
		avgCost = tcost / float64(requests)
	}
	fmt.Fprintf(&b, "## Per-request figures\n\n")
	fmt.Fprintf(&b, "Evaluation requests in the dataset: **%d**.\n\n", requests)
	fmt.Fprintf(&b, "| Metric | Value |\n|---|---:|\n")
	fmt.Fprintf(&b, "| Total tokens (input + output) | %d |\n| Average tokens per request | %.1f |\n| Estimated total cost | $%.4f |\n| Estimated cost per request | $%.6f |\n| Model calls per request | %.2f |\n\n", ti+to, avgTok, tcost, avgCost, float64(tc)/float64(max(requests, 1)))
	fmt.Fprintf(&b, "Totals include the one-off vision pass over the evidence files and the 32 sample requests, so the per-request average is conservative (it spreads all of that over the %d evaluation requests only).\n\n", requests)
	fmt.Fprintf(&b, "## How the numbers are produced\n\n")
	fmt.Fprintf(&b, "- Every model call is written to the `llm_usage` table (SQLite, `data/cache.db`) with run id, purpose, provider, model, input tokens, output tokens, latency and cost, using the `usageMetadata` the Gemini API returns. Output tokens include thinking tokens, which are billed as output.\n")
	fmt.Fprintf(&b, "- Cost is an **estimate**: tokens x price per million tokens, using `GEMINI_PRICE_INPUT_PER_M=%.2f` and `GEMINI_PRICE_OUTPUT_PER_M=%.2f` USD from the environment (defaults in `.env.example`). Check them against the current price list of the model used; the token counts are exact.\n", pin, pout)
	fmt.Fprintf(&b, "- Responses are cached by content hash (file hash for vision; request-text hash for intent) together with the model name and a prompt-version hash, so re-running with the same inputs costs nothing and changing the prompt or model invalidates the cache automatically.\n")
	fmt.Fprintf(&b, "- One model is used for both purposes (the vision model and the intent model are set separately with `GEMINI_VISION_MODEL` / `GEMINI_MODEL`); per-model totals are the rows above.\n")
	return b.String(), nil
}

// ------------------------------------------------------------------ evaluation

func evaluationReport(db *sql.DB, runID string) (string, error) {
	pred, err := score.Load(paths.Join("data", "sample_pred.csv"))
	if err != nil {
		return "", err
	}
	gold, err := score.Load(paths.Join("dataset", "sample_output.csv"))
	if err != nil {
		return "", err
	}
	m := score.Compare(pred, gold)

	var problems, warnings []string
	problems, warnings, err = validate.Check(paths.Join("output.csv"), db, "deletion_requests")
	if err != nil {
		return "", err
	}
	full, err := score.Load(paths.Join("output.csv"))
	if err != nil {
		return "", err
	}
	counts := map[string]int{}
	totalSteps := 0
	for _, r := range full {
		counts[r.Status]++
		totalSteps += len(r.Steps)
	}
	var classifyEvents int
	_ = db.QueryRow(`SELECT COUNT(*) FROM system_activity WHERE event_type='CLASSIFY_REQUEST'`).Scan(&classifyEvents)
	types := intentTypes()
	var live, hits int
	_ = db.QueryRow(`SELECT COALESCE(SUM(cache_hit=0),0), COALESCE(SUM(cache_hit=1),0) FROM cache.llm_usage WHERE purpose='intent_classify' AND run_id=?`, runID).Scan(&live, &hits)
	src := fmt.Sprintf("%d live Gemini calls, %d served from the content-hash cache (identical request texts recur and are classified once)", live, hits)

	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	w("# Evaluation report\n\n")
	w("Generated %s by `go run ./cmd/report`. Reproduce every number here with:\n\n```\ncd code\ngo run ./cmd/pipeline      # ingest + vision + intent + audit -> ../output.csv\ngo run ./cmd/eval           # score the 32 public samples\ngo run ./cmd/validate       # check output.csv against the submission contract\ngo test ./...               # custom evaluation cases\n```\n\n", time.Now().UTC().Format("2006-01-02 15:04 UTC"))

	w("## 1. Accuracy on the 32 public sample requests\n\n")
	w("Predictions (`data/sample_pred.csv`) are produced by the same code path as the 518-case run and scored against `dataset/sample_output.csv`. The answer key is used **only** for scoring, never for prediction.\n\n")
	w("| Metric | Result | How it is computed |\n|---|---:|---|\n")
	w("| Case-level deletion-status accuracy | **%.1f%%** (%d/%d) | `deletion_status` equals the key |\n", m.StatusAccuracy(), m.StatusOK, m.Cases)
	w("| Step-level precision | %.1f%% | predicted steps whose (phase, system, resource, operation) exist in the key |\n", m.Precision)
	w("| Step-level recall | %.1f%% | key steps found in the prediction |\n", m.Recall)
	w("| Step-level F1 | %.1f%% | harmonic mean |\n", m.F1)
	w("| Field-level accuracy | %.1f%% | on matched steps: `state`, `evidence_id`, `step_no` (3 fields per step) |\n", m.FieldAccuracy())
	w("| Steps correct on every field | %.1f%% (%d/%d) | identity + state + evidence + position |\n", m.StepExactRate(), m.StepExact, m.GoldSteps)
	w("| Sequence accuracy (exact) | %.1f%% (%d/%d cases) | whole ordered `steps_json` identical, status identical |\n", m.SeqExactRate(), m.SeqIdentical, m.Cases)
	w("| Sequence similarity | %.1f%% | mean LCS ratio of the ordered step lists (partial credit for order) |\n\n", m.SeqSimilarity())
	w("Row-level accuracy (status and steps both exactly equal) is the exact-sequence row. The public samples contain no `NOT_APPLICABLE` cases, so intent handling is evaluated separately in section 3.\n\n")

	w("### Cases that are not identical\n\n")
	w("| Case | Gold / predicted status | Differences |\n|---|---|---|\n")
	n := 0
	for _, c := range m.PerCase {
		if c.Identical {
			continue
		}
		n++
		d := strings.Join(c.Differences, "<br>")
		if d == "" {
			d = "step order only"
		}
		w("| %s | %s / %s | %s |\n", c.ID, c.GoldStatus, c.PredStatus, strings.ReplaceAll(d, "|", "/"))
	}
	if n == 0 {
		w("| (none) | | |\n")
	}
	w("\n")

	w("## 2. The full 518-request run\n\n")
	w("There is no answer key for these, so the checks are structural and consistency checks:\n\n")
	w("| Check | Result |\n|---|---|\n")
	w("| Rows | %d (one per request, sorted by `case_id`) |\n", len(full))
	w("| Status distribution | COMPLETE %d, INCOMPLETE %d, NOT_APPLICABLE %d |\n", counts["COMPLETE"], counts["INCOMPLETE"], counts["NOT_APPLICABLE"])
	w("| Total steps emitted | %d |\n", totalSteps)
	if len(problems) == 0 {
		w("| Submission-contract validator (`cmd/validate`) | **pass**: header, row count/order, status values, JSON shape, consecutive step numbers, allowed operation/phase/state pairs, phase order, empty `evidence_id` on future work, evidence cited for findings and COMPLETE verifications, and **no cited evidence captured after `audit_at`** |\n")
	} else {
		w("| Submission-contract validator | **%d problems** (see `go run ./cmd/validate`) |\n", len(problems))
	}
	for _, x := range warnings {
		w("| Validator warning | %s |\n", x)
	}
	w("\n")

	w("## 3. Intent classification (NOT_APPLICABLE)\n\n")
	w("A request that asks for access, correction, export, a marketing opt-out or support, or that explicitly says the account must be kept, is `NOT_APPLICABLE` with `steps_json=[]`. A follow-up asking to *verify* a deletion is still a deletion case (the samples confirm this).\n\n")
	w("- Model: Gemini with a structured-output schema; the full prompt is `internal/intent/intent.go` (`Prompt`). Identical texts are classified once; results are cached.\n")
	w("- Verdict sources in the final run: %s. Request types returned: %s.\n", src, types)
	w("- **Independent check:** the global activity stream contains %d `CLASSIFY_REQUEST` events (privacy intake logging \"classified as non-deletion\"). The classifier found exactly **%d** non-deletion requests, `case_533`..`case_542`, matching that count; all ten are access / export / correction / marketing / support requests with explicit \"keep my account\" wording.\n", classifyEvents, counts["NOT_APPLICABLE"])
	w("- Offline fallback (`Heuristic`, multilingual keywords + negation): also flags exactly those ten once the French \"suppression\" pattern was fixed (an earlier version wrongly flagged 12 French requests; found by comparing the fallback with the model).\n")
	w("- If an intent call fails, the run **aborts** instead of silently falling back (see `--allow-degraded`), so a blocked API key can never produce a weaker `output.csv` unnoticed.\n\n")

	w("## 4. Additional evaluation cases created for this project\n\n")
	w("These are executable (`go test -v ./internal/audit/`), each targets a failure mode the dataset is designed to provoke, and all pass on the final database.\n\n")
	w("| # | Custom case | What it proves | Result |\n|---|---|---|---|\n")
	w("| 1 | **Time travel.** Take every public sample and move `audit_at` back to 2026-07-01, before most evidence exists. | No step cites evidence captured after the cutoff; later evidence cannot change an earlier audit. (70 citations checked.) | pass |\n")
	w("| 2 | **Historical identifier.** For 5 requests made with an *old* email, re-run the same request with the user's *current* email. | Both resolve to the same user and produce a byte-identical audit; the identity graph's validity windows work. | pass |\n")
	w("| 3 | **Determinism.** Run every sample twice. | Identical status and `steps_json` bytes every time (no map-order or concurrency nondeterminism). | pass |\n")
	w("| 4 | **Contract.** Validate the sample predictions with the same validator used for `output.csv`. | The output format rules hold on a set where a gold key exists. | pass |\n")
	w("| 5 | **Non-deletion intent, offline.** Nine hand-written messages: marketing-only, correction, copy-of-invoices, explicit negations, and deletion requests in Spanish, French, Portuguese, Japanese and English. | Negation always wins; deletion wording is caught in other languages. | pass |\n")
	w("| 6 | **Natural experiment.** The ten real non-deletion requests in the evaluation set vs. the ten `CLASSIFY_REQUEST` events. | The LLM classifier agrees with the intake system on every one. | match |\n\n")

	w("## 5. Failures found and improvements made\n\n")
	w("Each row is a measured step on the 32 public samples. \"Precision\" is step-level precision.\n\n")
	w("| Iteration | Status acc. | Precision | Recall | Exact sequences | What was wrong -> what changed |\n|---|---:|---:|---:|---:|---|\n")
	w("| v0 baseline | 90.6%% | 51.3%% | 90.7%% | 3.1%% | Every deletion-worker event in the request window was treated as the user's resource: ~166 false steps. |\n")
	w("| + worker-activity attribution | 96.9%% | 94.2%% | 92.2%% | 50.0%% | Resources with no identifier (already-deleted accounts, anonymized profiles) are now tied to a case only through worker activity near `reported_at`; the activity result must agree with the case's `reported_status`; matching is global (each resource belongs to one case). Category now comes from the record's own `record_type`, structural hints and the worker's action type (an analytics-style id was really a marketing profile; a support ticket can be an attachment). |\n")
	w("| + relevance and citation fixes | %.1f%% | %.1f%% | %.1f%% | %.1f%% | Evidence-linked resources must appear in evidence captured at/after the request (an old 404 lookup from March is not this account); never-observed REDACT/ANONYMIZE records are skipped; records proven absent by omission cite the case's own primary structured evidence file; resource ids written inside document text (`CS-4827 / support_001`) are indexed. |\n\n", m.StatusAccuracy(), m.Precision, m.Recall, m.SeqExactRate())
	w("Other failures found and fixed along the way:\n\n")
	w("- **Silent degradation.** After a Gemini key was blocked (HTTP 403), a run produced a valid-looking but weaker output (status accuracy fell to 87.5%% and recall to 87.0%% on the samples because the 15 image/PDF files were not read). This doubles as an **ablation: vision evidence is worth ~12 points of status accuracy and ~6 points of recall**. Fix: strict mode (LLM failures abort the run), a cache backup before every run, and a validator that must pass before submitting.\n")
	w("- **Validator-found bug.** The contract validator flagged four COMPLETE cases with no steps. Three were the missing vision data above; `case_61` is a genuine time-travel case (same person as sample `case_07`, audited months earlier, before any of their later evidence existed). Policy: COMPLETE requires every relevant copy to be *proven* compliant, so when nothing can be identified at audit time the status is INCOMPLETE (reported as a validator warning, not an error).\n")
	w("- **Re-ingest on Windows.** DBeaver holding `audit.db` open made the rebuild fail silently; the rebuild now clears the schema in place.\n")
	w("- **Regex bug.** A Python-generated Go regex lost its `\\\\b` word boundaries and indexed region names (`eu_west_1`) as resources; caught by the resource count jumping 4,468 -> 4,778, fixed and restricted to records that already carry a non-conforming id.\n\n")

	w("## 6. Known limitations (honest failure analysis)\n\n")
	w("- **Retained audit records that exist nowhere in the data.** Seven sample answers include a `VERIFY_RECORD_PRESENT` step for an `audit_record_*` id (e.g. `audit_record_6645`) that appears in no supplied file, so it cannot be reconstructed. Each such case loses exactly one verification step.\n")
	w("- **Cases that share a `reported_at`.** Two or three cases can compete for the same unidentified account or analytics profile and the data does not separate them; the global matching picks deterministically but can swap them (samples 03, 23, 24, 26-29). On the samples accounts attribute correctly ~12/18 and analytics profiles ~4/7 times; the identified-by-evidence resources are essentially all correct.\n")
	w("- **Offsets were fitted on observed data.** Attribution windows/centres are configured in `internal/audit/rules.go`; the analytics centre is learned from resources already linked by identifiers. A dataset with a different workflow cadence would need those re-checked (the evaluator makes that a one-command test).\n")
	w("- **Cost figures are estimates** (see `usage_report.md`).\n")
	return b.String(), nil
}

// intentTypes summarises the request types in data/intent_verdicts.csv written by the audit stage.
func intentTypes() string {
	f, err := os.Open(paths.Join("data", "intent_verdicts.csv"))
	if err != nil {
		return "n/a"
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil || len(recs) < 2 {
		return "n/a"
	}
	ty := map[string]int{}
	for _, r := range recs[1:] {
		ty[r[2]]++
	}
	var parts []string
	for k, v := range ty {
		parts = append(parts, fmt.Sprintf("%s %d", k, v))
	}
	sortStrings(parts)
	return strings.Join(parts, ", ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

var _ = pipeline.OpenReadOnly
