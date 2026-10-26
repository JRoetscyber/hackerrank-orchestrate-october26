package audit

import (
	"fmt"
	"math"
	"sort"
	"time"
)

const tsLayout = "2006-01-02T15:04:05Z"

type attribRule struct {
	EventType        string    `json:"event_type"`
	WindowMin        []float64 `json:"window_min"`
	ResultConsistent bool      `json:"result_consistent"`
	Center           string    `json:"center"` // "zero" or "learned"
}

type workerEvent struct {
	Resource, Type, Result string
	At                     time.Time
}

// attribute ties resources that carry no identifier at all (an account that is already deleted,
// an anonymized analytics profile) to cases. The only grounded link is the deletion worker's
// activity: it acts on the resource shortly before the workflow reports back, and its reported
// result must agree with the case's reported status. Because each resource belongs to exactly
// one case and cases compete for the same pool, matching is global (cheapest pairs first)
// rather than per-case, which also keeps results independent of case order.
func (e *Engine) attribute() error {
	if e.attrib != nil {
		return nil
	}
	e.attrib = map[string][]string{}

	cases, err := e.allCases()
	if err != nil {
		return err
	}
	owned := map[string]map[string]bool{}
	byUser := map[string][]string{}
	rows, err := e.db.Query(`SELECT resource_id, user_id FROM resource_links WHERE confidence >= 0.9`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r, u string
		if err := rows.Scan(&r, &u); err != nil {
			rows.Close()
			return err
		}
		if owned[r] == nil {
			owned[r] = map[string]bool{}
		}
		owned[r][u] = true
		byUser[u] = append(byUser[u], r)
	}
	rows.Close()

	events := map[string][]workerEvent{} // by resource
	var all []workerEvent
	erows, err := e.db.Query(`SELECT resource_id, event_type, reported_result, event_time FROM system_activity WHERE source_system = ?`, e.cfg.ActivitySrc)
	if err != nil {
		return err
	}
	for erows.Next() {
		var w workerEvent
		var ts string
		if err := erows.Scan(&w.Resource, &w.Type, &w.Result, &ts); err != nil {
			erows.Close()
			return err
		}
		if w.At, err = time.Parse(tsLayout, ts); err != nil {
			continue
		}
		events[w.Resource] = append(events[w.Resource], w)
		all = append(all, w)
	}
	erows.Close()

	type subject struct {
		c        Case
		users    []string
		prefixes map[string]bool
		rep, req time.Time
	}
	var subs []subject
	for _, c := range cases {
		users, err := e.subjects(c.Email)
		if err != nil {
			return err
		}
		s := subject{c: c, users: users, prefixes: map[string]bool{}}
		for _, u := range users {
			for _, r := range byUser[u] {
				s.prefixes[prefixOf(r)] = true
			}
		}
		if s.rep, err = time.Parse(tsLayout, c.ReportedAt); err != nil {
			continue
		}
		s.req, _ = time.Parse(tsLayout, c.RequestedAt)
		subs = append(subs, s)
	}

	prefixes := make([]string, 0, len(e.cfg.Attribution))
	for p := range e.cfg.Attribution {
		if p != "_comment" {
			prefixes = append(prefixes, p)
		}
	}
	sort.Strings(prefixes)

	for _, p := range prefixes {
		rule := e.cfg.Attribution[p]
		center := 0.0
		if rule.Center == "learned" {
			var ds []float64
			for _, s := range subs {
				for _, u := range s.users {
					for _, r := range byUser[u] {
						if prefixOf(r) != p {
							continue
						}
						for _, w := range events[r] {
							if w.Type == rule.EventType && w.Result == "SUCCESS" {
								ds = append(ds, w.At.Sub(s.rep).Minutes())
							}
						}
					}
				}
			}
			if len(ds) >= 5 { // learned from resources already tied to users by identifiers
				sort.Float64s(ds)
				center = ds[len(ds)/2]
			}
		}
		type edge struct {
			cost     float64
			caseID   string
			resource string
		}
		var edges []edge
		for _, s := range subs {
			if s.prefixes[p] {
				continue // already has a resource of this kind from identifier links
			}
			want := e.cfg.ResultForStatus[s.c.ReportedStatus]
			for _, w := range all {
				if w.Type != rule.EventType || prefixOf(w.Resource) != p || len(owned[w.Resource]) > 0 {
					continue
				}
				d := w.At.Sub(s.rep).Minutes()
				if d < rule.WindowMin[0] || d > rule.WindowMin[1] || w.At.Before(s.req) {
					continue
				}
				cost := math.Abs(d - center)
				if rule.ResultConsistent && want != "" && w.Result != want {
					cost += 10000 // the worker's claim disagrees with the case's reported status: last resort only
				}
				edges = append(edges, edge{cost, s.c.ID, w.Resource})
			}
		}
		sort.Slice(edges, func(i, j int) bool {
			a, b := edges[i], edges[j]
			if a.cost != b.cost {
				return a.cost < b.cost
			}
			if a.caseID != b.caseID {
				return a.caseID < b.caseID
			}
			return a.resource < b.resource
		})
		doneCase, usedRes := map[string]bool{}, map[string]bool{}
		for _, ed := range edges {
			if doneCase[ed.caseID] || usedRes[ed.resource] {
				continue
			}
			doneCase[ed.caseID], usedRes[ed.resource] = true, true
			e.attrib[ed.caseID] = append(e.attrib[ed.caseID], ed.resource)
		}
	}
	return nil
}

// allCases returns every request in the database, from every requests table, so cases from the
// evaluation set and the public samples compete for the same activity pool.
func (e *Engine) allCases() ([]Case, error) {
	var out []Case
	for _, t := range []string{"deletion_requests", "sample_deletion_requests"} {
		var n int
		if err := e.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, t).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		cs, err := e.Cases(t)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t, err)
		}
		out = append(out, cs...)
	}
	return out, nil
}
