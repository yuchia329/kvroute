// Command divergence measures how wrong the router's prefix index was.
//
//	divergence -out runs/divergence.md \
//	           -calibration-out runs/divergence.json \
//	           runs/concurrency runs/goodput
//
// The router models state it does not own: the replicas evict on their own
// schedule and nothing tells it when they do. This reads every request a sweep
// recorded and puts the router's prefix match next to the engine's own account of
// how much of that prompt it did not have to compute, then bins the gap by the
// two things it should move with — the working set ratio, which decides whether
// the fleet can hold every session at once, and how long it had been since that
// conversation was last served.
//
// idea.md §1 records that nobody publishes this at any scale. The
// sticky-versus-cache-aware ablation has been published three times over at
// datacentre scale; the accuracy of the approximate index those routers decide on
// has not been, which is why this result stands whichever policy wins.
//
// It only reads. Every figure comes from the rows the sweeps wrote, so the
// measurement can be rebuilt from a repository checkout with no fleet running and
// no GPU present.
//
// With -calibration-out it also writes the reading that resizes the index: see
// cmd/calibrate -divergence, and ADR-0008.
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
	calibrationOut := flag.String("calibration-out", "",
		"where to write the divergence reading that resizes the prefix index, for `calibrate -divergence`. "+
			"It covers only the policies that actually consulted an index: a policy that predicts nothing on every request says nothing about how large the index should be")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: divergence [-out path] [-calibration-out path] <sweep dir>...\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Each directory is a sweep's -dir, holding the cells and per-request rows it produced.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Name several to draw the working-set axis across the points of a pressure grid.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	dirs := flag.Args()
	if len(dirs) == 0 {
		flag.Usage()
		return fmt.Errorf("divergence: name the sweep directories to measure")
	}

	report, err := bench.MeasureDivergence(dirs)
	if err != nil {
		return err
	}
	rendered := report.Report()
	fmt.Print("\n" + rendered)

	if *out != "" {
		if err := write(*out, []byte(rendered)); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "divergence: wrote %s\n", *out)
	}

	if *calibrationOut == "" {
		return nil
	}
	measured := report.ForCalibration()
	if !measured.Evidenced() {
		// Refused rather than written empty. A calibration file that says a run
		// measured nothing is indistinguishable, at the next bring-up, from one
		// that measured an index which was never wrong.
		return fmt.Errorf("divergence: no policy in %v consulted a prefix index, so there is no reading to calibrate one with. "+
			"Sweep %s first", dirs, "prefix_affinity")
	}
	if err := mkdirFor(*calibrationOut); err != nil {
		return err
	}
	if err := measured.Save(*calibrationOut); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "divergence: wrote %s — pass it to calibrate -divergence to resize the index\n", *calibrationOut)
	return nil
}

func write(path string, contents []byte) error {
	if err := mkdirFor(path); err != nil {
		return err
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		return fmt.Errorf("divergence: write %s: %w", path, err)
	}
	return nil
}

// mkdirFor creates the directory a path lives in. The report is already on
// stdout by the time this runs, so a failure here loses nothing — but after a
// sweep that took a night of GPU time to earn it should not fail over a
// directory that does not exist yet.
func mkdirFor(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("divergence: create %s: %w", dir, err)
	}
	return nil
}
