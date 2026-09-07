package bench_test

import (
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
// only in repetition or concurrency must send different bytes, and the same
// cell under two policies must send identical bytes or the policies are not
// being compared on the same workload.
func TestCellWorkloadOffsetsSeparateRepetitionsAndLevelsAndNothingElse(t *testing.T) {
	seen := map[int]string{}
	for _, concurrency := range bench.ConcurrencySweep {
		for repetition := 1; repetition <= 3; repetition++ {
			offset := bench.CellWorkloadOffset(concurrency, repetition)
			id := bench.CellID("round_robin", concurrency, repetition)
			if other, clash := seen[offset]; clash {
				t.Errorf("%s and %s share workload offset %d, so one re-sends the other's prompts", id, other, offset)
			}
			seen[offset] = id
			// The user ids inside a cell run 0..concurrency-1, so two cells'
			// ranges must not overlap either.
			if next := bench.CellWorkloadOffset(concurrency, repetition) + concurrency; next > offset+bench.WorkloadStride {
				t.Errorf("%s uses %d users, more than the %d stride between cells", id, concurrency, bench.WorkloadStride)
			}
		}
	}
	if bench.CellWorkloadOffset(8, 2) == bench.CellWorkloadOffset(8, 1) {
		t.Error("two repetitions of the same cell send the same bytes")
	}
	if bench.CellWorkloadOffset(16, 1) == bench.CellWorkloadOffset(8, 1) {
		t.Error("two concurrency levels send the same bytes")
	}
}
