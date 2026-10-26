// Package store owns the SQLite databases: a disposable audit.db that is rebuilt from
// dataset/ on every ingest, and a persistent cache.db (vision extractions + LLM usage)
// attached to it as "cache".
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const cacheSchema = `
CREATE TABLE IF NOT EXISTS cache.vision_cache (
    cache_key     TEXT PRIMARY KEY,   -- sha256 | model | prompt hash
    sha256        TEXT NOT NULL,
    model         TEXT NOT NULL,
    prompt_hash   TEXT NOT NULL,
    response_json TEXT NOT NULL CHECK (json_valid(response_json)),
    input_tokens  INTEGER,
    output_tokens INTEGER,
    cost_usd      REAL,
    created_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS cache.llm_usage (
    usage_id      INTEGER PRIMARY KEY,
    run_id        TEXT NOT NULL,
    ts            TEXT NOT NULL,
    purpose       TEXT NOT NULL,      -- vision_extract | intent | ...
    ref           TEXT,               -- file path or case_id
    provider      TEXT NOT NULL,
    model         TEXT NOT NULL,
    input_tokens  INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    latency_ms    INTEGER,
    cost_usd      REAL,
    cache_hit     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS cache.idx_usage_run ON llm_usage(run_id);
CREATE TABLE IF NOT EXISTS cache.intent_cache (
    cache_key     TEXT PRIMARY KEY,   -- sha256(request_text) | model | prompt hash
    model         TEXT NOT NULL,
    prompt_hash   TEXT NOT NULL,
    response_json TEXT NOT NULL CHECK (json_valid(response_json)),
    input_tokens  INTEGER,
    output_tokens INTEGER,
    cost_usd      REAL,
    created_at    TEXT NOT NULL
);
`

// Open (re)creates auditPath from scratch and attaches the persistent cache DB.
// One connection only: ATTACH and PRAGMAs are per-connection, and ingestion is single-writer anyway.
func Open(auditPath, cachePath string, rebuild bool) (*sql.DB, error) {
	wipe := false
	if rebuild {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			if err := os.Remove(auditPath + suffix); err != nil && !os.IsNotExist(err) {
				wipe = true // e.g. DBeaver holds the file open on Windows: clear the schema in place instead
			}
		}
	}
	for _, p := range []string{auditPath, cachePath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(auditPath))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		fmt.Sprintf(`ATTACH DATABASE '%s' AS cache`, filepath.ToSlash(cachePath)),
		cacheSchema,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("init %q: %w", firstLine(stmt), err)
		}
	}
	if wipe {
		if err := dropEverything(db); err != nil {
			db.Close()
			return nil, fmt.Errorf("audit.db is locked by another program and could not be cleared: %w", err)
		}
	}
	return db, nil
}

// dropEverything removes every view, table and index in the main schema (not the attached cache).
func dropEverything(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	for _, kind := range []string{"view", "table"} {
		rows, err := db.Query(`SELECT name FROM main.sqlite_master WHERE type = ? AND name NOT LIKE 'sqlite_%'`, kind)
		if err != nil {
			return err
		}
		var names []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return err
			}
			names = append(names, n)
		}
		rows.Close()
		for _, n := range names {
			if _, err := db.Exec(fmt.Sprintf(`DROP %s IF EXISTS main."%s"`, kind, n)); err != nil {
				return err
			}
		}
	}
	_, err := db.Exec(`PRAGMA foreign_keys = ON`)
	return err
}

// CreateCurated creates the derived relational tables. Call after raw CSV tables exist
// (the schema's foreign keys and views reference evidence_files / deletion_requests).
func CreateCurated(db *sql.DB) error {
	_, err := db.Exec(curatedSchema)
	return err
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
