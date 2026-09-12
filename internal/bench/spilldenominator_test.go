package bench_test

import (
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// The denominator is part of the grid point, so it survives the spec the
// commands take a point as. A point that lost it on the way to the router would
// label a cell with one rule and run the other.
func TestTheDenominatorSurvivesTheSpec(t *testing.T) {
	for _, want := range bench.LoadDenominatorCandidates() {
		got, err := bench.ParseSpill(bench.FormatSpill(want))
		if err != nil {
			t.Fatalf("ParseSpill(%q): %v", bench.FormatSpill(want), err)
		}
		if got != want {
			t.Errorf("%v round-tripped to %v", want, got)
		}
	}
}

// Every spec written before #31 names the point it named when it was written:
// the box scripts, the measurements' evidence and the cells of a sweep being
// resumed all carry two-part specs, and the rule they ran is the minimum.
func TestATwoPartSpecStillNamesTheRuleItAlwaysDid(t *testing.T) {
	got, err := bench.ParseSpill("0/2")
	if err != nil {
		t.Fatalf("ParseSpill: %v", err)
	}
	if got != (policy.Spill{LoadImbalanceFactor: 2}) {
		t.Errorf("0/2 parsed to %v, want the factor held against the fleet minimum", got)
	}
}

// And a denominator nobody implements is refused where it is written rather
// than silently read as the settled one.
func TestAnUnknownDenominatorIsRefused(t *testing.T) {
	for _, spec := range []string{"0/2/median", "0/2/", "0/2/MEAN"} {
		if _, err := bench.ParseSpill(spec); err == nil {
			t.Errorf("ParseSpill(%q) was accepted", spec)
		}
	}
}

// A disabled load condition has no denominator to name, and naming one would
// describe a comparison the run never made.
func TestADisabledLoadConditionIsWrittenWithoutADenominator(t *testing.T) {
	if got := bench.FormatSpill(policy.Spill{}); strings.Count(got, "/") != 1 {
		t.Errorf("the off point is written %q, want the two-part spec", got)
	}
}
