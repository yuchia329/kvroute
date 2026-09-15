package bench_test

import (
	"slices"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
)

// A workload is deterministic in (user, turn) so a re-run of a cell sends the
// same bytes. The cost of that is a second repetition re-sending the first's
// prompts, which the replica then answers out of its prefix cache instead of
// prefilling — measured on the box as 46 ms standing in for 325 ms. Shifting
// the user space is what separates the two.
func TestShiftedWorkloadSendsBytesTheUnshiftedOneNeverSends(t *testing.T) {
	base := bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, PromptBytes: 512, OutputTokens: 8, Seed: 1})
	shifted := bench.Shifted(base, bench.WorkloadStride)

	for turn := range 8 {
		if string(shifted.Next(0, turn).Body) == string(base.Next(0, turn).Body) {
			t.Errorf("turn %d sends the same bytes shifted as unshifted", turn)
		}
	}
	// A shift of zero is the workload itself: a cell that is re-run has to send
	// its own bytes again.
	if bench.Shifted(base, 0).Next(0, 0).Session != base.Next(0, 0).Session {
		t.Error("shifting by zero changed the workload")
	}
	if shifted.Name() != base.Name() {
		t.Errorf("shifting renamed the workload to %q", shifted.Name())
	}
}

// The offset is keyed on the axes and not on the policy: two cells differing
// only in repetition or load level must send different bytes, and the same
// cell under two policies must send identical bytes or the policies are not
// being compared on the same workload.
func TestCellWorkloadOffsetsSeparateRepetitionsAndLevelsAndNothingElse(t *testing.T) {
	var loads []bench.Load
	for _, concurrency := range bench.ConcurrencySweep {
		loads = append(loads, bench.ClosedLoopAt(concurrency))
	}
	// The rate axis crosses the concurrency axis: a rate of 8 requests per
	// second is a different cell from eight virtual users, and the two must not
	// land on the same prompts.
	for _, rate := range bench.ArrivalRateSweep {
		loads = append(loads, bench.OpenLoopAt(rate))
	}

	seen := map[int]string{}
	for _, load := range loads {
		for repetition := 1; repetition <= 3; repetition++ {
			offset := bench.CellWorkloadOffset(load, repetition)
			id := bench.CellID("round_robin", load, repetition)
			if other, clash := seen[offset]; clash {
				t.Errorf("%s and %s share workload offset %d, so one re-sends the other's prompts", id, other, offset)
			}
			seen[offset] = id
			// The user ids inside a closed-loop cell run 0..concurrency-1, so
			// two cells' ranges must not overlap either.
			if next := offset + load.Concurrency; next > offset+bench.WorkloadStride {
				t.Errorf("%s uses %d users, more than the %d stride between cells", id, load.Concurrency, bench.WorkloadStride)
			}
		}
	}

	if bench.CellWorkloadOffset(bench.ClosedLoopAt(8), 2) == bench.CellWorkloadOffset(bench.ClosedLoopAt(8), 1) {
		t.Error("two repetitions of the same cell send the same bytes")
	}
	if bench.CellWorkloadOffset(bench.ClosedLoopAt(16), 1) == bench.CellWorkloadOffset(bench.ClosedLoopAt(8), 1) {
		t.Error("two concurrency levels send the same bytes")
	}
	if bench.CellWorkloadOffset(bench.OpenLoopAt(8), 1) == bench.CellWorkloadOffset(bench.ClosedLoopAt(8), 1) {
		t.Error("a cell at 8 requests per second sends the same bytes as one at concurrency 8")
	}
}

// Rates round-trip through the flag spec for the same reason levels do.
func TestArrivalRatesSurviveARoundTripThroughTheSpecTheFlagsTake(t *testing.T) {
	for _, rates := range [][]float64{bench.ArrivalRateSweep, {0.5, 2.5}, {16}} {
		spec := bench.FormatRates(rates)
		got, err := bench.ParseRates(spec)
		if err != nil {
			t.Fatalf("parse %q: %v", spec, err)
		}
		if len(got) != len(rates) {
			t.Fatalf("parsing %q gave %v, want %v", spec, got, rates)
		}
		for i := range rates {
			if got[i] != rates[i] {
				t.Errorf("parsing %q gave %v, want %v", spec, got, rates)
			}
		}
	}
	if _, err := bench.ParseRates("4,-1"); err == nil {
		t.Error("a negative arrival rate was accepted")
	}
}

// The flag defaults in cmd/ are rendered from these values rather than spelled
// out again. A second copy is how the package gets changed and the command
// keeps measuring the old thing — which is exactly what happened once: the
// characterization's second load level was raised to 32 in the package while
// the flag default kept it at 16, and a full run measured the wrong level.
func TestLevelsSurviveARoundTripThroughTheSpecTheFlagsTake(t *testing.T) {
	for _, levels := range [][]int{bench.ConcurrencySweep, {1, 32}, {1}} {
		spec := bench.FormatLevels(levels)
		got, err := bench.ParseLevels(spec)
		if err != nil {
			t.Fatalf("parse %q: %v", spec, err)
		}
		if !slices.Equal(got, levels) {
			t.Errorf("%v rendered to %q and read back as %v", levels, spec, got)
		}
	}
}

func TestParseLevelsRefusesASpecThatIsNotLoadLevels(t *testing.T) {
	for _, spec := range []string{"", " , ", "0", "-4", "eight", "1,0"} {
		if got, err := bench.ParseLevels(spec); err == nil {
			t.Errorf("parsed %q as %v", spec, got)
		}
	}
}
