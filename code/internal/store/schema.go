package store

// curatedSchema is the relational layer derived from the raw CSVs and the evidence files.
// It lives in Go source (not a .sql file) so it is reviewed with the code; read it top to bottom
// for the whole data model: identity graph, evidence documents/snapshots/records/fields,
// record-to-user links, resources and the views that tie requests to users.
const curatedSchema = `
-- Curated relational layer. Raw CSV tables (deletion_requests, user_identities,
-- user_audit_log, deletion_rules, system_activity, evidence_files, ...) are created
-- dynamically from their headers by the CSV loader; everything below is derived
-- from them and from the evidence files, and is rebuilt on every ingest.

-- ---------------------------------------------------------------- identity graph
CREATE TABLE users (
    user_id      TEXT PRIMARY KEY,
    in_snapshot  INTEGER NOT NULL,          -- 1 if present in the current identity snapshot
    first_seen   TEXT                       -- earliest audit event, if any
);

-- Every identifier a user has ever held (current + historical), with validity windows.
CREATE TABLE identifiers (
    identifier_id INTEGER PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(user_id),
    kind          TEXT NOT NULL CHECK (kind IN ('user_id','email','phone','email_hash','name')),
    value         TEXT NOT NULL,
    norm_value    TEXT NOT NULL,
    valid_from    TEXT,                     -- NULL = since the beginning of time
    valid_to      TEXT,                     -- NULL = still current
    source        TEXT NOT NULL,            -- snapshot | audit_old | audit_new | derived_hash
    source_ref    TEXT                      -- audit_id for audit-derived rows
);
CREATE INDEX idx_identifiers_lookup ON identifiers(kind, norm_value);
CREATE INDEX idx_identifiers_user   ON identifiers(user_id);

-- ---------------------------------------------------------------- evidence
-- One row per physical file under dataset/evidence (manifest-joined).
CREATE TABLE evidence_documents (
    doc_id        INTEGER PRIMARY KEY,
    file_id       TEXT UNIQUE REFERENCES evidence_files(file_id),  -- NULL if not in manifest
    path          TEXT NOT NULL UNIQUE,     -- relative to dataset/
    kind          TEXT NOT NULL,            -- sql | json | log | txt | image | pdf | other
    sha256        TEXT NOT NULL,
    bytes         INTEGER NOT NULL,
    captured_at   TEXT,                     -- manifest value, else header-derived
    origin        TEXT,
    extractor     TEXT NOT NULL,            -- sqlite | json | logfmt | pipe | gemini_vision | none
    status        TEXT NOT NULL,            -- ok | missing | error | skipped
    error         TEXT
);

-- A file can hold several point-in-time slices (JSON bundles) or tables (SQL replicas).
CREATE TABLE evidence_snapshots (
    snapshot_id   INTEGER PRIMARY KEY,
    doc_id        INTEGER NOT NULL REFERENCES evidence_documents(doc_id),
    system_id     TEXT,
    collection    TEXT,
    topology      TEXT,                     -- primary | read_replica | failover_replica | ... (SQL only)
    table_name    TEXT,                     -- SQL table the rows came from
    captured_at   TEXT
);
CREATE INDEX idx_snap_doc ON evidence_snapshots(doc_id);
CREATE INDEX idx_snap_time ON evidence_snapshots(captured_at);

CREATE TABLE evidence_records (
    record_id     INTEGER PRIMARY KEY,
    doc_id        INTEGER NOT NULL REFERENCES evidence_documents(doc_id),
    snapshot_id   INTEGER REFERENCES evidence_snapshots(snapshot_id),
    system_id     TEXT,
    system_inferred INTEGER NOT NULL DEFAULT 0,  -- 1 if system_id was backfilled from the resource prefix
    resource_id   TEXT,
    collection    TEXT,
    record_type   TEXT,                     -- as reported by the source (e.g. USER_PROFILE)
    observed_at   TEXT,                     -- record's own timestamp, else snapshot, else file
    locator       TEXT,                     -- line number / json path / sql row / image region
    trace_id      TEXT,
    extractor     TEXT NOT NULL,
    raw_json      TEXT NOT NULL CHECK (json_valid(raw_json))
);
CREATE INDEX idx_rec_resource ON evidence_records(resource_id);
CREATE INDEX idx_rec_doc      ON evidence_records(doc_id);
CREATE INDEX idx_rec_time     ON evidence_records(observed_at);
CREATE INDEX idx_rec_system   ON evidence_records(system_id, collection);
CREATE INDEX idx_rec_trace    ON evidence_records(trace_id);

-- Every leaf value of every record, flattened. This is the search surface.
CREATE TABLE evidence_fields (
    field_id    INTEGER PRIMARY KEY,
    record_id   INTEGER NOT NULL REFERENCES evidence_records(record_id) ON DELETE CASCADE,
    path        TEXT NOT NULL,              -- e.g. document.email, metrics.sessions_30d
    key         TEXT NOT NULL,              -- last path segment
    value       TEXT,
    norm_value  TEXT,
    value_kind  TEXT NOT NULL               -- email | email_hash | phone | user_id | resource | name | text | null
);
CREATE INDEX idx_fld_record ON evidence_fields(record_id);
CREATE INDEX idx_fld_norm   ON evidence_fields(value_kind, norm_value);
CREATE INDEX idx_fld_key    ON evidence_fields(key);

-- Evidence record -> user links, produced by joining evidence_fields to identifiers.
CREATE TABLE record_identifiers (
    record_id     INTEGER NOT NULL REFERENCES evidence_records(record_id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL REFERENCES users(user_id),
    identifier_id INTEGER NOT NULL REFERENCES identifiers(identifier_id),
    field_id      INTEGER REFERENCES evidence_fields(field_id),
    match_type    TEXT NOT NULL,            -- direct | trace
    confidence    REAL NOT NULL,
    valid_at_obs  INTEGER,                  -- 1 if the identifier was held by the user at observed_at
    PRIMARY KEY (record_id, identifier_id, match_type)
);
CREATE INDEX idx_recid_user ON record_identifiers(user_id);

-- ---------------------------------------------------------------- resources
CREATE TABLE systems (
    system_id TEXT PRIMARY KEY
);
CREATE TABLE resources (
    resource_id TEXT PRIMARY KEY,
    system_id   TEXT REFERENCES systems(system_id),   -- first system that reported it
    prefix      TEXT                                   -- e.g. account, search, backup
);
CREATE INDEX idx_resources_prefix ON resources(prefix);

-- Which user a resource belongs to, from every grounded link we can find.
CREATE TABLE resource_links (
    resource_id TEXT NOT NULL REFERENCES resources(resource_id),
    user_id     TEXT NOT NULL REFERENCES users(user_id),
    source      TEXT NOT NULL,              -- evidence | activity_link
    confidence  REAL NOT NULL,
    source_ref  TEXT,                       -- activity_id for activity_link
    PRIMARY KEY (resource_id, user_id, source)
);
CREATE INDEX idx_reslink_user ON resource_links(user_id);

-- ---------------------------------------------------------------- samples
CREATE TABLE sample_output_steps (
    case_id     TEXT NOT NULL,
    idx         INTEGER NOT NULL,
    step_no     INTEGER,
    phase       TEXT,
    system_id   TEXT,
    resource_id TEXT,
    operation   TEXT,
    state       TEXT,
    evidence_id TEXT,
    PRIMARY KEY (case_id, idx)
);

-- ---------------------------------------------------------------- convenience views
-- Request email -> user, via ANY identifier the user has held (current or historical).
CREATE VIEW request_subjects AS
SELECT r.case_id, i.user_id, i.identifier_id, i.source, i.valid_from, i.valid_to,
       CASE WHEN i.valid_to IS NULL THEN 'current' ELSE 'historical' END AS email_form
FROM deletion_requests r
JOIN identifiers i ON i.kind = 'email' AND i.norm_value = lower(trim(r.email));

-- Everything we know about a resource across all evidence, time-ordered.
CREATE VIEW resource_observations AS
SELECT rec.resource_id, rec.system_id, rec.collection, rec.record_type, rec.observed_at,
       rec.record_id, d.file_id, d.path, rec.extractor
FROM evidence_records rec JOIN evidence_documents d ON d.doc_id = rec.doc_id
WHERE rec.resource_id IS NOT NULL;
`
