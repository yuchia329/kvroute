package bench_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/record"
)

// The router reports a reroute in a response header because the body cannot
// carry it, and the harness puts it on its own row. Without that the summary's
// rerouted count would be a column of zeros however many requests the router
// saved, and the drop count beside it would stand alone.
func TestTheHarnessRowCarriesTheRoutersReroute(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	_, alive := fakeReplicaServer(t)
	// Round-robin sends the first request to replica-0, which is not there.
	target := routerFor(t, "replica-0="+deadURL, "replica-1="+alive)

	row := onlyRequest(t, target)

	if row.Outcome != record.OutcomeSuccess || row.Replica != "replica-1" {
		t.Fatalf("outcome=%s replica=%s, want a success from replica-1", row.Outcome, row.Replica)
	}
	if row.Reroutes != 1 {
		t.Errorf("reroutes = %d, want 1: the router moved this request off a dead replica", row.Reroutes)
	}
}

// A reroute is invisible in the response by design, so the summary is where it
// becomes a number. Rerouted requests are counted on their own, beside the
// dropped ones rather than instead of them: a count of drops says what the
// router could not save, and only the count of reroutes beside it says how much
// it did save — together they are the claim idea.md §7 allows to be made about a
// replica dying, and neither is that claim alone.
//
// A rerouted request that was then answered in full is a success like any
// other. The client could not tell it had been moved, and its latency, reroute
// and all, is the latency it felt.
func TestReroutedRequestsAreCountedApartFromDroppedOnes(t *testing.T) {
	start := time.Unix(1757000000, 0)
	rerouted := success(start, time.Second)
	rerouted.Reroutes = 1
	// A request rerouted twice is still one rerouted request: the column counts
	// requests the reroute saved, not the attempts it took to save them.
	reroutedTwice := success(start, time.Second)
	reroutedTwice.Reroutes = 2
	// Warm-up rows are excluded from every count, and this one is no exception.
	warm := success(start, time.Second)
	warm.Reroutes, warm.Warmup = 1, true
	rows := []bench.Result{
		success(start, time.Second),
		rerouted,
		reroutedTwice,
		outcome(start, record.OutcomeDropped),
		warm,
	}

	got := bench.Summarize(rows, bench.SummaryOptions{SLO: slo})

	if got.Rerouted != 2 {
		t.Errorf("rerouted = %d, want 2: two measured requests were moved to another replica", got.Rerouted)
	}
	if got.Successes != 3 {
		t.Errorf("successes = %d, want 3: a rerouted request answered in full is a success", got.Successes)
	}
	if got.Dropped != 1 {
		t.Errorf("dropped = %d, want 1: a reroute is not a drop, and it does not cancel one either", got.Dropped)
	}
}
