package evidence

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// Extracted is what an extractor returns for one file.
type Extracted struct {
	DocTS   string // best-effort capture time derived from the file itself
	Records []Record
}

// ------------------------------------------------------------------ JSON

var systemKeyRe = regexp.MustCompile(`(_service|_platform|_store)$`)

type jctx struct {
	system, collection, ts string
}

// ExtractJSON walks arbitrary JSON. Any object that carries a resource_id is a record;
// surrounding keys (data_store, collection, captured_at, service-name map keys, array
// names) supply system / collection / timestamp context. No schema is hard-coded.
func ExtractJSON(data []byte) (Extracted, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return Extracted{}, err
	}
	var ex Extracted
	if m, ok := root.(map[string]any); ok {
		ex.DocTS = NormTS(firstString(m, "captured_at", "exported_at", "observed_at"))
	}
	var walk func(node any, c jctx, path string)
	walk = func(node any, c jctx, path string) {
		switch t := node.(type) {
		case map[string]any:
			if _, ok := t["resource_id"].(string); ok {
				ex.Records = append(ex.Records, jsonRecord(t, c, path, ex.DocTS))
				return
			}
			nc := c
			if v := firstString(t, "data_store", "system_id"); v != "" {
				nc.system = v
			}
			if v := firstString(t, "collection"); v != "" {
				nc.collection = v
			}
			if v := firstString(t, "captured_at", "observed_at"); v != "" {
				nc.ts = NormTS(v)
			} else if nc.ts == "" {
				nc.ts = NormTS(firstString(t, "exported_at"))
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				cc := nc
				switch t[k].(type) {
				case map[string]any:
					if systemKeyRe.MatchString(k) {
						cc.system = k
					}
				case []any:
					if k != "records" && k != "snapshots" {
						cc.collection = k
					}
				}
				walk(t[k], cc, path+"/"+k)
			}
		case []any:
			for i, e := range t {
				walk(e, c, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	walk(root, jctx{ts: ex.DocTS}, "$")
	return ex, nil
}

func jsonRecord(m map[string]any, c jctx, path, docTS string) Record {
	r := Record{
		System:     firstString(m, "system", "origin_system", "data_store", "system_id"),
		Resource:   firstString(m, "resource_id"),
		Collection: c.collection,
		RecordType: firstString(m, "record_type", "type"),
		ObservedAt: NormTS(firstString(m, "observed_at", "recorded_at", "updated_at", "captured_at", "event_time")),
		Locator:    path,
		Raw:        m,
	}
	if r.System == "" {
		r.System = c.system
	}
	if r.ObservedAt == "" {
		r.ObservedAt = c.ts
	}
	if r.ObservedAt == "" {
		r.ObservedAt = docTS
	}
	r.Snap = SnapKey{System: c.system, Collection: c.collection, TS: c.ts}
	return r
}

// ------------------------------------------------------------------ SQL snapshots

var (
	reSQLCaptured = regexp.MustCompile(`(?m)^--\s*Captured at:\s*(\S+)`)
	reSQLTopology = regexp.MustCompile(`(?m)^--\s*Database topology:\s*(\S+)`)
)

// ExtractSQL executes the SQLite-compatible snapshot in a throwaway in-memory database and
// reads back every table generically (no table or column names are assumed). JSON-valued
// columns are parsed; a record_json column is merged into the row so field paths match
// those from the JSON extractor.
func ExtractSQL(script []byte) (Extracted, error) {
	text := string(script)
	var ex Extracted
	if m := reSQLCaptured.FindStringSubmatch(text); m != nil {
		ex.DocTS = NormTS(m[1])
	}
	topology := ""
	if m := reSQLTopology.FindStringSubmatch(text); m != nil {
		topology = m[1]
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return ex, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(text); err != nil {
		return ex, fmt.Errorf("exec snapshot: %w", err)
	}
	tables, err := queryStrings(db, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return ex, err
	}
	for _, tbl := range tables {
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(tbl, `"`, `""`) + `"`)
		if err != nil {
			return ex, err
		}
		cols, _ := rows.Columns()
		rowNo := 0
		for rows.Next() {
			rowNo++
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return ex, err
			}
			m := map[string]any{}
			for i, c := range cols {
				v := vals[i]
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				if s, ok := v.(string); ok && (strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")) {
					var parsed any
					d := json.NewDecoder(strings.NewReader(s))
					d.UseNumber()
					if d.Decode(&parsed) == nil {
						if pm, ok := parsed.(map[string]any); ok && c == "record_json" {
							for k, pv := range pm {
								m[k] = pv
							}
							continue
						}
						v = parsed
					}
				}
				m[c] = v
			}
			rec := Record{
				System:     firstString(m, "origin_system", "system", "system_id"),
				Resource:   firstString(m, "resource_id"),
				Collection: firstString(m, "collection_name", "collection"),
				RecordType: firstString(m, "record_type", "type"),
				ObservedAt: NormTS(firstString(m, "updated_at", "observed_at", "recorded_at")),
				Locator:    fmt.Sprintf("%s#row%d", tbl, rowNo),
				Raw:        m,
			}
			if rec.ObservedAt == "" {
				rec.ObservedAt = ex.DocTS
			}
			rec.Snap = SnapKey{Topology: topology, Table: tbl, TS: ex.DocTS}
			ex.Records = append(ex.Records, rec)
		}
		rows.Close()
	}
	return ex, nil
}

func queryStrings(db *sql.DB, q string) ([]string, error) {
	rows, err := db.Query(q)
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

// ------------------------------------------------------------------ logs

var (
	reLogTS    = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z)\s+(.*)$`)
	reLogfmtKV = regexp.MustCompile(`([A-Za-z_][\w.\-]*)=("(?:[^"\\]|\\.)*"|\S*)`)
)

// ExtractLog parses logfmt (`ts k=v k="v"`) and JSON-line (`ts {...}`) logs, one record per line.
func ExtractLog(data []byte) (Extracted, error) {
	var ex Extracted
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		m := reLogTS.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ts, rest := NormTS(m[1]), strings.TrimSpace(m[2])
		attrs := map[string]any{}
		if strings.HasPrefix(rest, "{") {
			d := json.NewDecoder(strings.NewReader(rest))
			d.UseNumber()
			if err := d.Decode(&attrs); err != nil {
				attrs = map[string]any{"message": rest}
			}
		} else {
			for _, kv := range reLogfmtKV.FindAllStringSubmatch(rest, -1) {
				v := kv[2]
				if strings.HasPrefix(v, `"`) {
					if u, err := unquote(v); err == nil {
						v = u
					}
				}
				attrs[kv[1]] = v
			}
			if len(attrs) == 0 {
				attrs["message"] = rest
			}
		}
		attrs["timestamp"] = ts
		if ex.DocTS == "" || ts > ex.DocTS {
			ex.DocTS = ts
		}
		rec := Record{
			System:     firstString(attrs, "service", "logger"),
			Resource:   firstString(attrs, "resource_id"),
			Collection: "log_events",
			RecordType: firstString(attrs, "event"),
			ObservedAt: ts,
			Locator:    fmt.Sprintf("line %d", i+1),
			Trace:      firstString(attrs, "trace_id"),
			Raw:        attrs,
		}
		rec.Snap = SnapKey{Collection: "log_events"}
		ex.Records = append(ex.Records, rec)
	}
	return ex, nil
}

func unquote(s string) (string, error) {
	var out string
	err := json.Unmarshal([]byte(s), &out)
	return out, err
}

// ------------------------------------------------------------------ pipe-delimited text

var reSnapshotLine = regexp.MustCompile(`(?i)^snapshot:\s*(\S+)`)

// ExtractText parses `ts | system | resource | TYPE | RESULT | subject` lines. Lines with
// another shape are kept as free-text records so no content is silently dropped.
func ExtractText(data []byte) (Extracted, error) {
	var ex Extracted
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := reSnapshotLine.FindStringSubmatch(line); m != nil {
			ex.DocTS = NormTS(m[1])
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		parts := strings.Split(line, " | ")
		if len(parts) >= 5 && reLogTS.MatchString(parts[0]+" x") {
			raw := map[string]any{
				"observed_at": NormTS(parts[0]), "system": strings.TrimSpace(parts[1]),
				"resource_id": strings.TrimSpace(parts[2]), "record_type": strings.TrimSpace(parts[3]),
				"result": strings.TrimSpace(parts[4]),
			}
			if len(parts) > 5 {
				raw["subject_reference"] = strings.TrimSpace(strings.Join(parts[5:], " | "))
			}
			ex.Records = append(ex.Records, Record{
				Snap:   SnapKey{Collection: "text_records", TS: ex.DocTS},
				System: raw["system"].(string), Resource: raw["resource_id"].(string),
				Collection: "text_records", RecordType: raw["record_type"].(string),
				ObservedAt: raw["observed_at"].(string), Locator: fmt.Sprintf("line %d", i+1), Raw: raw,
			})
			continue
		}
		ex.Records = append(ex.Records, Record{
			Snap:       SnapKey{Collection: "text_lines", TS: ex.DocTS},
			Collection: "text_lines", ObservedAt: ex.DocTS,
			Locator: fmt.Sprintf("line %d", i+1), Raw: map[string]any{"text": line},
		})
	}
	return ex, nil
}
