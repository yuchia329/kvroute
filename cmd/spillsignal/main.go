// Command spillsignal reports what the spill rule's two branches were actually
// reading.
//
//	spillsignal -out runs/spill-signal.md runs/ws3-skew0/router.jsonl
//
// The rule declines a prefix match under two conditions, on the premise that
// they answer two different pressures: one that the replica is evicting, and one
// that it is buried under work. #16 built it that way and #18's headline figure
// rests on it, and it was false — the first condition read
// vllm:kv_cache_usage_perc, which counts the blocks held by a replica's running
// batch and therefore tracked the second condition's own signal at r = 0.973.
// Two of the three levels swept could not fire at the concurrency they ran at.
// See ADR-0011 and #28.
//
// So this exists to keep the premise checkable. Every router row carries the
// residency signal, the batch gauge and the router's own inflight, taken at the
// instant of one decision, and this reports each signal's observed range and how
// strongly it moves with inflight. Two things are read off it: whether the
// residency branch is a second branch at all, and which low-water levels the
// signal actually reached — which is what the grid is then cut from, rather than
// from a plausible-looking guess.
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
		fmt.Fprintf(flag.CommandLine.Output(), "usage: spillsignal [-out path] <router rows>...\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Each path is a router's own per-request rows: .jsonl, .jsonl.gz or .parquet.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Name several to read one signal across the cells of a sweep.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	paths := flag.Args()
	if len(paths) == 0 {
		flag.Usage()
		return fmt.Errorf("spillsignal: name the router rows to read")
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
		return fmt.Errorf("spillsignal: %d files held no rows", len(paths))
	}

	report := bench.MeasureSpillSignals(rows).Report()
	if *out == "" {
		fmt.Print(report)
		return nil
	}
	if err := os.WriteFile(*out, []byte(report), 0o644); err != nil {
		return fmt.Errorf("spillsignal: write %s: %w", *out, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s over %d decisions\n", *out, len(rows))
	return nil
}
