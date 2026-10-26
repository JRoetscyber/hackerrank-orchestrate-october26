package evidence

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"ghostaudit/internal/gemini"
	"ghostaudit/internal/store"
)

const fakeVision = `{
 "document_type":"admin_console","system_id":"account_service","captured_at":"2026-09-26T11:00:00Z",
 "visible_text":"Haruka Sato user_11 account_5762 haruka.s1838@zoho.com",
 "records":[{"resource_id":"account_5762","record_type":"customer","system_id":"account_service","observed_at":"",
   "fields":[{"key":"email","value":"HARUKA.S1838@zoho.com"},{"key":"customer_reference","value":"user_11"},{"key":"phone","value":"+81 90 7610 2507"}]}]}`

func TestVisionExtractCacheAndParse(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("x-goog-api-key") != "test-key" {
			t.Errorf("api key must be sent as a header")
		}
		if r.URL.RawQuery != "" {
			t.Errorf("api key must not be in the URL: %q", r.URL.RawQuery)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"candidates":    []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": fakeVision}}}}},
			"usageMetadata": map[string]any{"promptTokenCount": 1200, "candidatesTokenCount": 300, "thoughtsTokenCount": 50},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "a.db"), filepath.Join(dir, "c.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("GEMINI_BASE_URL", srv.URL)
	gc, err := gemini.FromEnv("GEMINI_VISION_MODEL")
	if err != nil {
		t.Fatal(err)
	}
	v := &Vision{Client: gc, DB: db, Systems: []string{"account_service", "backup_service"}, Concurrency: 2, RunID: "t1"}
	jobs := []VisionJob{{Path: "evidence/images/x.png", Data: []byte("png"), MIME: "image/png", SHA: "abc"}}

	r1 := v.Run(context.Background(), jobs)["evidence/images/x.png"]
	if r1.Err != nil || r1.CacheHit {
		t.Fatalf("first run: err=%v hit=%v", r1.Err, r1.CacheHit)
	}
	r2 := v.Run(context.Background(), jobs)["evidence/images/x.png"]
	if r2.Err != nil || !r2.CacheHit {
		t.Fatalf("second run should be a cache hit: err=%v hit=%v", r2.Err, r2.CacheHit)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 API call, got %d", calls.Load())
	}

	// a different prompt (new system list) must invalidate the cache
	v.Systems = append(v.Systems, "search_service")
	if r3 := v.Run(context.Background(), jobs)["evidence/images/x.png"]; r3.CacheHit {
		t.Fatalf("changed prompt must not reuse the cache")
	}

	// records: 1 entity + 1 visible_text transcription; email classified and normalised
	if len(r1.Ex.Records) != 2 || r1.Ex.Records[0].Resource != "account_5762" {
		t.Fatalf("unexpected records: %+v", r1.Ex.Records)
	}
	var email string
	for _, f := range Flatten(r1.Ex.Records[0].Raw) {
		if f.Key == "email" {
			email = f.Norm
		}
	}
	if email != "haruka.s1838@zoho.com" {
		t.Fatalf("email not normalised: %q", email)
	}

	var in, out int
	var cost float64
	if err := db.QueryRow(`SELECT SUM(input_tokens), SUM(output_tokens), SUM(cost_usd) FROM cache.llm_usage WHERE cache_hit=0`).Scan(&in, &out, &cost); err != nil {
		t.Fatal(err)
	}
	if in != 2400 || out != 700 || cost <= 0 { // two real calls (initial + invalidated)
		t.Fatalf("usage not recorded: in=%d out=%d cost=%f", in, out, cost)
	}
}

func TestClassifyAndEmbedded(t *testing.T) {
	cases := []struct{ key, val, kind, norm string }{
		{"email", "Foo.Bar@Gmail.com", "email", "foo.bar@gmail.com"},
		{"phone", "+34 612 847 593", "phone", "34612847593"},
		{"subject_selector", "user_55", "user_id", "user_55"},
		{"full_name", "  Mateo  Ríos ", "name", "mateo ríos"},
		{"resource_id", "account_5762", "resource", "account_5762"},
		{"event", "dependency_retry", "text", "dependency_retry"},
	}
	for _, c := range cases {
		if k, n := Classify(c.key, c.val); k != c.kind || n != c.norm {
			t.Errorf("Classify(%q,%q)=%s,%q want %s,%q", c.key, c.val, k, n, c.kind, c.norm)
		}
	}
	fs := Flatten(map[string]any{"msg": "sent to a.b@x.org re user_7"})
	var kinds []string
	for _, f := range fs {
		kinds = append(kinds, f.Kind)
	}
	if len(fs) != 3 { // text + embedded email + embedded user
		t.Fatalf("embedded identifiers not extracted: %v", kinds)
	}
}
