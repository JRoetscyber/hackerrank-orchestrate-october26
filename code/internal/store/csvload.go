package store

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	reIdent    = regexp.MustCompile(`[^a-z0-9_]+`)
	reIdxCol   = regexp.MustCompile(`(^|_)(id|email|phone|hash|resource|system|status|type|name)$|_at$|_time$|^(email|phone|name)`)
	skipTables = map[string]bool{"output": true} // blank prediction template, not data
)

// LoadedTable describes one raw table created from a CSV.
type LoadedTable struct {
	Name    string
	Rows    int
	Columns []string
	PK      string
}

// LoadCSVDir loads every *.csv in dir into a table named after the file. Columns come from
// the header, so new/renamed files or extra columns need no code change. The first column
// becomes the PRIMARY KEY when it looks like an id and is unique; id/email/phone/hash/time-like
// columns are indexed automatically.
func LoadCSVDir(db *sql.DB, dir string) ([]LoadedTable, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.csv"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []LoadedTable
	for _, p := range paths {
		stem := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		if skipTables[strings.ToLower(stem)] {
			continue
		}
		lt, err := loadCSV(db, p, ident(stem))
		if err != nil {
			return out, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, lt)
	}
	return out, nil
}

func ident(s string) string {
	s = strings.Trim(reIdent.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "_"), "_")
	if s == "" {
		s = "col"
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "c_" + s
	}
	return s
}

func loadCSV(db *sql.DB, path, table string) (LoadedTable, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return LoadedTable{}, err
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	r := csv.NewReader(bytes.NewReader(b))
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		return LoadedTable{}, err
	}
	if len(recs) == 0 {
		return LoadedTable{}, fmt.Errorf("empty csv")
	}
	cols := make([]string, len(recs[0]))
	seen := map[string]int{}
	for i, h := range recs[0] {
		c := ident(h)
		if n := seen[c]; n > 0 {
			c = fmt.Sprintf("%s_%d", c, n+1)
		}
		seen[c]++
		cols[i] = c
	}
	data := recs[1:]

	pk := ""
	if looksLikeID(cols[0]) && uniqueNonEmpty(data, 0) {
		pk = cols[0]
	}
	defs := make([]string, len(cols))
	for i, c := range cols {
		defs[i] = `"` + c + `" TEXT`
		if c == pk {
			defs[i] += " PRIMARY KEY"
		}
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE TABLE "%s" (%s)`, table, strings.Join(defs, ", "))); err != nil {
		return LoadedTable{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return LoadedTable{}, err
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	stmt, err := tx.Prepare(fmt.Sprintf(`INSERT INTO "%s" VALUES (%s)`, table, ph))
	if err != nil {
		tx.Rollback()
		return LoadedTable{}, err
	}
	for _, row := range data {
		args := make([]any, len(cols))
		for i := range cols {
			if i < len(row) {
				args[i] = row[i]
			}
		}
		if _, err := stmt.Exec(args...); err != nil {
			tx.Rollback()
			return LoadedTable{}, err
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		return LoadedTable{}, err
	}
	for _, c := range cols {
		if c != pk && reIdxCol.MatchString(c) {
			if _, err := db.Exec(fmt.Sprintf(`CREATE INDEX "idx_%s_%s" ON "%s"("%s")`, table, c, table, c)); err != nil {
				return LoadedTable{}, err
			}
		}
	}
	lt := LoadedTable{Name: table, Rows: len(data), Columns: cols, PK: pk}
	if hasCol(cols, "steps_json") && hasCol(cols, "case_id") {
		if err := expandSteps(db, table, cols); err != nil {
			return lt, err
		}
	}
	return lt, nil
}

func looksLikeID(c string) bool { return c == "id" || strings.HasSuffix(c, "_id") }

func hasCol(cols []string, c string) bool {
	for _, x := range cols {
		if x == c {
			return true
		}
	}
	return false
}

func uniqueNonEmpty(rows [][]string, col int) bool {
	seen := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		if col >= len(r) || r[col] == "" {
			return false
		}
		if _, dup := seen[r[col]]; dup {
			return false
		}
		seen[r[col]] = struct{}{}
	}
	return true
}

// expandSteps unnests steps_json from any CSV that has (case_id, steps_json) into the
// relational sample_output_steps table, so expected answers can be joined and diffed in SQL.
func expandSteps(db *sql.DB, table string, _ []string) error {
	rows, err := db.Query(fmt.Sprintf(`SELECT case_id, steps_json FROM "%s"`, table))
	if err != nil {
		return err
	}
	type pair struct{ id, js string }
	var all []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.js); err != nil {
			rows.Close()
			return err
		}
		all = append(all, p)
	}
	rows.Close()
	for _, p := range all {
		var steps []map[string]any
		if json.Unmarshal([]byte(p.js), &steps) != nil {
			continue
		}
		for i, s := range steps {
			get := func(k string) any {
				if v, ok := s[k]; ok {
					return v
				}
				return nil
			}
			if _, err := db.Exec(`INSERT OR REPLACE INTO sample_output_steps
				(case_id, idx, step_no, phase, system_id, resource_id, operation, state, evidence_id)
				VALUES (?,?,?,?,?,?,?,?,?)`, p.id, i, get("step_no"), get("phase"), get("system_id"),
				get("resource_id"), get("operation"), get("state"), get("evidence_id")); err != nil {
				return err
			}
		}
	}
	return nil
}
