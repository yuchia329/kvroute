// Command regimemap draws which policy won at each recorded point of a run.
//
//	regimemap -out runs/regimemap.md runs/pressure/ws*-skew*
//	regimemap -load-axis -out runs/regimemap.md runs/sweep/concurrency runs/sweep/goodput
//
// It is a view over the same comparison the pressure map and the sweep
// tables already build, never a second reduction of the cells: pooling,
// exclusion, the surfaced-cell rule and the one-SLO check all happen once, in
// bench.Compare and bench.BuildPressureMap, and this command reads their
// output rather than repeating any of it.
//
// Without -load-axis, each named directory is one pressure-grid point's -dir,
// the same shape pressuremap takes, and the map is laid out on skew against
// working set — one tile per grid point, naming the winner and its margin
// over the runner-up.
//
// With -load-axis, every named directory is compared together across the
// load axis instead — the same shape a sweep's own comparison report takes —
// and the map is laid out on driver against load rung: closed-loop against
// open-loop, each ascending.
//
// It only reads. Every figure comes from the cell records the sweeps wrote,
// so the map can be rebuilt from a repository checkout with no fleet running
// and no GPU present.
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
	data := flag.String("data", "", "where to write the figure data as JSON, for `make figures` to draw; empty writes none")
	loadAxis := flag.Bool("load-axis", false,
		"draw the load axis (driver x load) from a Compare-style comparison, instead of the pressure grid (working set x skew)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: regimemap [-out path] [-data path] [-load-axis] <sweep dir>...\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Without -load-axis, each directory is one pressure-grid point's -dir, holding\n")
		fmt.Fprintf(flag.CommandLine.Output(), "the cells it produced. With -load-axis, every directory is compared together\n")
		fmt.Fprintf(flag.CommandLine.Output(), "across the load axis, the way a sweep's own comparison report is.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	dirs := flag.Args()
	if len(dirs) == 0 {
		flag.Usage()
		return fmt.Errorf("regimemap: name the sweep directories to map")
	}

	var cells []bench.Cell
	for _, dir := range dirs {
		found, err := bench.LoadCells(dir)
		if err != nil {
			return err
		}
		cells = append(cells, found...)
	}

	var regime bench.RegimeMap
	if *loadAxis {
		c, err := bench.Compare(cells)
		if err != nil {
			return err
		}
		regime = c.Regime()
	} else {
		m, err := bench.BuildPressureMap(cells)
		if err != nil {
			return err
		}
		regime = m.Regime()
	}

	rendered := regime.Report()
	fmt.Print("\n" + rendered)

	if *out != "" {
		if err := write(*out, []byte(rendered)); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "regimemap: wrote %s\n", *out)
	}
	if *data != "" {
		if err := bench.WriteFigure(*data, regime.Figure()); err != nil {
			return err
		}
	}
	return nil
}

func write(path string, contents []byte) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("regimemap: create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		return fmt.Errorf("regimemap: write %s: %w", path, err)
	}
	return nil
}
