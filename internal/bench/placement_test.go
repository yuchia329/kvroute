package bench_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/record"
)

// on is a successful result that named the replica which served it. The timings
// are irrelevant to every test here and are the same on every row, so that a
// difference in what a placement figure reports is a difference in where the
// requests went.
func on(replica string, startedAt time.Time) bench.Result {
	r := success(startedAt, time.Second)
	r.Replica = replica
	return r
}

func TestPlacementCountsRequestsPerReplicaBecauseTheInflightColumnCouldNotBeTrusted(t *testing.T) {
	start := time.Unix(1757000000, 0)
	// Ten placed requests: five on a, three on b, two on c.
	results := []bench.Result{
		on("a", start), on("a", start), on("a", start), on("a", start), on("a", start),
		on("b", start), on("b", start), on("b", start),
		on("c", start), on("c", start),
	}

	got := bench.Summarize(results, bench.SummaryOptions{SLO: slo}).Placement

	if !got.Counted {
		t.Error("the placements were not marked counted, so they read as a cell that predates the count")
	}
	if got.Requests != 10 || got.Replicas != 3 {
		t.Errorf("counted %d requests over %d replicas, want 10 over 3", got.Requests, got.Replicas)
	}
	if got.Busiest != "a" || got.BusiestRequests != 5 {
		t.Errorf("busiest = %s with %d, want a with 5", got.Busiest, got.BusiestRequests)
	}
	if got.Quietest != "c" || got.QuietestRequests != 2 {
		t.Errorf("quietest = %s with %d, want c with 2", got.Quietest, got.QuietestRequests)
	}
	if share, ok := got.BusiestShare(); !ok || share != 0.5 {
		t.Errorf("busiest share = %v (%v), want 0.5: five of ten", share, ok)
	}
	if fair, ok := got.FairShare(); !ok || fair != 1.0/3.0 {
		t.Errorf("fair share = %v (%v), want a third: three replicas served", fair, ok)
	}
	if spread, ok := got.Spread(); !ok || spread != 2.5 {
		t.Errorf("spread = %v (%v), want 2.5: five over two", spread, ok)
	}
}

func TestWarmupRequestsAreLeftOutOfThePlacementCountsLikeEveryOtherFigure(t *testing.T) {
	start := time.Unix(1757000000, 0)
	warm := on("a", start)
	warm.Warmup = true
	results := []bench.Result{warm, warm, warm, on("a", start), on("b", start)}

	got := bench.Summarize(results, bench.SummaryOptions{SLO: slo}).Placement

	if got.Requests != 2 {
		t.Errorf("counted %d placed requests, want the 2 measured ones and not the 3 warm-up rows", got.Requests)
	}
	if spread, ok := got.Spread(); !ok || spread != 1 {
		t.Errorf("spread = %v (%v), want 1: the warm-up rows would have made it 4", spread, ok)
	}
}

// A request that ended on a replica still loaded that replica, whatever it ended
// as. Counting only successes would let a policy look balanced by overloading
// one card until it errored — the same trap the outcome taxonomy exists for.
func TestARequestThatFailedStillCountsAgainstTheReplicaThatTookIt(t *testing.T) {
	start := time.Unix(1757000000, 0)
	failed := outcome(start, record.OutcomeFailed)
	failed.Replica = "a"
	results := []bench.Result{on("a", start), failed, on("b", start)}

	got := bench.Summarize(results, bench.SummaryOptions{SLO: slo}).Placement

	if got.BusiestRequests != 2 || got.Busiest != "a" {
		t.Errorf("busiest = %s with %d, want a with 2: its failure loaded it too", got.Busiest, got.BusiestRequests)
	}
}

// A request the router could not place names no replica. It is counted apart
// rather than dropped, so the placements reconcile against the cell's own
// request count instead of quietly disagreeing with it.
func TestARequestTheRouterNeverPlacedIsCountedApartFromTheOnesItDid(t *testing.T) {
	start := time.Unix(1757000000, 0)
	unplaced := outcome(start, record.OutcomeDropped)
	results := []bench.Result{on("a", start), on("b", start), unplaced, unplaced}

	summary := bench.Summarize(results, bench.SummaryOptions{SLO: slo})
	got := summary.Placement

	if got.Requests != 2 || got.Unplaced != 2 {
		t.Errorf("counted %d placed and %d unplaced, want 2 and 2", got.Requests, got.Unplaced)
	}
	if got.Requests+got.Unplaced != summary.Requests {
		t.Errorf("%d placed plus %d unplaced is not the cell's %d measured requests",
			got.Requests, got.Unplaced, summary.Requests)
	}
}

// Nothing placed and nothing counted are different states, and only the flag
// tells them apart. A cell whose every request was refused is a result about a
// fleet that was falling over; a cell recorded before #27 is a cell nobody
// counted. Both are an empty record otherwise.
func TestACellThatPlacedNothingIsStillMarkedCounted(t *testing.T) {
	start := time.Unix(1757000000, 0)
	results := []bench.Result{outcome(start, record.OutcomeDropped)}

	got := bench.Summarize(results, bench.SummaryOptions{SLO: slo}).Placement

	if !got.Counted {
		t.Error("a cell that placed nothing was not marked counted, so it reads like one recorded before #27")
	}
	if _, ok := got.BusiestShare(); ok {
		t.Error("a busiest share was reported over no placed requests")
	}
}

// Every cell recorded before #27 carries no placement block at all, which is the
// truth about it: its rows were never counted. The zero value has to say so,
// because the alternative is a whole sweep reading as a fleet that served
// nothing.
func TestACellRecordedBeforeTheCountExistedSaysSoRatherThanReadingAsAnEmptyFleet(t *testing.T) {
	// Trimmed to the fields that matter here, in the shape the cells committed
	// under docs/measurements/ are actually on disk in.
	const old = `{"id":"session_affinity-c64-r1","policy":"session_affinity",
	  "summary":{"requests":819,"successes":819,"goodput_rps":7.08}}`

	var cell bench.Cell
	if err := json.Unmarshal([]byte(old), &cell); err != nil {
		t.Fatal(err)
	}

	if cell.Placement.Counted {
		t.Error("a cell with no placement block claims to have counted its placements")
	}
	if _, ok := cell.Placement.Spread(); ok {
		t.Error("a spread was reported for a cell whose rows nobody counted")
	}
	if _, ok := cell.Placement.BusiestShare(); ok {
		t.Error("a busiest share was reported for a cell whose rows nobody counted")
	}
}

func TestNoSpreadIsReportedUntilTwoReplicasHaveServedSomething(t *testing.T) {
	start := time.Unix(1757000000, 0)

	got := bench.Summarize([]bench.Result{on("a", start), on("a", start)}, bench.SummaryOptions{SLO: slo}).Placement

	if spread, ok := got.Spread(); ok {
		t.Errorf("spread = %v over one replica, and a ratio between a replica and itself is not a spread", spread)
	}
	// The share still means something: everything on one replica is 100% of the
	// requests, against a fair share of 100% when one replica is all that served.
	if share, ok := got.BusiestShare(); !ok || share != 1 {
		t.Errorf("busiest share = %v (%v), want 1", share, ok)
	}
}

// Two replicas tied for busiest must not resolve by whichever the map happened
// to yield first, or two readings of identical rows would name different
// replicas and the figure would stop being reproducible.
func TestTiesAreBrokenByReplicaIDSoTheFigureIsAFunctionOfTheRows(t *testing.T) {
	start := time.Unix(1757000000, 0)
	results := []bench.Result{on("b", start), on("a", start), on("c", start)}

	for range 20 {
		got := bench.Summarize(results, bench.SummaryOptions{SLO: slo}).Placement
		if got.Busiest != "a" || got.Quietest != "a" {
			t.Fatalf("busiest = %s, quietest = %s, want a for both: all three tied at one request",
				got.Busiest, got.Quietest)
		}
	}
}
