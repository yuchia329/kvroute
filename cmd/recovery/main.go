// Command recovery compares chaos runs of one scenario across policies: their
// recovery curves side by side, the numbers read off each, and each run's drop
// accounting in full.
//
//	recovery -out runs/recovery-kill.md runs/chaos/kill-session_affinity runs/chaos/kill-prefix_affinity
//
// Like compare and pressuremap it reads records only, so it needs no fleet and no
// GPU. It refuses runs that did not face the same failure — a different replica,
// fault, load, timing, SLO or workload — rather than drawing curves that differ
// for reasons that are not the policy.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yuchia329/kvroute/internal/bench"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "", "where to write the comparison as markdown; empty prints it only")
	data := flag.String("data", "", "where to write the figure data as JSON, for `make figures` to draw; empty writes none")
	flag.Parse()

	dirs := flag.Args()
	if len(dirs) < 2 {
		return fmt.Errorf("recovery: name the directories of at least two chaos runs of one scenario, one run per policy")
	}
	runs := make([]bench.ChaosRun, 0, len(dirs))
	for _, dir := range dirs {
		s, err := bench.LoadChaosRun(dir)
		if err != nil {
			return err
		}
		runs = append(runs, s)
	}
	comparison, err := bench.CompareRecovery(runs)
	if err != nil {
		return err
	}

	report := comparison.Report()
	fmt.Print(report)
	if *out != "" {
		if err := os.WriteFile(*out, []byte(report), 0o644); err != nil {
			return err
		}
	}
	if *data != "" {
		return bench.WriteFigure(*data, comparison.Figure())
	}
	return nil
}
