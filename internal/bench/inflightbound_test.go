package bench_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// The bound is a label a cell carries, so it has to survive the round trip
// through the spec a command takes it as.
func TestAnInflightBoundSurvivesItsSpec(t *testing.T) {
	want := policy.InflightBound(bench.CacheRouteInflightBound)
	got, err := bench.ParseInflightBound(bench.FormatInflightBound(want))
	if err != nil {
		t.Fatalf("ParseInflightBound(%q): %v", bench.FormatInflightBound(want), err)
	}
	if got != want {
		t.Errorf("%v round-tripped to %v", want, got)
	}
}

// Zero is how a cell records that its policy had no bound, so it is not a bound
// a sweep may name: a cell labelled with it would be indistinguishable from a
// cell of a policy that has none.
func TestAnUnreadableInflightBoundIsRefused(t *testing.T) {
	for _, spec := range []string{"nonsense", "", "-0.25", "0", "0.25/2", "NaN", "Inf"} {
		if _, err := bench.ParseInflightBound(spec); err == nil {
			t.Errorf("ParseInflightBound(%q) was accepted", spec)
		}
	}
}

// The same argument the spill point's check rests on. The bound reaches the
// router as its own flag and the sweep is only told what it was, so nothing else
// stands between a mistyped flag and a grid of cells labelled with a bound that
// never ran.
func TestASweepRefusesARouterAtADifferentInflightBound(t *testing.T) {
	err := boundSweepAgainst(t, t.TempDir(), policy.BoundedSessionAffinityName, 0.25, 0.5)
	if err == nil {
		t.Fatal("a sweep was allowed to label its cells with a bound the router is not running")
	}
	if !strings.Contains(err.Error(), "0.25 above") || !strings.Contains(err.Error(), "0.5 above") {
		t.Errorf("the refusal does not name the two bounds: %v", err)
	}
}

// A policy with no bound reports none, and a sweep of it must not name one — and
// the policy that has one must not be swept unlabelled, which would record its
// cells as though nothing had bounded them.
func TestASweepMayNotMislabelWhetherItsPolicyIsBounded(t *testing.T) {
	if err := boundSweepAgainst(t, t.TempDir(), policy.SessionAffinityName, 0, 0); err != nil {
		t.Fatalf("a policy with no bound was refused: %v", err)
	}
	if err := boundSweepAgainst(t, t.TempDir(), policy.SessionAffinityName, 0, 0.25); err == nil {
		t.Error("session affinity's cells were allowed to name a bound, and it is blind to load")
	}
	if err := boundSweepAgainst(t, t.TempDir(), policy.BoundedSessionAffinityName, 0.25, 0); err == nil {
		t.Error("bounded session affinity's cells were allowed to record no bound, and the router was running one")
	}
}

// The cell carries the bound it ran at in the column #42 added for it, because a
// report places the cell by that column and never by its workload's name.
func TestACellRecordsTheInflightBoundItRanAt(t *testing.T) {
	dir := t.TempDir()
	if err := boundSweepAgainst(t, dir, policy.BoundedSessionAffinityName, 0.25, 0.25); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if cell := oneCellIn(t, dir); cell.InflightBound != 0.25 {
		t.Errorf("the cell records a bound of %v, and it ran at 0.25", cell.InflightBound)
	}
}

// Two bounds differ in nothing a cell id carries, so without this a sweep at a
// second bound would report the first one's cells cached and send nothing.
func TestASweepRefusesToResumeCellsOfAnotherInflightBound(t *testing.T) {
	dir := t.TempDir()
	if err := boundSweepAgainst(t, dir, policy.BoundedSessionAffinityName, 0.25, 0.25); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	err := boundSweepAgainst(t, dir, policy.BoundedSessionAffinityName, 0.5, 0.5)
	if err == nil {
		t.Fatal("a sweep at a second bound resumed the first one's cells")
	}
	if !strings.Contains(err.Error(), "inflight bound") {
		t.Errorf("the refusal does not say what disagreed: %v", err)
	}
}

// The bounded policy's two decisions are columns of their own, so that a cell
// whose bound moved nothing and one whose bound moved everything are told apart
// by the mix rather than by the goodput the mix is there to explain.
func TestTheBoundedPolicysDecisionsAreCountedApart(t *testing.T) {
	start := time.Unix(1757000000, 0)
	got := bench.Summarize([]bench.Result{
		decided(start, policy.ReasonBoundedSessionAffinity),
		decided(start, policy.ReasonBoundedSessionAffinity),
		decided(start, policy.ReasonBoundDeflected),
	}, bench.SummaryOptions{SLO: slo}).Decisions

	if got.BoundedSessionAffinity != 2 || got.BoundDeflected != 1 {
		t.Errorf("the bounded reasons counted as %+v", got)
	}
	if got.Undecided != 0 {
		t.Errorf("%d bounded decisions were counted as undecided, which is what a reason the harness does not know reads as", got.Undecided)
	}
	if got.Total() != 3 {
		t.Errorf("Total() = %d, want 3: the mix has to reconcile against the request count", got.Total())
	}
	if text := got.String(); !strings.Contains(text, "BOUND_DEFLECTED=1") || !strings.Contains(text, "BOUNDED_SESSION_AFFINITY=2") {
		t.Errorf("the rendered mix does not name the bounded decisions: %s", text)
	}
}

// boundSweepAgainst starts a router at one bound — zero being a policy that has
// none — and asks a sweep labelled with another to run against it.
func boundSweepAgainst(t *testing.T, dir, policyName string, running, labelled policy.InflightBound) error {
	t.Helper()
	_, replicaURL := fakeReplicaServer(t)
	target := routerTunedTo(t, policyName, policy.Options{InflightBound: running}, "replica-0="+replicaURL)

	_, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir:           dir,
		Target:        target,
		Policy:        policyName,
		InflightBound: labelled,
		Concurrencies: []int{1},
		Repetitions:   1,
		CellDuration:  20 * time.Millisecond,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 4096, OutputTokens: 1}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return err
}
