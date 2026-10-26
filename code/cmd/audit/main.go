// Command audit classifies each request, audits the authorized ones and writes the predictions.
//
//	go run ./cmd/audit --sample      # 32 public cases -> data/sample_pred.csv
//	go run ./cmd/audit               # 518 evaluation cases -> output.csv (repo root)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"ghostaudit/internal/pipeline"
)

func main() {
	var o pipeline.Options
	flag.BoolVar(&o.Sample, "sample", false, "audit the public sample requests")
	flag.StringVar(&o.DBPath, "db", "", "database built by cmd/ingest")
	flag.StringVar(&o.CachePath, "cache", "", "persistent cache")
	flag.StringVar(&o.EnvPath, "env", "", "path to .env")
	flag.StringVar(&o.Out, "out", "", "output CSV (default output.csv, or data/sample_pred.csv with --sample)")
	flag.StringVar(&o.RunID, "run", "", "run id recorded in the usage table")
	flag.BoolVar(&o.NoIntent, "no-llm", false, "use the keyword heuristic for intent instead of Gemini")
	flag.BoolVar(&o.AllowDegraded, "allow-degraded", false, "continue even if an LLM step failed (not for the final run)")
	flag.Parse()
	if err := o.Defaults(); err != nil {
		fail(err)
	}
	sum, err := pipeline.Audit(context.Background(), o)
	if err != nil {
		fail(err)
	}
	fmt.Printf("%d cases -> %s  %v (not applicable: %d)\n", sum.Cases, o.Out, sum.Status, sum.NotApplicable)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
