// Command pressuremap draws the project's headline figure.
//
//	pressuremap -out runs/pressuremap.md runs/pressure/ws*-skew*
//
// The pressure grid sweeps working set ratio against Zipf skew at one
// concurrency, for every policy. The two axes are there because the two
// pressures are physically different: working set drives eviction, skew drives
// load imbalance, and they fire different branches of the spill rule. This puts
// the goodput delta between session affinity and prefix affinity on that grid,
// which is the answer to where cache-aware routing pays for itself and where it
// does not.
//
// Each point of the grid is swept into its own directory, because each point
// sends its own workload and a comparison across two workloads is not a
// comparison. Name them all here and the map is drawn across them; name some
// and it says which points it is missing.
//
// It only reads. Every figure comes from the cell records the sweeps wrote, so
// the map can be rebuilt from a repository checkout with no fleet running and no
// GPU present.
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
	out := flag.String("out", "", "where to write the map; empty prints it and writes nothing")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: pressuremap [-out path] <sweep dir>...\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Each directory is one grid point's -dir, holding the cells it produced.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Name every point of the grid to draw the whole map.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	dirs := flag.Args()
	if len(dirs) == 0 {
		flag.Usage()
		return fmt.Errorf("pressuremap: name the sweep directories to map")
	}

	var cells []bench.Cell
	for _, dir := range dirs {
		found, err := bench.LoadCells(dir)
		if err != nil {
			return err
		}
		cells = append(cells, found...)
	}

	m, err := bench.BuildPressureMap(cells)
	if err != nil {
		return err
	}
	rendered := m.Report()
	fmt.Print("\n" + rendered)

	if *out != "" {
		if err := write(*out, []byte(rendered)); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "pressuremap: wrote %s\n", *out)
	}

	// The exit status carries the one verdict a script should be able to act on
	// without parsing markdown. A grid that never applied pressure is not a
	// result to publish and not a policy to tune: idea.md §6 says to correct the
	// workload first, and a pass that returned zero here would let a nightly run
	// file it as a finished measurement.
	if m.SuspectTheWorkload() {
		return fmt.Errorf("pressuremap: no point of this grid separated the policies and none of them exercised the mechanism either — " +
			"no differing prefill, no differing cache hit rates, and not one declined match. " +
			"That is a workload that applied no pressure rather than a null result: correct it before touching any policy")
	}
	return nil
}

func write(path string, contents []byte) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("pressuremap: create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		return fmt.Errorf("pressuremap: write %s: %w", path, err)
	}
	return nil
}
