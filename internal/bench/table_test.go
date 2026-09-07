package bench_test

import (
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
)

func TestTheResultsTableKeepsTheThreeFailureColumnsApart(t *testing.T) {
	cells := []bench.Cell{{
		ID:          "round_robin-c8-r1",
		Driver:      bench.ClosedLoopDriver,
		Concurrency: 8,
		Repetition:  1,
		Summary: bench.Summary{
			Requests: 100, Successes: 90, Dropped: 4, Failed: 6, SLOViolations: 7,
			SLOApplied: true, GoodputRPS: 8.3, ThroughputRPS: 10.7,
			Flagged: true, FlagReasons: []string{"failure rate 10.00% exceeds the 1.00% threshold"},
		},
	}}

	got := bench.Table(cells)

	// The counts appear as separate cells of one row, in the documented order.
	if !strings.Contains(got, "| 100 | 90 | 4 | 6 | 7 |") {
		t.Errorf("the row does not carry requests, successes, dropped, failed and SLO violations as separate columns:\n%s", got)
	}
	if !strings.Contains(got, "failure rate 10.00%") {
		t.Errorf("the flag reason is not reported:\n%s", got)
	}
}

// A goodput figure is not interpretable without the driver that produced it,
// and the two drivers' rows share a schema so that they land in one table.
func TestEveryRowOfTheResultsTableSaysWhichDriverProducedIt(t *testing.T) {
	cells := []bench.Cell{{
		ID: "round_robin-c8-r1", Driver: bench.ClosedLoopDriver, Concurrency: 8, Repetition: 1,
		Summary: bench.Summary{Requests: 100, Successes: 100, GoodputRPS: 8.3, SLOApplied: true},
	}, {
		ID: "round_robin-a16-r1", Driver: bench.OpenLoopDriver, ArrivalRate: 16, Repetition: 1,
		Summary: bench.Summary{Requests: 200, Successes: 180, GoodputRPS: 12.1, SLOApplied: true},
	}}

	got := bench.Table(cells)

	if !strings.Contains(got, "| closed-loop | 8 users |") {
		t.Errorf("the closed-loop row does not name its driver and the concurrency it held:\n%s", got)
	}
	if !strings.Contains(got, "| open-loop | 16 req/s |") {
		t.Errorf("the open-loop row does not name its driver and the rate it offered:\n%s", got)
	}
}

// A cell recorded before cells named their driver must not be quietly rendered
// as one of them.
func TestACellThatDoesNotSayWhichDriverProducedItSaysSo(t *testing.T) {
	got := bench.Table([]bench.Cell{{ID: "round_robin-c8-r1", Concurrency: 8, Repetition: 1}})

	if !strings.Contains(got, "unstated") {
		t.Errorf("a cell with no driver was rendered as though it had one:\n%s", got)
	}
}

func TestACellWithNoSLOReportsNoGoodputRatherThanZero(t *testing.T) {
	cells := []bench.Cell{{
		ID: "round_robin-c1-r1", Concurrency: 1, Repetition: 1,
		Summary: bench.Summary{Requests: 10, Successes: 10, ThroughputRPS: 2.5},
	}}

	got := bench.Table(cells)

	if strings.Contains(got, "| 0.00 |") {
		t.Errorf("a cell with no SLO reported 0.00 goodput, which reads as a measured zero:\n%s", got)
	}
	if !strings.Contains(got, "—") {
		t.Errorf("the unmeasured columns are not marked as unmeasured:\n%s", got)
	}
}
