// Command disagg does idea.md §8's arithmetic from one measurement directory:
// the KV a request carries, the bandwidth measured across each class of link
// between the host's cards, and what moving the one over the other costs next
// to the prefill it follows.
//
//	disagg -out docs/measurements/2026-09-11-pcie-arithmetic/report.md docs/measurements/2026-09-11-pcie-arithmetic
//
// Like compare and recovery it reads records only, so it needs no fleet and no
// GPU. The records are what ops/pcie/measure.sh leaves on the box.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yuchia329/kvroute/internal/disagg"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "", "where to write the report as markdown; empty prints it only")
	flag.Parse()

	if flag.NArg() != 1 {
		return fmt.Errorf("disagg: name one measurement directory, as ops/pcie/measure.sh leaves it")
	}
	in, err := disagg.Load(flag.Arg(0))
	if err != nil {
		return err
	}
	analysis, err := disagg.Analyse(in)
	if err != nil {
		return err
	}

	report := analysis.Report()
	fmt.Print(report)
	if *out == "" {
		return nil
	}
	return os.WriteFile(*out, []byte(report), 0o644)
}
