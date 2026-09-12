package bench_test

import (
	"slices"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// While the denominator grid is empty, the sweep is the pass that observes the
// comparison rather than one that changes it. The points are written only once a
// run has said what the fleet's minimum actually does at this rung and which
// points could fire there — the discipline #28 arrived at for the residency mark
// and #16 arrived at the expensive way.
func TestTheDenominatorSweepIsAnObservingPassUntilItsGridIsCut(t *testing.T) {
	sweep := bench.LoadDenominatorSweep()

	if len(bench.LoadDenominatorGrid) == 0 && len(sweep) != 1 {
		t.Errorf("the sweep runs %d points against an uncut grid, want the reference alone", len(sweep))
	}
	if sweep[0].Enabled() {
		t.Errorf("the sweep opens at %v, want the spill-off reference every point is read against", sweep[0])
	}
	for _, p := range sweep[1:] {
		if p.LoadImbalanceFactor <= 0 {
			t.Errorf("the grid holds %v, which does not run the condition it is sweeping", p)
		}
	}
}

// The rung is open-loop, because the closed-loop driver cannot show this: it
// holds concurrency fixed, so the fleet never empties between arrivals and the
// minimum never spends its time at zero. It is a rung of the goodput ladder so
// that its cells sit beside the ones already measured there.
func TestTheDenominatorIsMeasuredAtARungOfTheOpenLoopLadder(t *testing.T) {
	if !slices.Contains(bench.ArrivalRateSweep, bench.LoadDenominatorRate) {
		t.Errorf("the denominator runs at %v req/s, which is not a rung of the open-loop ladder %v",
			bench.LoadDenominatorRate, bench.ArrivalRateSweep)
	}
}

// The candidates the observing pass projects have to include the rule that
// actually ran, or nothing the projection says about the others can be checked
// against anything.
func TestTheCandidatesIncludeTheRuleThatIsRunningToday(t *testing.T) {
	candidates := bench.LoadDenominatorCandidates()

	settled := policy.Spill{LoadImbalanceFactor: bench.Chosen.LoadImbalanceFactor}
	if !slices.Contains(candidates, settled) {
		t.Errorf("the candidates %v do not include the settled point %v", candidates, settled)
	}
	var mean int
	for _, c := range candidates {
		if c.MeanInflightDenominator {
			mean++
		}
	}
	if mean == 0 {
		t.Error("no candidate holds the factor against the fleet mean, which is the axis being measured")
	}
}

// Every candidate has to be a point the router would run, or the projection
// would cut a grid towards a cell that cannot be configured.
func TestEveryCandidateIsAPointTheRouterWouldRun(t *testing.T) {
	for _, c := range bench.LoadDenominatorCandidates() {
		if err := c.Validate(); err != nil {
			t.Errorf("%v: %v", c, err)
		}
	}
}

// A cell's grid point is rebuilt from its columns in several places, and all of
// them have to see the same point — including the column that says what the
// factor was a multiple of.
func TestACellsGridPointIncludesItsDenominator(t *testing.T) {
	cell := bench.Cell{LoadImbalanceFactor: 2, MeanInflightDenominator: true}

	if got := bench.CellSpill(cell); !got.MeanInflightDenominator {
		t.Errorf("a cell measured against the fleet mean reads back as %v", got)
	}
}
