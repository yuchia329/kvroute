// Command rescore re-judges recorded cells from their own recorded rows.
//
//	rescore -out docs/measurements/2026-09-13-warmup-drift/rescore.md \
//	        docs/measurements/2026-09-08-three-policy-multiturn/goodput \
//	        docs/measurements/2026-09-12-recency-rerun/think30
//
// The per-request rows are the system of record and a cell's summary is
// arithmetic over them, so a change to that arithmetic can be held against every
// cell already measured rather than only against the next run. #33 changes how a
// cell is judged to have been still warming up; this says which recorded
// verdicts the new rule moves, and in which direction.
//
// It only reads. No fleet, no GPU, and nothing written back into the cell
// records: a recorded cell says what was concluded when it ran, and a re-score
// that edited it in place would erase what it is reporting on.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yuchia329/kvroute/internal/bench"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "", "where to write the report; empty prints it and writes nothing")
	everyCell := flag.Bool("cells", false,
		"list every cell rather than only those whose verdict changed")
	threshold := flag.Float64("warmup-drift-threshold", bench.DefaultWarmupDriftThreshold,
		"the drift threshold to re-judge against. It is a judgement, so a re-score that hard-coded it could not show what a different one would have decided")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: rescore [-out path] [-cells] [-warmup-drift-threshold f] <sweep dir>...\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Each directory is a sweep's -dir, holding cells/ and the rows those cells produced,\n")
		fmt.Fprintf(flag.CommandLine.Output(), "as either cells/<id>.jsonl or a compacted requests.parquet.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	dirs := flag.Args()
	if len(dirs) == 0 {
		flag.Usage()
		return fmt.Errorf("rescore: name the sweep directories to re-score")
	}

	scored, err := bench.Rescore(dirs, *threshold)
	if err != nil {
		return err
	}
	rendered := bench.RescoreReport(scored, *threshold, *everyCell)
	fmt.Print("\n" + rendered)

	if *out == "" {
		return nil
	}
	if dir := filepath.Dir(*out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("rescore: %w", err)
		}
	}
	if err := os.WriteFile(*out, []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("rescore: %w", err)
	}
	fmt.Fprintf(os.Stderr, "rescore: wrote %s\n", *out)
	return nil
}
