package store

import (
	"database/sql"
	"regexp"
	"strings"
)

// LinkStats reports how much of the evidence got connected to users.
type LinkStats struct {
	Direct, Trace, Resources, Systems int
}

// BuildLinks derives the relational glue after evidence is loaded:
//   - record_identifiers: evidence field values joined to every user identifier (current or
//     historical), with a confidence and a flag saying whether the user actually held that
//     identifier at the record's observation time;
//   - trace propagation: log lines sharing a trace_id with a linked line inherit the link
//     (a lookup line carries the email, its completion line carries the resource_id);
//   - systems / resources catalogues from evidence and the global activity stream.
func BuildLinks(db *sql.DB) (LinkStats, error) {
	var st LinkStats
	// Typed SQL tables carry a resource_id but no system column. Backfill the system from the
	// dominant system observed for the same resource prefix (data-driven, flagged as inferred).
	if _, err := db.Exec(`
CREATE TEMP TABLE prefix_system AS
SELECT prefix, system_id FROM (
  SELECT rtrim(rtrim(resource_id,'0123456789'),'_') AS prefix, system_id, COUNT(*) AS n,
         ROW_NUMBER() OVER (PARTITION BY rtrim(rtrim(resource_id,'0123456789'),'_') ORDER BY COUNT(*) DESC, system_id) AS rn
  FROM evidence_records
  WHERE resource_id IS NOT NULL AND system_id IS NOT NULL AND system_id <> ''
  GROUP BY 1, 2
) WHERE rn = 1;
UPDATE evidence_records
SET system_id = (SELECT p.system_id FROM prefix_system p WHERE p.prefix = rtrim(rtrim(evidence_records.resource_id,'0123456789'),'_')),
    system_inferred = 1
WHERE (system_id IS NULL OR system_id = '') AND resource_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM prefix_system p WHERE p.prefix = rtrim(rtrim(evidence_records.resource_id,'0123456789'),'_'));
DROP TABLE prefix_system;`); err != nil {
		return st, err
	}
	res, err := db.Exec(`
INSERT OR IGNORE INTO record_identifiers (record_id, user_id, identifier_id, field_id, match_type, confidence, valid_at_obs)
SELECT f.record_id, i.user_id, i.identifier_id, f.field_id, 'direct',
       CASE i.kind WHEN 'name' THEN 0.5 WHEN 'phone' THEN 0.9 ELSE 1.0 END,
       CASE WHEN (i.valid_from IS NULL OR r.observed_at IS NULL OR i.valid_from <= r.observed_at)
             AND (i.valid_to   IS NULL OR r.observed_at IS NULL OR r.observed_at <  i.valid_to) THEN 1 ELSE 0 END
FROM evidence_fields f
JOIN identifiers i       ON i.kind = f.value_kind AND i.norm_value = f.norm_value
JOIN evidence_records r  ON r.record_id = f.record_id
WHERE f.norm_value IS NOT NULL AND f.norm_value <> ''`)
	if err != nil {
		return st, err
	}
	n, _ := res.RowsAffected()
	st.Direct = int(n)

	res, err = db.Exec(`
INSERT OR IGNORE INTO record_identifiers (record_id, user_id, identifier_id, field_id, match_type, confidence, valid_at_obs)
SELECT r2.record_id, ri.user_id, ri.identifier_id, NULL, 'trace', ri.confidence * 0.9, ri.valid_at_obs
FROM record_identifiers ri
JOIN evidence_records r1 ON r1.record_id = ri.record_id
JOIN evidence_records r2 ON r2.trace_id = r1.trace_id AND r2.record_id <> r1.record_id
WHERE ri.match_type = 'direct' AND r1.trace_id IS NOT NULL AND r1.trace_id <> ''`)
	if err != nil {
		return st, err
	}
	n, _ = res.RowsAffected()
	st.Trace = int(n)

	for _, q := range []string{
		`INSERT OR IGNORE INTO systems SELECT DISTINCT system_id FROM evidence_records WHERE system_id IS NOT NULL AND system_id <> ''`,
		`INSERT OR IGNORE INTO systems SELECT DISTINCT source_system FROM system_activity WHERE source_system <> ''`,
		`INSERT OR IGNORE INTO systems SELECT DISTINCT target_system FROM system_activity WHERE target_system <> ''`,
		`INSERT OR IGNORE INTO resources (resource_id, system_id, prefix)
		   SELECT resource_id, MIN(system_id), rtrim(rtrim(resource_id,'0123456789'),'_')
		   FROM evidence_records WHERE resource_id IS NOT NULL AND resource_id <> '' GROUP BY resource_id`,
		`INSERT OR IGNORE INTO resources (resource_id, system_id, prefix)
		   SELECT resource_id, MIN(target_system), rtrim(rtrim(resource_id,'0123456789'),'_')
		   FROM system_activity WHERE resource_id <> '' GROUP BY resource_id`,
	} {
		if _, err := db.Exec(q); err != nil {
			return st, err
		}
	}
	if err := buildResourceLinks(db); err != nil {
		return st, err
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM resources`).Scan(&st.Resources)
	_ = db.QueryRow(`SELECT COUNT(*) FROM systems`).Scan(&st.Systems)
	return st, nil
}

var reLinkEvent = regexp.MustCompile(`(?i)linked\s+(\S+)\s+\(\S+\)\s+to\s+(\S+)\s+and\s+(user_\d+)`)

// buildResourceLinks unifies the ways a resource gets tied to a user: evidence records that
// carry one of the user's identifiers, and explicit data-catalog LINK_RESOURCE_IDENTITY events.
// Name-only matches (confidence 0.5) are kept but flagged by their low confidence.
func buildResourceLinks(db *sql.DB) error {
	if _, err := db.Exec(`
INSERT OR IGNORE INTO resource_links (resource_id, user_id, source, confidence)
SELECT r.resource_id, ri.user_id, 'evidence', MAX(ri.confidence)
FROM record_identifiers ri JOIN evidence_records r ON r.record_id = ri.record_id
WHERE r.resource_id IS NOT NULL AND r.resource_id <> '' AND r.resource_id IN (SELECT resource_id FROM resources)
GROUP BY r.resource_id, ri.user_id`); err != nil {
		return err
	}
	rows, err := db.Query(`SELECT activity_id, details FROM system_activity WHERE details LIKE '%linked%' AND event_type = 'LINK_RESOURCE_IDENTITY'`)
	if err != nil {
		return err
	}
	type link struct{ act, res, user string }
	var links []link
	for rows.Next() {
		var id, det string
		if err := rows.Scan(&id, &det); err != nil {
			rows.Close()
			return err
		}
		if m := reLinkEvent.FindStringSubmatch(det); m != nil {
			links = append(links, link{id, m[1], strings.ToLower(m[3])})
		}
	}
	rows.Close()
	for _, l := range links {
		if _, err := db.Exec(`INSERT OR IGNORE INTO resource_links (resource_id, user_id, source, confidence, source_ref)
			SELECT ?, ?, 'activity_link', 1.0, ? WHERE EXISTS (SELECT 1 FROM resources WHERE resource_id = ?) AND EXISTS (SELECT 1 FROM users WHERE user_id = ?)`,
			l.res, l.user, l.act, l.res, l.user); err != nil {
			return err
		}
	}
	return nil
}
