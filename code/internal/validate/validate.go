// Package validate checks a predictions file against the submission contract in AGENTS.md /
// problem_statement.md: header, row count and order, status values, JSON shape, consecutive step
// numbers, phase and operation order, allowed operation/state pairs, evidence rules and the
// "no evidence captured after audit_at" rule.
package validate

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

type step struct {
	StepNo     int    `json:"step_no"`
	Phase      string `json:"phase"`
	SystemID   string `json:"system_id"`
	ResourceID string `json:"resource_id"`
	Operation  string `json:"operation"`
	State      string `json:"state"`
	EvidenceID string `json:"evidence_id"`
}

// allowed operation -> (phase, state) exactly as in the task contract.
var allowed = map[string][2]string{
	"REPORT_REMAINING_RECORD":   {"FINDING", "PRESENT"},
	"REPORT_EXPOSED_PII":        {"FINDING", "PRESENT"},
	"REPORT_ACTIVE_DATA_FLOW":   {"FINDING", "ACTIVE"},
	"REPORT_RESTORE_RISK":       {"FINDING", "ACTIVE"},
	"DISABLE_DATA_FLOW":         {"REMEDIATION", "DISABLED"},
	"BLOCK_RESTORE":             {"REMEDIATION", "BLOCKED"},
	"DELETE_RECORD":             {"REMEDIATION", "ABSENT"},
	"REDACT_RECORD":             {"REMEDIATION", "REDACTED"},
	"ANONYMIZE_RECORD":          {"REMEDIATION", "ANONYMIZED"},
	"VERIFY_RECORD_ABSENT":      {"VERIFICATION", "ABSENT"},
	"VERIFY_RECORD_PRESENT":     {"VERIFICATION", "PRESENT"},
	"VERIFY_RECORD_REDACTED":    {"VERIFICATION", "REDACTED"},
	"VERIFY_RECORD_ANONYMIZED":  {"VERIFICATION", "ANONYMIZED"},
	"VERIFY_DATA_FLOW_DISABLED": {"VERIFICATION", "DISABLED"},
	"VERIFY_RESTORE_BLOCKED":    {"VERIFICATION", "BLOCKED"},
}

var phaseRank = map[string]int{"FINDING": 0, "REMEDIATION": 1, "VERIFICATION": 2}

// Check validates the CSV at path against the database (which supplies the expected case ids,
// audit_at cutoffs and the evidence manifest). It returns every problem found; warnings are
// legitimate-but-notable outcomes that do not break the contract.
func Check(path string, db *sql.DB, requestsTable string) (problems, warnings []string, err error) {
	warn := func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) }
	bad := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(recs) == 0 || strings.Join(recs[0], ",") != "case_id,deletion_status,steps_json" {
		bad("header must be exactly case_id,deletion_status,steps_json (got %v)", recs[0])
	}

	auditAt := map[string]string{}
	rows, err := db.Query(fmt.Sprintf(`SELECT case_id, audit_at FROM "%s"`, requestsTable))
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return nil, nil, err
		}
		auditAt[id] = at
	}
	rows.Close()
	captured := map[string]string{}
	frows, err := db.Query(`SELECT file_id, captured_at FROM evidence_files`)
	if err != nil {
		return nil, nil, err
	}
	for frows.Next() {
		var id, at string
		if err := frows.Scan(&id, &at); err != nil {
			frows.Close()
			return nil, nil, err
		}
		captured[id] = at
	}
	frows.Close()

	data := recs[1:]
	if len(data) != len(auditAt) {
		bad("expected %d data rows, found %d", len(auditAt), len(data))
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(data))
	for _, rec := range data {
		if len(rec) != 3 {
			bad("row %v: expected 3 columns", rec)
			continue
		}
		id, status, js := rec[0], rec[1], rec[2]
		ids = append(ids, id)
		if seen[id] {
			bad("%s: duplicate case_id", id)
		}
		seen[id] = true
		if _, ok := auditAt[id]; !ok {
			bad("%s: not a request in %s", id, requestsTable)
		}
		if status != "COMPLETE" && status != "INCOMPLETE" && status != "NOT_APPLICABLE" {
			bad("%s: bad deletion_status %q", id, status)
		}
		var steps []step
		if err := json.Unmarshal([]byte(js), &steps); err != nil {
			bad("%s: steps_json is not a JSON array of steps: %v", id, err)
			continue
		}
		if status == "NOT_APPLICABLE" && len(steps) != 0 {
			bad("%s: NOT_APPLICABLE must have steps_json=[]", id)
		}
		if status == "COMPLETE" && len(steps) == 0 {
			bad("%s: COMPLETE with no steps", id)
		}
		if status == "INCOMPLETE" && len(steps) == 0 {
			warn("%s: INCOMPLETE with no steps (nothing identifiable at audit time; completion unproven)", id)
			continue
		}
		prevPhase, prevOp := -1, ""
		hasFinding := false
		for i, s := range steps {
			if s.StepNo != i+1 {
				bad("%s: step %d has step_no %d (must be consecutive from 1)", id, i+1, s.StepNo)
			}
			a, ok := allowed[s.Operation]
			if !ok {
				bad("%s step %d: operation %q is not allowed", id, s.StepNo, s.Operation)
				continue
			}
			if s.Phase != a[0] || s.State != a[1] {
				bad("%s step %d: %s must be phase %s state %s (got %s/%s)", id, s.StepNo, s.Operation, a[0], a[1], s.Phase, s.State)
			}
			if phaseRank[s.Phase] < prevPhase {
				bad("%s step %d: phase %s after a later phase", id, s.StepNo, s.Phase)
			}
			prevPhase, prevOp = phaseRank[s.Phase], s.Operation
			_ = prevOp
			if s.Phase == "FINDING" {
				hasFinding = true
			}
			future := s.Phase == "REMEDIATION" || (s.Phase == "VERIFICATION" && status == "INCOMPLETE")
			if future && s.EvidenceID != "" {
				bad("%s step %d: future work must use an empty evidence_id (got %q)", id, s.StepNo, s.EvidenceID)
			}
			if s.Phase == "FINDING" && s.EvidenceID == "" {
				bad("%s step %d: finding without evidence", id, s.StepNo)
			}
			if s.Phase == "VERIFICATION" && status == "COMPLETE" && s.EvidenceID == "" {
				bad("%s step %d: COMPLETE verification must cite evidence", id, s.StepNo)
			}
			if s.EvidenceID != "" {
				cap, ok := captured[s.EvidenceID]
				switch {
				case !ok:
					bad("%s step %d: unknown evidence id %q", id, s.StepNo, s.EvidenceID)
				case cap > auditAt[id]:
					bad("%s step %d: evidence %s captured %s is after audit_at %s", id, s.StepNo, s.EvidenceID, cap, auditAt[id])
				}
			}
		}
		if status == "INCOMPLETE" && len(steps) > 0 && !hasFinding {
			bad("%s: INCOMPLETE without any finding", id)
		}
		if status == "COMPLETE" && hasFinding {
			bad("%s: COMPLETE must contain verification steps only", id)
		}
	}
	if !sort.StringsAreSorted(ids) {
		bad("rows are not sorted by case_id")
	}
	return problems, warnings, nil
}
