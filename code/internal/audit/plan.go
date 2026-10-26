package audit

import (
	"sort"
	"strings"
)

// Operation order inside each phase. Findings/remediation follow the task's dependency rule
// (stop flows and block restores before changing the records they feed); verification order
// was derived from the public sample answers.
var (
	findingOrder = []string{"REPORT_REMAINING_RECORD", "REPORT_EXPOSED_PII", "REPORT_ACTIVE_DATA_FLOW", "REPORT_RESTORE_RISK"}
	remedyOrder  = []string{"DISABLE_DATA_FLOW", "BLOCK_RESTORE", "DELETE_RECORD", "REDACT_RECORD", "ANONYMIZE_RECORD"}
	verifyOrder  = []string{"VERIFY_RECORD_ABSENT", "VERIFY_RECORD_PRESENT", "VERIFY_RECORD_REDACTED", "VERIFY_RECORD_ANONYMIZED", "VERIFY_DATA_FLOW_DISABLED", "VERIFY_RESTORE_BLOCKED"}
)

// item is a resource with its derived state and required treatment.
type item struct {
	Resource, Prefix, System, Category, Action string
	State                                      string // PRESENT ABSENT EXPOSED REDACTED ANONYMIZED ACTIVE DISABLED RESTORABLE BLOCKED
	EvidenceFile                               string
	OmissionFile                               string // for an unobserved resource: the case's own primary evidence file
	Why                                        string
}

// categoryOf picks the data category for a resource. A prefix can map to several categories
// (a support ticket is a message or an attachment; an analytics-style id may be a marketing
// profile), so the choice uses, in order: the record_type the evidence itself states, structural
// hints in the record, and the kind of action the deletion worker attempted on the resource.
func (e *Engine) categoryOf(prefix, resource string, os []obs) string {
	cats := e.cfg.Prefixes[prefix].Categories
	if len(cats) == 0 {
		return ""
	}
	if len(cats) == 1 {
		return cats[0]
	}
	allowed := map[string]bool{}
	for _, c := range cats {
		allowed[c] = true
	}
	for _, o := range os {
		if rt, ok := o.Raw["record_type"].(string); ok && allowed[strings.ToUpper(rt)] {
			return strings.ToUpper(rt)
		}
	}
	for _, o := range os {
		for k := range o.Raw {
			if c, ok := e.cfg.ShapeHints[strings.ToLower(k)]; ok && allowed[c] {
				return c
			}
		}
	}
	rows, err := e.db.Query(`SELECT DISTINCT event_type FROM system_activity WHERE resource_id = ? AND source_system = ?`, resource, e.cfg.ActivitySrc)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var et string
			if rows.Scan(&et) != nil {
				continue
			}
			for _, c := range cats {
				if a := e.cfg.EventAction[et]; a != "" && e.actions[c] == a {
					return c
				}
			}
		}
	}
	return cats[0]
}

// classify derives the state of a resource at audit time from its latest eligible evidence file.
func (e *Engine) classify(cat string, all []obs) (state, file string) {
	file, in := latest(all)
	var f Facts
	for _, o := range in {
		f.Exists = merge(f.Exists, o.F.Exists)
		f.Ident = merge(f.Ident, o.F.Ident)
		f.Ref = merge(f.Ref, o.F.Ref)
		f.Enabled = merge(f.Enabled, o.F.Enabled)
		f.Restore = merge(f.Restore, o.F.Restore)
	}
	// A bare structured row (no lookup verdict) exists simply by being in the snapshot.
	rowOnly := f.Exists == 0 && len(in) > 0
	switch cat {
	case "DATA_FLOW":
		if f.Enabled > 0 {
			return "ACTIVE", file
		}
		return "DISABLED", file
	case "BACKUP_ARCHIVE":
		if f.Restore > 0 {
			return "RESTORABLE", file
		}
		return "BLOCKED", file
	}
	switch e.actions[cat] {
	case "DELETE":
		if f.Exists > 0 || rowOnly {
			return "PRESENT", file
		}
		return "ABSENT", file
	case "REDACT":
		if f.Ident > 0 {
			return "EXPOSED", file
		}
		return "REDACTED", file
	case "ANONYMIZE":
		if f.Ident > 0 || f.Ref > 0 {
			return "EXPOSED", file
		}
		return "ANONYMIZED", file
	case "RETAIN":
		if f.Exists < 0 {
			return "ABSENT", file
		}
		return "PRESENT", file
	}
	return "PRESENT", file
}

// Run audits one case.
func (e *Engine) Run(c Case) (Result, error) {
	res := Result{CaseID: c.ID}
	users, err := e.subjects(c.Email)
	if err != nil {
		return res, err
	}
	cands, notes, err := e.candidates(c, users)
	if err != nil {
		return res, err
	}
	res.Notes = append(res.Notes, notes...)

	var items []item
	for _, cd := range cands {
		all, err := e.observations(cd.Resource, c.AuditAt)
		if err != nil {
			return res, err
		}
		cat := e.categoryOf(cd.Prefix, cd.Resource, all)
		it := item{Resource: cd.Resource, Prefix: cd.Prefix, Category: cat, Action: e.actions[cat], Why: cd.Why, System: e.system[cd.Prefix]}
		if len(all) == 0 {
			// Never observed before audit time: a deletable record that was never seen is absent;
			// a retained record that was never seen is assumed still retained (no citation possible).
			switch {
			case cat == "DATA_FLOW" || cat == "BACKUP_ARCHIVE":
				continue
			case it.Action == "DELETE":
				it.State = "ABSENT"
			case it.Action == "RETAIN":
				it.State = "PRESENT"
			default:
				continue // nothing is known about an unobserved record that would need redaction/anonymization
			}
		} else {
			it.State, it.EvidenceFile = e.classify(cat, all)
		}
		items = append(items, it)
	}

	e.pickEvidence(c, items)
	res.Steps, res.Status = e.assemble(c, items)
	if len(items) == 0 {
		// COMPLETE requires every relevant copy to be proven compliant. When nothing belonging to the
		// subject can be identified from evidence available at audit time, completion is unproven.
		res.Status = "INCOMPLETE"
		res.Notes = append(res.Notes, "no resources identified at audit time; completion cannot be proven")
	}
	return res, nil
}

type planned struct {
	op, state string
	it        item
}

// assemble converts items into ordered findings, remediation and verification steps.
func (e *Engine) assemble(c Case, items []item) ([]Step, string) {
	var finds, fixes, checks []planned
	violations := 0
	for _, it := range items {
		switch it.Category {
		case "DATA_FLOW":
			if it.State == "ACTIVE" {
				violations++
				finds = append(finds, planned{"REPORT_ACTIVE_DATA_FLOW", "ACTIVE", it})
				fixes = append(fixes, planned{"DISABLE_DATA_FLOW", "DISABLED", it})
			}
			checks = append(checks, planned{"VERIFY_DATA_FLOW_DISABLED", "DISABLED", it})
			continue
		case "BACKUP_ARCHIVE":
			if it.State == "RESTORABLE" {
				violations++
				finds = append(finds, planned{"REPORT_RESTORE_RISK", "ACTIVE", it})
				fixes = append(fixes, planned{"BLOCK_RESTORE", "BLOCKED", it})
			}
			checks = append(checks, planned{"VERIFY_RESTORE_BLOCKED", "BLOCKED", it})
			continue
		}
		switch it.Action {
		case "DELETE":
			if it.State == "PRESENT" {
				violations++
				finds = append(finds, planned{"REPORT_REMAINING_RECORD", "PRESENT", it})
				fixes = append(fixes, planned{"DELETE_RECORD", "ABSENT", it})
			}
			checks = append(checks, planned{"VERIFY_RECORD_ABSENT", "ABSENT", it})
		case "REDACT":
			if it.State == "EXPOSED" {
				violations++
				finds = append(finds, planned{"REPORT_EXPOSED_PII", "PRESENT", it})
				fixes = append(fixes, planned{"REDACT_RECORD", "REDACTED", it})
			}
			checks = append(checks, planned{"VERIFY_RECORD_REDACTED", "REDACTED", it})
		case "ANONYMIZE":
			if it.State == "EXPOSED" {
				violations++
				finds = append(finds, planned{"REPORT_EXPOSED_PII", "PRESENT", it})
				fixes = append(fixes, planned{"ANONYMIZE_RECORD", "ANONYMIZED", it})
			}
			checks = append(checks, planned{"VERIFY_RECORD_ANONYMIZED", "ANONYMIZED", it})
		case "RETAIN":
			checks = append(checks, planned{"VERIFY_RECORD_PRESENT", "PRESENT", it})
		}
	}
	status := "COMPLETE"
	if violations > 0 {
		status = "INCOMPLETE"
	}

	sortPlanned := func(list []planned, order []string) {
		rank := map[string]int{}
		for i, o := range order {
			rank[o] = i
		}
		sort.SliceStable(list, func(i, j int) bool {
			a, b := list[i], list[j]
			if rank[a.op] != rank[b.op] {
				return rank[a.op] < rank[b.op]
			}
			if a.it.System != b.it.System {
				return a.it.System < b.it.System
			}
			return a.it.Resource < b.it.Resource
		})
	}
	sortPlanned(finds, findingOrder)
	sortPlanned(fixes, remedyOrder)
	sortPlanned(checks, verifyOrder)

	var steps []Step
	add := func(phase string, list []planned, cite bool) {
		for _, x := range list {
			ev := ""
			if cite {
				ev = e.cite(c, x.it)
			}
			steps = append(steps, Step{StepNo: len(steps) + 1, Phase: phase, SystemID: x.it.System, ResourceID: x.it.Resource, Operation: x.op, State: x.state, EvidenceID: ev})
		}
	}
	if status == "INCOMPLETE" {
		add("FINDING", finds, true)
		add("REMEDIATION", fixes, false)
		add("VERIFICATION", checks, false)
	} else {
		add("VERIFICATION", checks, true)
	}
	return steps, status
}

// cite picks the evidence that proves a step: the latest file captured at or before audit time
// that observed the resource. For a record proven absent by omission, the latest eligible
// snapshot of the same kind of resource is cited instead.
func (e *Engine) cite(c Case, it item) string {
	if it.EvidenceFile != "" {
		return it.EvidenceFile
	}
	if it.OmissionFile != "" {
		return it.OmissionFile
	}
	var f string
	_ = e.db.QueryRow(`
SELECT d.file_id FROM evidence_records r JOIN evidence_documents d ON d.doc_id = r.doc_id
WHERE d.status='ok' AND d.file_id IS NOT NULL AND d.captured_at <= ?
  AND rtrim(rtrim(r.resource_id,'0123456789'),'_') = ?
  AND (r.system_id = ? OR r.system_id IS NULL)
ORDER BY d.captured_at DESC LIMIT 1`, c.AuditAt, it.Prefix, it.System).Scan(&f)
	return f
}

// pickEvidence handles records proven absent by omission (never seen in any eligible evidence):
// they are cited against the most recent structured (non-vision) file that observed the case's
// other resources, since that file is the case's own point-in-time view of the platform.
func (e *Engine) pickEvidence(c Case, items []item) {
	best := ""
	for _, it := range items {
		if it.EvidenceFile == "" {
			continue
		}
		var cap, ext string
		if err := e.db.QueryRow(`SELECT captured_at, extractor FROM evidence_documents WHERE file_id = ?`, it.EvidenceFile).Scan(&cap, &ext); err != nil || ext == "gemini_vision" {
			continue
		}
		if best == "" || it.EvidenceFile != best {
			var bc string
			if best != "" {
				_ = e.db.QueryRow(`SELECT captured_at FROM evidence_documents WHERE file_id = ?`, best).Scan(&bc)
			}
			if best == "" || cap > bc {
				best = it.EvidenceFile
			}
		}
	}
	if best == "" {
		return
	}
	for i := range items {
		if items[i].EvidenceFile == "" {
			items[i].OmissionFile = best
		}
	}
}
