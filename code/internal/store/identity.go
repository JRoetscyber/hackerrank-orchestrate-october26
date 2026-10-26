package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var reSpaces = regexp.MustCompile(`\s+`)

func normValue(kind, v string) string {
	v = strings.TrimSpace(v)
	switch kind {
	case "phone":
		var b strings.Builder
		for _, r := range v {
			if r >= '0' && r <= '9' {
				b.WriteRune(r)
			}
		}
		return b.String()
	case "name":
		return reSpaces.ReplaceAllString(strings.ToLower(v), " ")
	default: // email, email_hash, user_id
		return strings.ToLower(v)
	}
}

type interval struct {
	value     string
	from, to  sql.NullString
	source    string
	sourceRef sql.NullString
}

// BuildIdentityGraph fills users + identifiers from the current snapshot and the audit
// history. For each (user, field) the audit log is replayed in time order into validity
// windows, so a historical email resolves to the right user and "was this identifier held
// at time T?" is answerable. Email hashes are also derived (sha256 of the lowercased
// email) so a hash found in evidence links even if the audit log never recorded it.
func BuildIdentityGraph(db *sql.DB) (users, idents int, err error) {
	snapCols, err := tableCols(db, "user_identities")
	if err != nil {
		return 0, 0, err
	}
	fieldKinds := map[string]string{"email": "email", "phone": "phone", "email_hash": "email_hash", "name": "name"}

	type snap struct {
		vals      map[string]string // kind -> value
		updatedAt string
	}
	snaps := map[string]*snap{}
	rows, err := db.Query(`SELECT * FROM user_identities`)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		vals := make([]sql.NullString, len(snapCols))
		ptrs := make([]any, len(snapCols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return 0, 0, err
		}
		s := &snap{vals: map[string]string{}}
		uid := ""
		for i, c := range snapCols {
			switch {
			case c == "user_id":
				uid = vals[i].String
			case c == "updated_at":
				s.updatedAt = vals[i].String
			case fieldKinds[c] != "" && vals[i].Valid && vals[i].String != "":
				s.vals[fieldKinds[c]] = vals[i].String
			}
		}
		if uid != "" {
			snaps[uid] = s
		}
	}
	rows.Close()

	type event struct{ id, time, field, old, new string }
	events := map[string][]event{}
	rows, err = db.Query(`SELECT audit_id, user_id, event_time, field_name, old_value, new_value FROM user_audit_log ORDER BY event_time, audit_id`)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var e event
		var uid string
		var o, n sql.NullString
		if err := rows.Scan(&e.id, &uid, &e.time, &e.field, &o, &n); err != nil {
			rows.Close()
			return 0, 0, err
		}
		e.old, e.new = o.String, n.String
		events[uid] = append(events[uid], e)
	}
	rows.Close()

	allUsers := map[string]bool{}
	for u := range snaps {
		allUsers[u] = true
	}
	for u := range events {
		allUsers[u] = true
	}
	ids := make([]string, 0, len(allUsers))
	for u := range allUsers {
		ids = append(ids, u)
	}
	sort.Strings(ids)

	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	insUser, _ := tx.Prepare(`INSERT INTO users (user_id, in_snapshot, first_seen) VALUES (?,?,?)`)
	insID, _ := tx.Prepare(`INSERT INTO identifiers (user_id, kind, value, norm_value, valid_from, valid_to, source, source_ref) VALUES (?,?,?,?,?,?,?,?)`)

	for _, uid := range ids {
		evs := events[uid]
		first := sql.NullString{}
		if len(evs) > 0 {
			first = sql.NullString{String: evs[0].time, Valid: true}
		}
		_, inSnap := snaps[uid]
		b := 0
		if inSnap {
			b = 1
		}
		if _, err := insUser.Exec(uid, b, nullable(first)); err != nil {
			return 0, 0, err
		}
		users++

		var ivs []struct {
			kind string
			iv   interval
		}
		add := func(kind string, iv interval) {
			ivs = append(ivs, struct {
				kind string
				iv   interval
			}{kind, iv})
		}
		add("user_id", interval{value: uid, source: "snapshot"})

		byField := map[string][]event{}
		for _, e := range evs {
			if fieldKinds[e.field] != "" {
				byField[fieldKinds[e.field]] = append(byField[fieldKinds[e.field]], e)
			}
		}
		for kind, fe := range byField {
			// value before the first change
			if fe[0].old != "" {
				add(kind, interval{value: fe[0].old, to: ns(fe[0].time), source: "audit_old", sourceRef: ns(fe[0].id)})
			}
			for i, e := range fe {
				iv := interval{value: e.new, from: ns(e.time), source: "audit_new", sourceRef: ns(e.id)}
				if i+1 < len(fe) {
					iv.to = ns(fe[i+1].time)
				}
				add(kind, iv)
			}
		}
		if s := snaps[uid]; s != nil {
			for kind, val := range s.vals {
				nv := normValue(kind, val)
				matched := false
				for k := range ivs {
					if ivs[k].kind == kind && normValue(kind, ivs[k].iv.value) == nv && !ivs[k].iv.to.Valid {
						matched = true
					}
				}
				if matched {
					continue
				}
				// Snapshot disagrees with / extends the audit trail: close any open window and add it.
				for k := range ivs {
					if ivs[k].kind == kind && !ivs[k].iv.to.Valid && ivs[k].iv.source != "snapshot" {
						ivs[k].iv.to = ns(s.updatedAt)
					}
				}
				from := sql.NullString{}
				if len(byField[kind]) > 0 {
					from = ns(s.updatedAt)
				}
				add(kind, interval{value: val, from: from, source: "snapshot"})
			}
		}
		// derived hashes for every email window
		have := map[string]bool{}
		for _, x := range ivs {
			if x.kind == "email_hash" {
				have[normValue("email_hash", x.iv.value)] = true
			}
		}
		for _, x := range append([]struct {
			kind string
			iv   interval
		}(nil), ivs...) {
			if x.kind != "email" {
				continue
			}
			sum := sha256.Sum256([]byte(normValue("email", x.iv.value)))
			h := hex.EncodeToString(sum[:])
			if have[h] {
				continue
			}
			have[h] = true
			add("email_hash", interval{value: h, from: x.iv.from, to: x.iv.to, source: "derived_hash", sourceRef: x.iv.sourceRef})
		}
		for _, x := range ivs {
			if x.iv.value == "" {
				continue
			}
			if _, err := insID.Exec(uid, x.kind, x.iv.value, normValue(x.kind, x.iv.value),
				nullable(x.iv.from), nullable(x.iv.to), x.iv.source, nullable(x.iv.sourceRef)); err != nil {
				return 0, 0, fmt.Errorf("insert identifier for %s: %w", uid, err)
			}
			idents++
		}
	}
	return users, idents, tx.Commit()
}

func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nullable(n sql.NullString) any {
	if n.Valid {
		return n.String
	}
	return nil
}

func tableCols(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(fmt.Sprintf(`SELECT name FROM pragma_table_info('%s')`, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table %s not found", table)
	}
	return cols, nil
}
