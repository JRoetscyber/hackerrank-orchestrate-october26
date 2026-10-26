package evidence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ghostaudit/internal/gemini"
)

// Options controls an evidence ingest.
type Options struct {
	DatasetDir  string
	Gemini      *gemini.Client // nil => images/PDFs are registered but skipped
	Concurrency int
	RunID       string
	Logf        func(format string, args ...any)
}

// Summary is printed by the CLI after ingestion.
type Summary struct {
	Docs, OK, Errors, Skipped, Missing int
	Records, Fields                    int
	VisionJobs, VisionCacheHits        int
	PerDoc                             []DocSummary
}

// DocSummary is one file's outcome.
type DocSummary struct {
	Path, Kind, Status, Err string
	Records                 int
}

type manifestEntry struct {
	fileID, origin, capturedAt string
}

func kindOf(path string) (kind, mime string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".sql":
		return "sql", ""
	case ".json":
		return "json", ""
	case ".log":
		return "log", ""
	case ".txt":
		return "txt", ""
	case ".png":
		return "image", "image/png"
	case ".jpg", ".jpeg":
		return "image", "image/jpeg"
	case ".webp":
		return "image", "image/webp"
	case ".pdf":
		return "pdf", "application/pdf"
	}
	return "other", ""
}

type pendingDoc struct {
	docID int64
	path  string // relative to dataset, forward slashes
	kind  string
	mime  string
	data  []byte
	sha   string
	ts    string // manifest captured_at
}

// Ingest registers every file under dataset/evidence (and every manifest row), extracts records
// from each, and writes them. Text formats are parsed deterministically; images and PDFs go
// through Gemini vision (cached by content hash). The directory is walked, not assumed:
// files absent from the manifest are still ingested (with file_id NULL).
func Ingest(ctx context.Context, db *sql.DB, o Options) (Summary, error) {
	var sum Summary
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	manifest := map[string]manifestEntry{}
	rows, err := db.Query(`SELECT file_id, file_path, COALESCE(origin,''), COALESCE(captured_at,'') FROM evidence_files`)
	if err != nil {
		return sum, fmt.Errorf("read manifest: %w", err)
	}
	for rows.Next() {
		var e manifestEntry
		var p string
		if err := rows.Scan(&e.fileID, &p, &e.origin, &e.capturedAt); err != nil {
			rows.Close()
			return sum, err
		}
		manifest[filepath.ToSlash(p)] = e
	}
	rows.Close()

	root := filepath.Join(o.DatasetDir, "evidence")
	var onDisk []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(o.DatasetDir, p)
			onDisk = append(onDisk, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return sum, err
	}
	sort.Strings(onDisk)

	tx, err := db.Begin()
	if err != nil {
		return sum, err
	}
	defer tx.Rollback()

	insDoc, err := tx.Prepare(`INSERT INTO evidence_documents (file_id, path, kind, sha256, bytes, captured_at, origin, extractor, status, error) VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return sum, err
	}
	sink, err := newSink(tx)
	if err != nil {
		return sum, err
	}

	var visionDocs []pendingDoc
	present := map[string]bool{}
	for _, rel := range onDisk {
		present[rel] = true
		data, err := os.ReadFile(filepath.Join(o.DatasetDir, filepath.FromSlash(rel)))
		if err != nil {
			return sum, err
		}
		h := sha256.Sum256(data)
		sha := hex.EncodeToString(h[:])
		kind, mime := kindOf(rel)
		me, inManifest := manifest[rel]
		var fileID any
		if inManifest {
			fileID = me.fileID
		} else {
			logf("warn: %s is not in the evidence manifest", rel)
		}
		ts := NormTS(me.capturedAt)
		sum.Docs++

		var ex Extracted
		var xerr error
		extractor := "none"
		switch kind {
		case "sql":
			extractor = "sqlite"
			ex, xerr = ExtractSQL(data)
		case "json":
			extractor = "json"
			ex, xerr = ExtractJSON(data)
		case "log":
			extractor = "logfmt"
			ex, xerr = ExtractLog(data)
		case "txt":
			extractor = "pipe_text"
			ex, xerr = ExtractText(data)
		case "image", "pdf":
			extractor = "gemini_vision"
		}

		docCapture := firstNonEmpty(ts, ex.DocTS)
		status, errMsg := "ok", ""
		switch {
		case xerr != nil:
			status, errMsg = "error", xerr.Error()
		case kind == "image" || kind == "pdf":
			if o.Gemini == nil {
				status, errMsg = "skipped", "no GEMINI_API_KEY; vision extraction not run"
			} else {
				status = "pending"
			}
		case kind == "other":
			status, extractor = "skipped", "none"
		}
		res, err := insDoc.Exec(fileID, rel, kind, sha, len(data), nullIf(docCapture), nullIf(me.origin), extractor, status, nullIf(errMsg))
		if err != nil {
			return sum, fmt.Errorf("register %s: %w", rel, err)
		}
		docID, _ := res.LastInsertId()

		if status == "pending" {
			visionDocs = append(visionDocs, pendingDoc{docID: docID, path: rel, kind: kind, mime: mime, data: data, sha: sha, ts: docCapture})
			continue
		}
		n := 0
		if status == "ok" {
			n, err = sink.write(docID, docCapture, ex.Records)
			if err != nil {
				return sum, fmt.Errorf("write %s: %w", rel, err)
			}
		}
		sum.tally(DocSummary{Path: rel, Kind: kind, Status: status, Err: errMsg, Records: n})
	}

	// manifest rows with no file on disk
	var missing []string
	for p := range manifest {
		if !present[p] {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)
	for _, p := range missing {
		me := manifest[p]
		kind, _ := kindOf(p)
		if _, err := insDoc.Exec(me.fileID, p, kind, "", 0, nullIf(NormTS(me.capturedAt)), nullIf(me.origin), "none", "missing", "listed in manifest but not found on disk"); err != nil {
			return sum, err
		}
		sum.Docs++
		sum.tally(DocSummary{Path: p, Kind: kind, Status: "missing", Err: "not on disk"})
	}

	sum.Records, sum.Fields = sink.records, sink.fields
	if err := tx.Commit(); err != nil {
		return sum, err
	}
	if len(visionDocs) == 0 {
		return sum, nil
	}

	// Vision pass, deliberately OUTSIDE the first transaction: every paid API response is
	// persisted to the cache the moment it arrives, so a later failure can never lose it.
	// Known systems come from what was just ingested plus the activity stream.
	systems, err := knownSystems(db)
	if err != nil {
		return sum, err
	}
	v := &Vision{Client: o.Gemini, DB: db, Systems: systems, Concurrency: o.Concurrency, RunID: o.RunID}
	jobs := make([]VisionJob, len(visionDocs))
	for i, d := range visionDocs {
		jobs[i] = VisionJob{Path: d.path, Data: d.data, MIME: d.mime, SHA: d.sha}
	}
	sum.VisionJobs = len(jobs)
	logf("vision: reading %d images/PDFs with %s", len(jobs), o.Gemini.Model)
	results := v.Run(ctx, jobs)

	tx2, err := db.Begin()
	if err != nil {
		return sum, err
	}
	defer tx2.Rollback()
	sink2, err := newSink(tx2)
	if err != nil {
		return sum, err
	}
	for _, d := range visionDocs {
		r := results[d.path]
		if r.CacheHit {
			sum.VisionCacheHits++
		}
		if r.Err != nil {
			_, _ = tx2.Exec(`UPDATE evidence_documents SET status='error', error=? WHERE doc_id=?`, r.Err.Error(), d.docID)
			sum.tally(DocSummary{Path: d.path, Kind: d.kind, Status: "error", Err: r.Err.Error()})
			continue
		}
		docTS := firstNonEmpty(d.ts, r.Ex.DocTS)
		if _, err := tx2.Exec(`UPDATE evidence_documents SET status='ok', captured_at=COALESCE(captured_at, ?) WHERE doc_id=?`, nullIf(docTS), d.docID); err != nil {
			return sum, err
		}
		n, err := sink2.write(d.docID, docTS, r.Ex.Records)
		if err != nil {
			return sum, fmt.Errorf("write %s: %w", d.path, err)
		}
		sum.tally(DocSummary{Path: d.path, Kind: d.kind, Status: "ok", Records: n})
	}
	sum.Records += sink2.records
	sum.Fields += sink2.fields
	return sum, tx2.Commit()
}

func (s *Summary) tally(d DocSummary) {
	s.PerDoc = append(s.PerDoc, d)
	switch d.Status {
	case "ok":
		s.OK++
	case "error":
		s.Errors++
	case "skipped":
		s.Skipped++
	case "missing":
		s.Missing++
	}
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func knownSystems(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`
SELECT system_id FROM (
  SELECT DISTINCT system_id FROM evidence_records WHERE system_id IS NOT NULL AND system_id <> ''
  UNION SELECT DISTINCT source_system FROM system_activity
  UNION SELECT DISTINCT target_system FROM system_activity
) WHERE system_id <> '' ORDER BY system_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------ record sink

type sink struct {
	tx                      *sql.Tx
	insSnap, insRec, insFld *sql.Stmt
	snaps                   map[string]int64
	records, fields         int
}

func newSink(tx *sql.Tx) (*sink, error) {
	s := &sink{tx: tx, snaps: map[string]int64{}}
	var err error
	if s.insSnap, err = tx.Prepare(`INSERT INTO evidence_snapshots (doc_id, system_id, collection, topology, table_name, captured_at) VALUES (?,?,?,?,?,?)`); err != nil {
		return nil, err
	}
	if s.insRec, err = tx.Prepare(`INSERT INTO evidence_records (doc_id, snapshot_id, system_id, resource_id, collection, record_type, observed_at, locator, trace_id, extractor, raw_json) VALUES (?,?,?,?,?,?,?,?,?,?,?)`); err != nil {
		return nil, err
	}
	if s.insFld, err = tx.Prepare(`INSERT INTO evidence_fields (record_id, path, key, value, norm_value, value_kind) VALUES (?,?,?,?,?,?)`); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *sink) write(docID int64, docTS string, recs []Record) (int, error) {
	var extractor string
	_ = s.tx.QueryRow(`SELECT extractor FROM evidence_documents WHERE doc_id=?`, docID).Scan(&extractor)
	for _, r := range recs {
		key := fmt.Sprintf("%d|%s|%s|%s|%s|%s", docID, r.Snap.System, r.Snap.Collection, r.Snap.Topology, r.Snap.Table, r.Snap.TS)
		sid, ok := s.snaps[key]
		if !ok {
			res, err := s.insSnap.Exec(docID, nullIf(r.Snap.System), nullIf(r.Snap.Collection), nullIf(r.Snap.Topology),
				nullIf(r.Snap.Table), nullIf(firstNonEmpty(r.Snap.TS, docTS)))
			if err != nil {
				return 0, err
			}
			sid, _ = res.LastInsertId()
			s.snaps[key] = sid
		}
		raw, err := json.Marshal(r.Raw)
		if err != nil {
			return 0, err
		}
		flds := Flatten(r.Raw)
		resource := r.Resource
		if resource != "" && !reRes.MatchString(strings.ToLower(resource)) {
			// A document-style id (CS-4827, invoice.pdf) with a resource id written inside its text:
			// index the record under that resource, keeping the original in the raw JSON.
			for _, f := range flds {
				if f.Kind == "resource" && strings.HasSuffix(f.Path, "~resource") {
					resource = f.Norm
					break
				}
			}
		}
		res, err := s.insRec.Exec(docID, sid, nullIf(r.System), nullIf(resource), nullIf(r.Collection), nullIf(r.RecordType),
			nullIf(firstNonEmpty(r.ObservedAt, docTS)), nullIf(r.Locator), nullIf(r.Trace), extractor, string(raw))
		if err != nil {
			return 0, err
		}
		rid, _ := res.LastInsertId()
		s.records++
		for _, f := range flds {
			var val any
			if f.Value != nil {
				val = *f.Value
			}
			if _, err := s.insFld.Exec(rid, f.Path, f.Key, val, nullIf(f.Norm), f.Kind); err != nil {
				return 0, err
			}
			s.fields++
		}
	}
	return len(recs), nil
}
