// Command pipeline runs the whole solution end to end under one run id:
// ingest (database + vision) -> sample audit + score -> full audit -> output.csv.
//
//	go run ./cmd/pipeline
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
	flag.StringVar(&o.RunID, "run", "", "run id recorded in the usage table")
	flag.BoolVar(&o.NoVision, "no-vision", false, "skip Gemini vision")
	flag.BoolVar(&o.NoIntent, "no-llm", false, "keyword heuristic for intent instead of Gemini")
	flag.BoolVar(&o.AllowDegraded, "allow-degraded", false, "continue even if an LLM step failed (not for the final run)")
	flag.Parse()
	if err := o.Defaults(); err != nil {
		fail(err)
	}
	ctx := context.Background()
	fmt.Println("== run", o.RunID)
	fmt.Println("== stage 1/3: ingest")
	if err := pipeline.Ingest(ctx, o); err != nil {
		fail(err)
	}
	fmt.Println("== stage 2/3: sample audit (for scoring)")
	so := o
	so.Sample, so.Out = true, ""
	if err := so.Defaults(); err != nil {
		fail(err)
	}
	if _, err := pipeline.Audit(ctx, so); err != nil {
		fail(err)
	}
	fmt.Println("== stage 3/3: full audit")
	sum, err := pipeline.Audit(ctx, o)
	if err != nil {
		fail(err)
	}
	fmt.Printf("%d cases -> %s  %v (not applicable: %d)\n", sum.Cases, o.Out, sum.Status, sum.NotApplicable)
	fmt.Println("next: go run ./cmd/eval   and   go run ./cmd/report -run", o.RunID)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
