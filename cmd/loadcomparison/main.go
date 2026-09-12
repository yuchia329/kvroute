// Command loadcomparison reports what the spill rule's load condition was
// actually comparing against over a run, and what each candidate grid point
// would have declined there.
//
//	loadcomparison -out runs/load-denominator/comparison.md runs/.../router.jsonl
//
// The condition declines the best prefix match when its replica is carrying more
// than a factor times the fleet's minimum inflight. That is a ratio, and a ratio
// is what lets a factor settled at one rung describe the rule at another. #31
// found that at 6 requests per second over five replicas it is not one: the
// fleet holds six requests at the median, the quietest replica holds nothing or
// one, and "twice the minimum" becomes "more than two requests". The rule declined
// 19–20% of later turns against 0.674% at the 32-user rung the factor was
// settled at, and 42% of the declined turns missed the SLO.
//
// Two things are read off this, and they are the two halves of choosing a
// threshold: how often the comparison was a ratio at all, and which points could
// fire at this rung — the second being what the grid is then cut from, rather
// than from a plausible-looking guess. See internal/bench/loaddenominatorgrid.go.
//
// It only reads. Every figure comes from rows a run already wrote, so the
// measurement can be rebuilt from a checkout with no fleet running.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/record"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "", "where to write the report; empty prints it and writes nothing")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: loadcomparison [-out path] <router rows>...\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Each path is a router's own per-request rows: .jsonl, .jsonl.gz or .parquet.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Name several to read one rung across the cells of a sweep.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	paths := flag.Args()
	if len(paths) == 0 {
		flag.Usage()
		return fmt.Errorf("loadcomparison: name the router rows to read")
	}

	var rows []record.Request
	for _, path := range paths {
		read, err := bench.LoadRouterRows(path)
		if err != nil {
			return err
		}
		rows = append(rows, read...)
	}
	if len(rows) == 0 {
		return fmt.Errorf("loadcomparison: %d files held no rows", len(paths))
	}

	// The package's own candidates rather than a flag, for the reason the sweeps
	// carry their own levels: the run that cuts the grid and the code that is
	// cutting it have to be projecting the same points, and a list retyped per
	// invocation is a second grid nothing compares against the first.
	report := bench.MeasureLoadComparison(rows, bench.LoadDenominatorCandidates()).Report()
	if *out == "" {
		fmt.Print(report)
		return nil
	}
	if err := os.WriteFile(*out, []byte(report), 0o644); err != nil {
		return fmt.Errorf("loadcomparison: write %s: %w", *out, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s over %d decisions\n", *out, len(rows))
	return nil
}
