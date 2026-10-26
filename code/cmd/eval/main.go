// Command eval scores a predictions CSV against an answer-key CSV (the public sample answers).
//
//	go run ./cmd/eval          # data/sample_pred.csv vs dataset/sample_output.csv
//	go run ./cmd/eval -v       # also print every differing case
package main

import (
	"flag"
	"fmt"
	"os"

	"ghostaudit/internal/paths"
	"ghostaudit/internal/score"
)

func main() {
	pred := flag.String("pred", paths.Join("data", "sample_pred.csv"), "predictions CSV")
	gold := flag.String("gold", paths.Join("dataset", "sample_output.csv"), "answer key CSV")
	verbose := flag.Bool("v", false, "print every differing case")
	flag.Parse()

	p, err := score.Load(*pred)
	if err != nil {
		fail(err)
	}
	g, err := score.Load(*gold)
	if err != nil {
		fail(err)
	}
	m := score.Compare(p, g)
	if *verbose {
		for _, c := range m.PerCase {
			if c.Identical {
				continue
			}
			fmt.Printf("\n--- %s  status gold=%s pred=%s\n", c.ID, c.GoldStatus, c.PredStatus)
			for _, d := range c.Differences {
				fmt.Println(" ", d)
			}
		}
	}
	fmt.Printf("\ncases: %d\n", m.Cases)
	fmt.Printf("status accuracy        : %5.1f%%  (%d/%d)\n", m.StatusAccuracy(), m.StatusOK, m.Cases)
	fmt.Printf("step precision/recall  : %5.1f%% / %5.1f%%  F1 %.1f%%  (tp=%d fp=%d fn=%d)\n", m.Precision, m.Recall, m.F1, m.TP, m.FP, m.FN)
	fmt.Printf("field accuracy         : %5.1f%%  (state, evidence_id, step_no on matched steps)\n", m.FieldAccuracy())
	fmt.Printf("step exact (all fields): %5.1f%%  (%d/%d)\n", m.StepExactRate(), m.StepExact, m.GoldSteps)
	fmt.Printf("sequence exact         : %5.1f%%  (%d/%d cases identical)\n", m.SeqExactRate(), m.SeqIdentical, m.Cases)
	fmt.Printf("sequence similarity    : %5.1f%%  (mean LCS ratio)\n", m.SeqSimilarity())
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
