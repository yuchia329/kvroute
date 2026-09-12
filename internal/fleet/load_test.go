package fleet_test

import (
	"testing"

	"github.com/yuchia329/kvroute/internal/fleet"
)

// loaded is a snapshot of replicas carrying these inflight counts, which is the
// only part of a candidate these two figures read.
func loaded(counts ...int) fleet.State {
	replicas := make([]fleet.Candidate, len(counts))
	for i, n := range counts {
		replicas[i] = fleet.Candidate{Replica: fleet.Replica{ID: string(rune('a' + i))}, Inflight: n}
	}
	return fleet.State{Replicas: replicas}
}

// The two figures a load comparison can be made against, read off the one
// snapshot the decision saw.
func TestASnapshotReportsItsMinimumAndItsMeanInflight(t *testing.T) {
	state := loaded(0, 1, 3, 8)

	if got := state.MinInflight(); got != 0 {
		t.Errorf("minimum inflight = %d, want 0", got)
	}
	if got, want := state.MeanInflight(), 3.0; got != want {
		t.Errorf("mean inflight = %v, want %v", got, want)
	}
}

// The distinction the whole of #31 turns on: a fleet holding twelve requests
// across five replicas has a mean of 2.4 and, very often, a minimum of 0. The
// two numbers describe the same fleet and a rule comparing against one of them
// is not the rule comparing against the other. Measured at that rung, the
// minimum was never above 1 across 2,361 decisions and the mean ran to 2.8.
func TestAnIdleReplicaDoesNotMakeTheFleetIdle(t *testing.T) {
	state := loaded(0, 1, 2, 4, 5)

	if got := state.MinInflight(); got != 0 {
		t.Errorf("minimum inflight = %d, want 0: one replica is idle", got)
	}
	if got, want := state.MeanInflight(), 2.4; got != want {
		t.Errorf("mean inflight = %v, want %v: the fleet is holding twelve requests", got, want)
	}
}

// A fleet with nothing in it has neither. Nothing reaches a policy without a
// candidate, so this is about the figures being defined rather than about a
// decision that could be made from them.
func TestAnEmptyFleetHasNoMinimumAndNoMean(t *testing.T) {
	state := fleet.State{}

	if got := state.MinInflight(); got != 0 {
		t.Errorf("minimum inflight = %d, want 0", got)
	}
	if got := state.MeanInflight(); got != 0 {
		t.Errorf("mean inflight = %v, want 0", got)
	}
}
