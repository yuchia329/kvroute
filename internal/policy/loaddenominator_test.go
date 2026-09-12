package policy_test

import (
	"testing"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
)

// inflight is a snapshot of replicas carrying these counts. The load condition
// reads nothing else off a candidate.
func inflight(counts ...int) fleet.State {
	replicas := make([]fleet.Candidate, len(counts))
	for i, n := range counts {
		replicas[i] = fleet.Candidate{Replica: fleet.Replica{ID: string(rune('a' + i))}, Inflight: n}
	}
	return fleet.State{Replicas: replicas}
}

// The rung the factor was settled at. Thirty-two users over five replicas, and
// the quietest replica is carrying real work, so the two denominators are close
// and the factor means much the same under either.
var busy = inflight(3, 5, 6, 8, 10)

// #19's rung: 6 requests per second open-loop over five replicas, where the
// fleet holds six requests at the median and one replica is idle between
// arrivals. Twelve is the busy end of what that rung reaches.
var quiet = inflight(0, 1, 2, 4, 5)

// The degeneracy #31 found. Against the minimum, a factor of 2 on this fleet
// declines any replica holding three or more requests — an absolute inflight
// threshold, not the ratio the rule is written as — and three of these five
// replicas are over it.
func TestAtLowLoadTheMinimumTurnsTheFactorIntoAnAbsoluteThreshold(t *testing.T) {
	rule := policy.Spill{LoadImbalanceFactor: 2}

	for _, n := range []int{3, 4, 5} {
		if _, declined := rule.Declines(fleet.Candidate{Inflight: n}, quiet); !declined {
			t.Errorf("a replica holding %d was kept: 2 x the floor of one is 2, so anything above it spills", n)
		}
	}
	if _, declined := rule.Declines(fleet.Candidate{Inflight: 2}, quiet); declined {
		t.Error("a replica holding 2 was declined, which is not what a factor of 2 over a floor of one says")
	}
}

// And the same fleet under the other denominator. It is holding twelve requests
// over five replicas, so the average replica is carrying 2.4 and a factor of 2
// asks for 4.8 — an imbalance rather than a queue of three.
func TestAgainstTheFleetMeanTheSameFactorAsksForAnImbalance(t *testing.T) {
	rule := policy.Spill{LoadImbalanceFactor: 2, MeanInflightDenominator: true}

	for _, n := range []int{3, 4} {
		if _, declined := rule.Declines(fleet.Candidate{Inflight: n}, quiet); declined {
			t.Errorf("a replica holding %d was declined, against a fleet averaging 2.4: that is not an imbalance", n)
		}
	}
	if _, declined := rule.Declines(fleet.Candidate{Inflight: 5}, quiet); !declined {
		t.Error("a replica holding 5 was kept, at more than twice what the average replica is carrying")
	}
}

// The rung the factor was settled at is where the two denominators have to
// agree, or changing one would silently restate #18's grid. They do not agree
// exactly — the mean is never below the minimum — but at 32 users the gap is
// one replica's worth of work rather than the whole comparison.
func TestAtTheSettledRungTheTwoDenominatorsAskForMuchTheSameThing(t *testing.T) {
	against := policy.Spill{LoadImbalanceFactor: 2}
	mean := policy.Spill{LoadImbalanceFactor: 2, MeanInflightDenominator: true}

	// Minimum 3, mean 6.4: the thresholds are 6 and 12.8.
	if _, declined := against.Declines(fleet.Candidate{Inflight: 13}, busy); !declined {
		t.Error("a replica holding 13 was kept under the minimum denominator")
	}
	if _, declined := mean.Declines(fleet.Candidate{Inflight: 13}, busy); !declined {
		t.Error("a replica holding 13 was kept under the mean denominator")
	}
	if _, declined := against.Declines(fleet.Candidate{Inflight: 5}, busy); declined {
		t.Error("a replica holding 5 was declined under the minimum denominator")
	}
	if _, declined := mean.Declines(fleet.Candidate{Inflight: 5}, busy); declined {
		t.Error("a replica holding 5 was declined under the mean denominator")
	}
}

// The floor survives the change of denominator, because the failure it exists
// to prevent does. An idle fleet averages nothing, and any factor times nothing
// declines every match to a replica holding a single request.
func TestAnIdleFleetDoesNotSpillUnderEitherDenominator(t *testing.T) {
	idle := inflight(0, 0, 0, 0, 0)

	for _, rule := range []policy.Spill{
		{LoadImbalanceFactor: 2},
		{LoadImbalanceFactor: 2, MeanInflightDenominator: true},
	} {
		if _, declined := rule.Declines(fleet.Candidate{Inflight: 1}, idle); declined {
			t.Errorf("%v declined the only request on an idle fleet", rule)
		}
	}
}

// The denominator is part of the grid point, so a run says which one it was
// made at. A cell labelled with a factor alone would be two different rules
// wearing one number.
func TestTheDenominatorIsPartOfTheGridPointAndIsRendered(t *testing.T) {
	against := policy.Spill{LoadImbalanceFactor: 2}
	mean := policy.Spill{LoadImbalanceFactor: 2, MeanInflightDenominator: true}

	if against.String() == mean.String() {
		t.Fatalf("both denominators render as %q, so a cell cannot say which rule it ran", against)
	}
	if against == mean {
		t.Error("two grid points compared equal, so the harness could not refuse a mislabelled sweep")
	}
}

// A denominator without a factor is a setting for a condition that is switched
// off, which is far more likely to be a half-written grid point than a
// deliberate one.
func TestADenominatorWithNoFactorIsRefused(t *testing.T) {
	rule := policy.Spill{MeanInflightDenominator: true}

	if err := rule.Validate(); err == nil {
		t.Error("a mean denominator with the load condition disabled was accepted")
	}
}
