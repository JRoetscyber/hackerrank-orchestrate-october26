// Command ingest rebuilds data/audit.db from dataset/: raw CSV tables, the identity graph,
// every evidence file (deterministic parsers, Gemini vision for images/PDFs) and the links
// between them.
//
//	go run ./cmd/ingest
//	go run ./cmd/ingest --no-vision
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
	flag.StringVar(&o.Dataset, "dataset", "", "dataset directory (default <repo>/dataset)")
	flag.StringVar(&o.DBPath, "db", "", "output database (default <repo>/data/audit.db, rebuilt every run)")
	flag.StringVar(&o.CachePath, "cache", "", "persistent cache (default <repo>/data/cache.db)")
	flag.StringVar(&o.EnvPath, "env", "", "path to .env (default <repo>/.env)")
	flag.StringVar(&o.RunID, "run", "", "run id recorded in the usage table")
	flag.BoolVar(&o.NoVision, "no-vision", false, "do not call Gemini for images/PDFs")
	flag.BoolVar(&o.Verbose, "v", false, "list every evidence file")
	flag.BoolVar(&o.AllowDegraded, "allow-degraded", false, "continue even if an LLM step failed (not for the final run)")
	flag.Parse()
	if err := o.Defaults(); err != nil {
		fail(err)
	}
	if err := pipeline.Ingest(context.Background(), o); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
