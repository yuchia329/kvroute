package bench_test

import (
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
)

func TestTheResultsTableKeepsTheThreeFailureColumnsApart(t *testing.T) {
	cells := []bench.Cell{{
		ID:          "round_robin-c8-r1",
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
