// Package pipeline wires the stages together. Each stage is also exposed as its own command
// (cmd/ingest, cmd/audit); cmd/pipeline runs them back to back under one run id so the usage
// report covers exactly the run that produced output.csv.
package pipeline

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"ghostaudit/internal/audit"
	"ghostaudit/internal/envfile"
	"ghostaudit/internal/evidence"
	"ghostaudit/internal/gemini"
	"ghostaudit/internal/intent"
	"ghostaudit/internal/paths"
	"ghostaudit/internal/store"
)

// Options configures a run. Zero values pick repo-relative defaults.
type Options struct {
	Dataset   string
	DBPath    string
	CachePath string
	EnvPath   string
	Out       string // predictions CSV
	Sample    bool   // audit the public sample requests instead of the evaluation requests
	NoVision  bool
	NoIntent  bool // use the keyword heuristic instead of Gemini
	Verbose   bool
	// AllowDegraded lets a run continue when an LLM step fails (vision errors, intent calls that
	// fell back to keywords). Off by default so a blocked API key can never silently produce a
	// weaker output.csv.
	AllowDegraded bool
	RunID         string
	Logf          func(format string, args ...any)
}

// Defaults fills unset fields and loads .env.
func (o *Options) Defaults() error {
	if o.Dataset == "" {
		o.Dataset = paths.Join("dataset")
	}
	if o.DBPath == "" {
		o.DBPath = paths.Join("data", "audit.db")
	}
	if o.CachePath == "" {
		o.CachePath = paths.Join("data", "cache.db")
	}
	if o.EnvPath == "" {
		o.EnvPath = paths.Join(".env")
	}
	if o.RunID == "" {
		o.RunID = time.Now().UTC().Format("20060102T150405Z")
	}
	if o.Logf == nil {
		o.Logf = func(f string, a ...any) { fmt.Printf(f+"\n", a...) }
	}
	if o.Out == "" {
		if o.Sample {
			o.Out = paths.Join("data", "sample_pred.csv")
		} else {
			o.Out = paths.Join("output.csv")
		}
	}
	return envfile.Load(o.EnvPath)
}

// backupCache copies cache.db (paid LLM results) aside before a run touches it.
func backupCache(path string, logf func(string, ...any)) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return
	}
	if err := os.WriteFile(path+".bak", b, 0o644); err == nil {
		logf("cache backup -> %s.bak", filepath.Base(path))
	}
}

// Ingest rebuilds the audit database from dataset/.
func Ingest(ctx context.Context, o Options) error {
	logf := o.Logf
	var gc *gemini.Client
	if !o.NoVision {
		var err error
		if gc, err = gemini.FromEnv("GEMINI_VISION_MODEL", "GEMINI_MODEL"); err != nil {
			logf("note: %v -> images/PDFs will be registered but not read", err)
			gc = nil
		}
	}
	start := time.Now()
	backupCache(o.CachePath, logf)
	db, err := store.Open(o.DBPath, o.CachePath, true)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := store.CreateCurated(db); err != nil {
		return fmt.Errorf("create curated schema: %w", err)
	}
	tables, err := store.LoadCSVDir(db, o.Dataset)
	if err != nil {
		return err
	}
	for _, t := range tables {
		pk := t.PK
		if pk == "" {
			pk = "-"
		}
		logf("csv   %-26s %6d rows  pk=%s", t.Name, t.Rows, pk)
	}
	users, idents, err := store.BuildIdentityGraph(db)
	if err != nil {
		return fmt.Errorf("identity graph: %w", err)
	}
	logf("identity graph: %d users, %d identifiers", users, idents)

	conc := 4
	fmt.Sscanf(os.Getenv("GEMINI_CONCURRENCY"), "%d", &conc)
	sum, err := evidence.Ingest(ctx, db, evidence.Options{DatasetDir: o.Dataset, Gemini: gc, Concurrency: conc, RunID: o.RunID, Logf: logf})
	if err != nil {
		return fmt.Errorf("evidence: %w", err)
	}
	if o.Verbose {
		for _, d := range sum.PerDoc {
			logf("  %-8s %-7s %5d recs  %s %s", d.Kind, d.Status, d.Records, d.Path, d.Err)
		}
	}
	logf("evidence: %d files (ok=%d error=%d skipped=%d missing=%d), %d records, %d fields; vision %d jobs (%d cache hits)",
		sum.Docs, sum.OK, sum.Errors, sum.Skipped, sum.Missing, sum.Records, sum.Fields, sum.VisionJobs, sum.VisionCacheHits)
	for _, d := range sum.PerDoc {
		if d.Status == "error" {
			logf("  ERROR %s: %s", d.Path, d.Err)
		}
	}
	ls, err := store.BuildLinks(db)
	if err != nil {
		return fmt.Errorf("links: %w", err)
	}
	logf("links: %d direct + %d trace record->user links; %d systems, %d resources", ls.Direct, ls.Trace, ls.Systems, ls.Resources)
	var matched, total int
	if err := db.QueryRow(`SELECT COUNT(DISTINCT case_id), (SELECT COUNT(*) FROM deletion_requests) FROM request_subjects`).Scan(&matched, &total); err == nil {
		logf("requests resolved to a user: %d / %d", matched, total)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return err
	}
	logf("ingest done in %s -> %s", time.Since(start).Round(time.Millisecond), o.DBPath)
	if sum.Errors > 0 && !o.AllowDegraded {
		return fmt.Errorf("%d evidence file(s) failed to extract (see ERROR lines above); fix the cause and re-run, or pass --allow-degraded", sum.Errors)
	}
	return nil
}

// AuditSummary reports what an audit run produced.
type AuditSummary struct {
	Cases                      int
	Status                     map[string]int
	NotApplicable, IntentCalls int
	Verdicts                   map[string]intent.Verdict
}

// Audit classifies intent, audits every authorized case and writes the predictions CSV.
func Audit(ctx context.Context, o Options) (AuditSummary, error) {
	sum := AuditSummary{Status: map[string]int{}}
	db, err := store.Open(o.DBPath, o.CachePath, false)
	if err != nil {
		return sum, err
	}
	defer db.Close()

	eng, err := audit.New(db)
	if err != nil {
		return sum, err
	}
	table := "deletion_requests"
	if o.Sample {
		table = "sample_deletion_requests"
	}
	cases, err := eng.Cases(table)
	if err != nil {
		return sum, err
	}

	var gc *gemini.Client
	if !o.NoIntent {
		if gc, err = gemini.FromEnv("GEMINI_MODEL", "GEMINI_VISION_MODEL"); err != nil {
			o.Logf("note: %v -> using the keyword heuristic for intent", err)
			gc = nil
		}
	}
	texts := make(map[string]string, len(cases))
	for _, c := range cases {
		texts[c.ID] = c.Text
	}
	conc := 8
	fmt.Sscanf(os.Getenv("INTENT_CONCURRENCY"), "%d", &conc)
	cl := &intent.Classifier{Client: gc, DB: db, RunID: o.RunID, Concurrency: conc}
	sum.Verdicts = cl.ClassifyAll(ctx, texts)

	fb := 0
	for _, v := range sum.Verdicts {
		if v.Source == "heuristic-fallback" {
			fb++
		}
	}
	if fb > 0 && !o.AllowDegraded {
		return sum, fmt.Errorf("%d intent call(s) failed and fell back to keywords; fix the Gemini error and re-run, or pass --allow-degraded", fb)
	}
	if err := os.MkdirAll(filepath.Dir(o.Out), 0o755); err != nil {
		return sum, err
	}
	f, err := os.Create(o.Out)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"case_id", "deletion_status", "steps_json"})
	for _, c := range cases {
		v := sum.Verdicts[c.ID]
		var r audit.Result
		if !v.AuthorizesDeletion {
			r = audit.Result{CaseID: c.ID, Status: "NOT_APPLICABLE"}
			sum.NotApplicable++
		} else if r, err = eng.Run(c); err != nil {
			return sum, fmt.Errorf("%s: %w", c.ID, err)
		}
		steps := r.Steps
		if steps == nil {
			steps = []audit.Step{}
		}
		js, err := json.Marshal(steps)
		if err != nil {
			return sum, err
		}
		_ = w.Write([]string{c.ID, r.Status, string(js)})
		sum.Status[r.Status]++
		sum.Cases++
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return sum, err
	}
	return sum, writeVerdicts(o, cases, sum.Verdicts)
}

// writeVerdicts keeps a reviewable record of every intent decision next to the database.
func writeVerdicts(o Options, cases []audit.Case, v map[string]intent.Verdict) error {
	p := filepath.Join(filepath.Dir(o.DBPath), "intent_verdicts.csv")
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"case_id", "authorizes_deletion", "request_type", "language", "source", "reason", "request_text"})
	ids := make([]string, 0, len(cases))
	byID := map[string]audit.Case{}
	for _, c := range cases {
		ids = append(ids, c.ID)
		byID[c.ID] = c
	}
	sort.Strings(ids)
	for _, id := range ids {
		x := v[id]
		_ = w.Write([]string{id, fmt.Sprint(x.AuthorizesDeletion), x.RequestType, x.Language, x.Source, x.Reason, byID[id].Text})
	}
	w.Flush()
	return w.Error()
}

// OpenReadOnly opens the audit database for queries.
func OpenReadOnly(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
}
