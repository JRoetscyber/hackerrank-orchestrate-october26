// Package score compares a predictions CSV with an answer-key CSV and computes the metrics the
// evaluation report needs: case accuracy, step-level precision/recall, field accuracy and
// sequence accuracy.
package score

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Step is one steps_json entry.
type Step struct {
	StepNo     int    `json:"step_no"`
	Phase      string `json:"phase"`
	SystemID   string `json:"system_id"`
	ResourceID string `json:"resource_id"`
	Operation  string `json:"operation"`
	State      string `json:"state"`
	EvidenceID string `json:"evidence_id"`
}

// Key identifies a step ignoring its state, evidence and position.
func (s Step) Key() string {
	return strings.Join([]string{s.Phase, s.SystemID, s.ResourceID, s.Operation}, "|")
}

// Full identifies a step on every field.
func (s Step) Full() string {
	return fmt.Sprintf("%s|%s|%s|%d", s.Key(), s.State, s.EvidenceID, s.StepNo)
}

// Row is one CSV answer.
type Row struct {
	Status string
	Steps  []Step
}

// Load reads a case_id,deletion_status,steps_json CSV.
func Load(path string) (map[string]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	out := map[string]Row{}
	for _, rec := range recs[1:] {
		var st []Step
		if err := json.Unmarshal([]byte(rec[2]), &st); err != nil {
			return nil, fmt.Errorf("%s: bad steps_json: %w", rec[0], err)
		}
		out[rec[0]] = Row{rec[1], st}
	}
	return out, nil
}

// Metrics are the aggregate scores.
type Metrics struct {
	Cases, StatusOK       int
	TP, FP, FN            int
	FieldHit, FieldTotal  int
	StepExact, GoldSteps  int
	SeqIdentical          int
	LCSSum                float64
	Precision, Recall, F1 float64
	PerCase               []CaseResult
	StatusConfusion       map[string]int // "gold>pred" -> n
}

// CaseResult is the per-case outcome and any differences.
type CaseResult struct {
	ID          string
	GoldStatus  string
	PredStatus  string
	Identical   bool
	Differences []string
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// StatusAccuracy, FieldAccuracy etc. return percentages.
func (m Metrics) StatusAccuracy() float64 { return pct(m.StatusOK, m.Cases) }
func (m Metrics) FieldAccuracy() float64  { return pct(m.FieldHit, m.FieldTotal) }
func (m Metrics) StepExactRate() float64  { return pct(m.StepExact, m.GoldSteps) }
func (m Metrics) SeqExactRate() float64   { return pct(m.SeqIdentical, m.Cases) }
func (m Metrics) SeqSimilarity() float64  { return 100 * m.LCSSum / float64(max(m.Cases, 1)) }

// Compare scores pred against gold over the gold case ids.
func Compare(pred, gold map[string]Row) Metrics {
	m := Metrics{StatusConfusion: map[string]int{}}
	ids := make([]string, 0, len(gold))
	for id := range gold {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		m.Cases++
		g, p := gold[id], pred[id]
		cr := CaseResult{ID: id, GoldStatus: g.Status, PredStatus: p.Status}
		if g.Status == p.Status {
			m.StatusOK++
		} else {
			m.StatusConfusion[g.Status+">"+p.Status]++
		}
		gk, pk := map[string]Step{}, map[string]Step{}
		for _, s := range g.Steps {
			gk[s.Key()] = s
		}
		for _, s := range p.Steps {
			pk[s.Key()] = s
		}
		keys := make([]string, 0, len(gk))
		for k := range gk {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			gs := gk[k]
			m.GoldSteps++
			ps, ok := pk[k]
			if !ok {
				m.FN++
				cr.Differences = append(cr.Differences, fmt.Sprintf("MISSING  #%d %s ev=%q", gs.StepNo, k, gs.EvidenceID))
				continue
			}
			m.TP++
			m.FieldTotal += 3
			if ps.State == gs.State {
				m.FieldHit++
			}
			if ps.EvidenceID == gs.EvidenceID {
				m.FieldHit++
			}
			if ps.StepNo == gs.StepNo {
				m.FieldHit++
			}
			if ps.Full() == gs.Full() {
				m.StepExact++
			} else {
				cr.Differences = append(cr.Differences, fmt.Sprintf("DIFF     %s gold(no=%d ev=%q) pred(no=%d ev=%q)", k, gs.StepNo, gs.EvidenceID, ps.StepNo, ps.EvidenceID))
			}
		}
		pkeys := make([]string, 0, len(pk))
		for k := range pk {
			pkeys = append(pkeys, k)
		}
		sort.Strings(pkeys)
		for _, k := range pkeys {
			if _, ok := gk[k]; !ok {
				m.FP++
				cr.Differences = append(cr.Differences, fmt.Sprintf("EXTRA    #%d %s ev=%q", pk[k].StepNo, k, pk[k].EvidenceID))
			}
		}
		same := len(p.Steps) == len(g.Steps) && g.Status == p.Status
		if same {
			for i := range g.Steps {
				if p.Steps[i].Full() != g.Steps[i].Full() {
					same = false
					break
				}
			}
		}
		cr.Identical = same
		if same {
			m.SeqIdentical++
		}
		m.LCSSum += lcsRatio(g.Steps, p.Steps)
		m.PerCase = append(m.PerCase, cr)
	}
	m.Precision, m.Recall = pct(m.TP, m.TP+m.FP), pct(m.TP, m.TP+m.FN)
	if m.Precision+m.Recall > 0 {
		m.F1 = 2 * m.Precision * m.Recall / (m.Precision + m.Recall)
	}
	return m
}

// lcsRatio is 2*LCS/(len(a)+len(b)) over ordered step identities.
func lcsRatio(a, b []Step) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1].Key() == b[j-1].Key() {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max(dp[i-1][j], dp[i][j-1])
			}
		}
	}
	return 2 * float64(dp[len(a)][len(b)]) / float64(len(a)+len(b))
}
