package audit_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"ghostaudit/internal/audit"
	"ghostaudit/internal/intent"
	"ghostaudit/internal/paths"
	"ghostaudit/internal/validate"

	_ "modernc.org/sqlite"
)

// These are the custom evaluation cases described in code/evaluation/evaluation_report.md.
// They run against data/audit.db (built by `go run ./cmd/ingest`) and are skipped if it is absent.

func openEngine(t *testing.T) (*audit.Engine, *sql.DB) {
	t.Helper()
	p := paths.Join("data", "audit.db")
	if _, err := os.Stat(p); err != nil {
		t.Skip("data/audit.db not built; run `go run ./cmd/ingest` first")
	}
	db, err := sql.Open("sqlite", "file:"+p+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	e, err := audit.New(db)
	if err != nil {
		t.Fatal(err)
	}
	return e, db
}

func sampleCases(t *testing.T, e *audit.Engine) []audit.Case {
	t.Helper()
	cs, err := e.Cases("sample_deletion_requests")
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// Custom case 1 - TIME TRAVEL. Move every sample's audit_at far into the past. Nothing captured
// after the new cutoff may be cited, and no answer may rely on later evidence.
func TestCustom1_TimeTravelNeverCitesFutureEvidence(t *testing.T) {
	e, db := openEngine(t)
	captured := map[string]string{}
	rows, _ := db.Query(`SELECT file_id, captured_at FROM evidence_files`)
	for rows.Next() {
		var id, at string
		rows.Scan(&id, &at)
		captured[id] = at
	}
	rows.Close()
	const cutoff = "2026-07-01T00:00:00Z"
	cited := 0
	for _, c := range sampleCases(t, e) {
		c.AuditAt = cutoff
		r, err := e.Run(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range r.Steps {
			if s.EvidenceID == "" {
				continue
			}
			cited++
			if captured[s.EvidenceID] > cutoff {
				t.Errorf("%s step %d cites %s captured %s, after audit_at %s", c.ID, s.StepNo, s.EvidenceID, captured[s.EvidenceID], cutoff)
			}
		}
	}
	t.Logf("checked %d citations at cutoff %s", cited, cutoff)
}

// Custom case 2 - HISTORICAL IDENTIFIER. A request made with an OLD email must resolve to the same
// user, and produce the identical audit, as a request made with the CURRENT email.
func TestCustom2_HistoricalEmailEqualsCurrentEmail(t *testing.T) {
	e, db := openEngine(t)
	rows, err := db.Query(`
SELECT r.case_id, cur.value
FROM request_subjects rs
JOIN deletion_requests r ON r.case_id = rs.case_id
JOIN identifiers cur ON cur.user_id = rs.user_id AND cur.kind = 'email' AND cur.valid_to IS NULL
WHERE rs.email_form = 'historical' ORDER BY r.case_id LIMIT 5`)
	if err != nil {
		t.Fatal(err)
	}
	type pair struct{ id, cur string }
	var ps []pair
	for rows.Next() {
		var p pair
		rows.Scan(&p.id, &p.cur)
		ps = append(ps, p)
	}
	rows.Close()
	if len(ps) == 0 {
		t.Skip("no historical-email requests in this dataset")
	}
	cases, _ := e.Cases("deletion_requests")
	byID := map[string]audit.Case{}
	for _, c := range cases {
		byID[c.ID] = c
	}
	for _, p := range ps {
		hist := byID[p.id]
		cur := hist
		cur.Email = p.cur
		a, err1 := e.Run(hist)
		b, err2 := e.Run(cur)
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		if a.Status != b.Status || !reflect.DeepEqual(a.Steps, b.Steps) {
			t.Errorf("%s: old email %q and current email %q gave different audits", p.id, hist.Email, p.cur)
		}
	}
	t.Logf("%d historical-email requests resolve identically to the current email", len(ps))
}

// Custom case 3 - DETERMINISM. Same input, same bytes out, every time.
func TestCustom3_Deterministic(t *testing.T) {
	e, _ := openEngine(t)
	for _, c := range sampleCases(t, e) {
		a, _ := e.Run(c)
		b, _ := e.Run(c)
		ja, _ := json.Marshal(a.Steps)
		jb, _ := json.Marshal(b.Steps)
		if a.Status != b.Status || string(ja) != string(jb) {
			t.Fatalf("%s is not deterministic", c.ID)
		}
	}
}

// Custom case 4 - CONTRACT. The sample predictions must satisfy every submission rule.
func TestCustom4_SamplePredictionsSatisfyContract(t *testing.T) {
	_, db := openEngine(t)
	p := paths.Join("data", "sample_pred.csv")
	if _, err := os.Stat(p); err != nil {
		t.Skip("data/sample_pred.csv not built; run `go run ./cmd/audit --sample`")
	}
	problems, _, err := validate.Check(p, db, "sample_deletion_requests")
	if err != nil {
		t.Fatal(err)
	}
	for _, pr := range problems {
		t.Error(pr)
	}
}

// Custom case 5 - NON-DELETION INTENT, offline. The keyword fallback must never treat an explicit
// "keep my account" request as a deletion, and must catch deletion wording in other languages.
func TestCustom5_IntentHeuristic(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"Please stop the promotional emails. I still need the account, so I am not asking for deletion.", false},
		{"Can you correct my surname without removing any of my records?", false},
		{"I need a copy of last year's invoices. Please leave my profile and purchase history in place.", false},
		{"Please remove me from marketing lists only. I still want receipts.", false},
		{"Solicito la eliminación permanente de la cuenta asociada a este correo.", true},
		{"Je demande la suppression définitive du compte associé à cette adresse e-mail.", true},
		{"Solicito a exclusão permanente da conta associada a este e-mail.", true},
		{"このメールアドレスに関連するアカウントの完全な削除を依頼します。", true},
		{"Please close my account and process the associated personal-data deletion.", true},
	}
	for _, c := range cases {
		if got := intent.Heuristic(c.text).AuthorizesDeletion; got != c.want {
			t.Errorf("Heuristic(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
