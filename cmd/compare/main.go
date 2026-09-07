// Command compare puts two or more policies' sweeps in one table.
//
// The project's claim is comparative, so this is the shape the result is
// published in: goodput against the derived SLO, per policy, at each point of the
// load axis, with the spread across repetitions beside each figure.
//
//	compare -out runs/comparison.md \
//	        runs/concurrency-round_robin runs/concurrency-least_outstanding
//
// It only reads. Every figure comes from the cell records the sweeps wrote, so a
// comparison can be rebuilt from a repository checkout with no fleet running and
// no GPU present.
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
	out := flag.String("out", "", "where to write the comparison; empty prints it and writes nothing")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: compare [-out path] <sweep dir> <sweep dir>...\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Each directory is a sweep's -dir, holding the cells one policy produced.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "One directory holding several policies' cells works too.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	dirs := flag.Args()
	if len(dirs) == 0 {
		flag.Usage()
		return fmt.Errorf("compare: name the sweep directories to compare")
	}

	// Every directory's cells go into one pile, and the comparison sorts out what
	// belongs where from the cells' own labels. A policy's cells are identified by
	// what they record rather than by which directory they were found in, so
	// pointing this at one directory holding two policies' cells is the same
	// operation as pointing it at two.
	var cells []bench.Cell
	for _, dir := range dirs {
		// A directory that was never created is an axis that was not run, and the
		// table names the driver on every row, so its absence is visible in the
		// output rather than hidden by it. A directory that exists and holds no
		// cells is a different thing and LoadCells says so: something was pointed
		// at the wrong place, or a sweep left nothing behind.
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "compare: no %s, so nothing from it is in this table\n", dir)
			continue
		}
		found, err := bench.LoadCells(dir)
		if err != nil {
			return err
		}
		cells = append(cells, found...)
		fmt.Fprintf(os.Stderr, "compare: read %d cells from %s\n", len(found), dir)
	}
	if len(cells) == 0 {
		return fmt.Errorf("compare: none of %v holds any cells, so there is nothing to compare", dirs)
	}

	comparison, err := bench.Compare(cells)
	if err != nil {
		return err
	}
	report := comparison.Report()
	fmt.Print("\n" + report)

	if *out == "" {
		return nil
	}
	if err := os.WriteFile(*out, []byte(report), 0o644); err != nil {
		return fmt.Errorf("compare: write %s: %w", *out, err)
	}
	fmt.Fprintf(os.Stderr, "compare: wrote %s\n", *out)
	return nil
}
