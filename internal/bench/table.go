package bench

import (
	"fmt"
	"strings"
	"time"
)

// Table renders cells as the results table.
//
// Every row names the driver that produced it. A goodput figure is not
// interpretable without it: the closed-loop driver throttles itself when the
// fleet slows, so its tail is optimistic, and the two drivers' rows share a
// schema precisely so they can be read side by side — which they cannot be if
// the table does not say which is which.
//
// Dropped, failed and SLO violations get a column each and are never summed
// into one. Goodput leads because it is the primary metric: throughput with a
// dead tail is how people lie about serving systems. Every cell keeps its own
// row rather than being averaged with its repetitions, because a flagged or
// unclean cell must be visible as itself before anything is aggregated.
func Table(cells []Cell) string {
	var b strings.Builder

	fmt.Fprintln(&b, "| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |")
	fmt.Fprintln(&b, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|")

	for _, c := range cells {
		goodput := "—"
		if c.SLOApplied {
			goodput = fmt.Sprintf("%.2f", c.GoodputRPS)
		}
		violations := "—"
		if c.SLOApplied {
			violations = fmt.Sprintf("%d", c.SLOViolations)
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %.2f | %s | %s | %s | %d | %d | %d | %d | %s | %s | %s |\n",
			DriverName(c.Driver), c.Load(), c.Repetition,
			goodput, c.ThroughputRPS,
			ms(c.TTFTP50Ns), ms(c.TTFTP99Ns), ms(c.ITLP50Ns),
			c.Requests, c.Successes, c.Dropped, c.Failed, violations,
			tick(c.Clean), flags(c),
		)
	}

	// A footnote rather than a column: the reasons are sentences, and a table
	// that truncated them would be a table that hid why a cell was excluded.
	notes := 0
	for _, c := range cells {
		if !c.Flagged {
			continue
		}
		if notes == 0 {
			fmt.Fprintln(&b)
			fmt.Fprintln(&b, "Flagged cells — these are not averaged in without being read first:")
			fmt.Fprintln(&b)
		}
		notes++
		for _, reason := range c.FlagReasons {
			fmt.Fprintf(&b, "- `%s`: %s\n", c.ID, reason)
		}
	}
	return b.String()
}

// DriverName is how a driver reads in prose and in a table. The recorded value
// is the machine-readable one; this is the same fact spelled for a reader.
func DriverName(driver string) string {
	switch driver {
	case ClosedLoopDriver:
		return "closed-loop"
	case OpenLoopDriver:
		return "open-loop"
	case "":
		// A cell recorded before cells named their driver. Better an admission
		// than a guess: this is the column the table exists to be honest about.
		return "**unstated**"
	default:
		return driver
	}
}

func ms(ns int64) string {
	if ns == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0fms", float64(time.Duration(ns).Nanoseconds())/1e6)
}

func tick(ok bool) string {
	if ok {
		return "yes"
	}
	return "**no**"
}

func flags(c Cell) string {
	if !c.Flagged {
		return ""
	}
	return fmt.Sprintf("**%d**", len(c.FlagReasons))
}
