// Command overhead reports the router's own cost under each policy: accept to
// first dispatch, off the router's per-request rows.
//
//	overhead -out runs/overhead.md docs/measurements/2026-09-08-policy-comparison/router-*.jsonl.gz
//
// It exists so a reader can rule out the router having simply added latency the
// baselines did not pay. The cell records cannot answer that — TTFT is measured
// at the client, with the router's cost inside it — so this reads the rows the
// router wrote itself, in any form a run keeps them: .jsonl, .jsonl.gz or
// .parquet.
//
// Like compare and pressuremap it only reads, so it needs no fleet and no GPU.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

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
	out := flag.String("out", "", "where to write the report as markdown; empty prints it only")
	data := flag.String("data", "", "where to write the figure data as JSON, for `make figures` to draw; empty writes none")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: overhead [-out path] [-data path] <router rows>...\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Each file is a router's own rows: router.jsonl, a gzipped copy of it, or router.parquet.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	paths := flag.Args()
	if len(paths) == 0 {
		flag.Usage()
		return fmt.Errorf("overhead: name the router row files to read")
	}
	var rows []record.Request
	for _, path := range paths {
		read, err := bench.LoadRouterRows(path)
		if err != nil {
			return err
		}
		rows = append(rows, read...)
		fmt.Fprintf(os.Stderr, "overhead: read %d rows from %s\n", len(read), path)
	}
	overhead := bench.Overhead{Sources: paths, Policies: bench.RouterOverhead(rows)}
	if len(overhead.Policies) == 0 {
		return fmt.Errorf("overhead: none of the %d rows in %v was dispatched, so there is no overhead to report", len(rows), paths)
	}

	report := overhead.Report()
	fmt.Print(report)
	if *out != "" {
		if dir := filepath.Dir(*out); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("overhead: create %s: %w", dir, err)
			}
		}
		if err := os.WriteFile(*out, []byte(report), 0o644); err != nil {
			return fmt.Errorf("overhead: write %s: %w", *out, err)
		}
	}
	if *data != "" {
		return bench.WriteFigure(*data, overhead.Figure())
	}
	return nil
}
