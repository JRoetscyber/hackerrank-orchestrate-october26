// Command validate checks output.csv against the submission contract.
//
//	go run ./cmd/validate                 # output.csv vs the 518 evaluation requests
//	go run ./cmd/validate --sample        # data/sample_pred.csv vs the 32 sample requests
package main

import (
	"flag"
	"fmt"
	"os"

	"ghostaudit/internal/paths"
	"ghostaudit/internal/pipeline"
	"ghostaudit/internal/validate"
)

func main() {
	sample := flag.Bool("sample", false, "validate the sample predictions")
	file := flag.String("file", "", "CSV to validate")
	flag.Parse()
	table, path := "deletion_requests", paths.Join("output.csv")
	if *sample {
		table, path = "sample_deletion_requests", paths.Join("data", "sample_pred.csv")
	}
	if *file != "" {
		path = *file
	}
	db, err := pipeline.OpenReadOnly(paths.Join("data", "audit.db"))
	if err != nil {
		fail(err)
	}
	defer db.Close()
	problems, warnings, err := validate.Check(path, db, table)
	if err != nil {
		fail(err)
	}
	for _, w := range warnings {
		fmt.Println("WARNING:", w)
	}
	if len(problems) == 0 {
		fmt.Printf("OK: %s satisfies the submission contract\n", path)
		return
	}
	for i, p := range problems {
		if i == 40 {
			fmt.Printf("... and %d more\n", len(problems)-40)
			break
		}
		fmt.Println("PROBLEM:", p)
	}
	os.Exit(1)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
