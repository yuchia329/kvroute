package bench_test

import (
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
)

// The turn geometry is what the two new axes are coordinates on, and what the
// cell's turn columns are read from. These test it at its own seam: a
// coordinate and a partition of the workload's user space, with no grid, no
// fleet and no I/O.

// Half a geometry is not a point. A turn count without a prompt size does not
// say what a session weighs, and both axes are swept against session footprint,
// so a cell carrying one of the two is on neither axis rather than on the one
// it stated.
func TestHalfAGeometryIsNotAPoint(t *testing.T) {
	for _, g := range []bench.Geometry{
		{},
		{TurnsPerSession: 4},
		{PromptTokens: 448},
	} {
		if g.Stated() {
			t.Errorf("%v reports itself as a stated geometry", g)
		}
	}
	if g := (bench.Geometry{TurnsPerSession: 4, PromptTokens: 448}); !g.Stated() {
		t.Errorf("%v states both halves and reports itself unstated", g)
	}
}

// A workload with conversations states its geometry and its pool; one without
// states neither. The fixed workload is single-turn requests with no history,
// so a turn count it was made to report would be an unreachable point on the
// axis rather than a workload that is not on it.
func TestOnlyAWorkloadWithConversationsStatesAGeometry(t *testing.T) {
	cfg := multiTurnConfig()
	cfg.TurnsPerSession = 8
	cfg.PromptTokens = 896
	conversations := newMultiTurn(t, cfg)

	want := bench.Geometry{TurnsPerSession: 8, PromptTokens: 896}
	if got := bench.OfferedGeometry(conversations); got != want {
		t.Errorf("geometry = %v, want %v", got, want)
	}
	if got := bench.OfferedSessions(conversations); got != cfg.Sessions {
		t.Errorf("session pool = %d, want the %d the trace draws from", got, cfg.Sessions)
	}

	fixed := bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, PromptBytes: 64, OutputTokens: 1})
	if got := bench.OfferedGeometry(fixed); got.Stated() {
		t.Errorf("a workload with no conversations reported the geometry %v", got)
	}
	if got := bench.OfferedSessions(fixed); got != 0 {
		t.Errorf("a workload with no session pool reported %d sessions", got)
	}
}

// Every cell the sweep runs is handed a Shifted workload, so a wrapper that
// swallowed the question would report both new axes as empty columns — and put
// every geometry point back on one slice of the user space, which is the
// contamination the partition exists to prevent.
func TestAShiftedWorkloadStillStatesItsGeometry(t *testing.T) {
	cfg := multiTurnConfig()
	cfg.TurnsPerSession = 8
	cfg.PromptTokens = 896
	base := newMultiTurn(t, cfg)

	shifted := bench.Shifted(base, 4*bench.WorkloadStride)

	if got, want := bench.OfferedGeometry(shifted), bench.OfferedGeometry(base); got != want {
		t.Errorf("shifting the workload changed its geometry to %v, want %v", got, want)
	}
	if got, want := bench.OfferedSessions(shifted), bench.OfferedSessions(base); got != want {
		t.Errorf("shifting the workload changed its pool to %d sessions, want %d", got, want)
	}
}

// The published geometry is the pivot the partition turns on: every cell
// already on disk ran at it, and the frozen comparison has to keep sending
// exactly the bytes it sent before. A workload that states no geometry at all
// is unmoved for the same reason.
func TestThePublishedAndUnstatedGeometriesMoveNothing(t *testing.T) {
	for _, g := range []bench.Geometry{
		{},
		{TurnsPerSession: bench.DefaultTurnsPerSession, PromptTokens: bench.DefaultPromptTokens},
	} {
		offset, err := bench.GeometryWorkloadOffset(g)
		if err != nil {
			t.Fatalf("%v: %v", g, err)
		}
		if offset != 0 {
			t.Errorf("%v moves the bytes every published cell sent, by %d", g, offset)
		}
	}
	// The two are unmoved for different reasons — one is the axis's origin, the
	// other is not on the axis — and only the published point is the default.
	if (bench.Geometry{}).Default() {
		t.Error("a workload with no conversations reports itself as sitting on the published geometry")
	}
}

// A geometry that named one half and not the other would otherwise fall through
// to offset zero and send the published cells' own bytes while carrying a turn
// count that says it did not. It is refused instead, because a half-stated
// geometry is not a point and the partition has nowhere to put it.
func TestAHalfStatedGeometryHasNoSliceOfItsOwn(t *testing.T) {
	for _, g := range []bench.Geometry{
		{TurnsPerSession: 16},
		{PromptTokens: 1792},
	} {
		if offset, err := bench.GeometryWorkloadOffset(g); err == nil {
			t.Errorf("%d turns of %d tokens names half a geometry and was encoded as %d",
				g.TurnsPerSession, g.PromptTokens, offset)
		}
	}
}

// Two geometries must never share a slice of the user space. A turn's text is
// filler(seed, tokens) cut to length, so the same turn at 112 prompt tokens is
// a byte-for-byte prefix of the same turn at 1,792, and a 16-turn cell resends
// a 4-turn cell's whole first four turns verbatim. A shared slice would have
// the second point reading its opening out of the replicas' caches instead of
// prefilling it — ADR-0004's contamination, landing on the independent variable.
func TestEveryGeometryGetsItsOwnSliceOfTheUserSpace(t *testing.T) {
	seen := map[int]bench.Geometry{}
	for _, turns := range []int{1, 2, 4, 8, 16, 32} {
		for _, prompt := range []int{112, 448, 896, 1792, 8192} {
			g := bench.Geometry{TurnsPerSession: turns, PromptTokens: prompt}
			offset, err := bench.GeometryWorkloadOffset(g)
			if err != nil {
				t.Fatalf("%v: %v", g, err)
			}
			if other, clash := seen[offset]; clash {
				t.Errorf("%v and %v share slice %d", g, other, offset)
			}
			seen[offset] = g
		}
	}
}

// A geometry the partition cannot encode is refused rather than rounded.
// Rounding two points onto one slice is a collision that stays invisible in
// every figure it corrupts, which is the one outcome this arithmetic must never
// produce.
func TestAGeometryOutsideThePartitionIsRefused(t *testing.T) {
	for _, g := range []bench.Geometry{
		{TurnsPerSession: 4, PromptTokens: 1 << 20},
		{TurnsPerSession: 1 << 12, PromptTokens: 448},
	} {
		if offset, err := bench.GeometryWorkloadOffset(g); err == nil {
			t.Errorf("%v is outside the span the user space is partitioned for and was encoded as %d", g, offset)
		}
	}
}

// The sweep adds the pressure point's offset to the geometry's, so the geometry
// partition has to step over every block a pressure point could reach. If it
// did not, a geometry point and a pressure point could add up to another pair's
// slice — a collision between two partitions that are each internally sound.
func TestTheGeometryPartitionStepsOverThePressureGridWhole(t *testing.T) {
	widest := 0
	for _, at := range bench.PressureGrid() {
		offset, err := bench.GridWorkloadOffset(at)
		if err != nil {
			t.Fatalf("%s: %v", at, err)
		}
		if offset > widest {
			widest = offset
		}
	}

	// The narrowest step the geometry axis can take: one prompt token at the
	// published turn count. Every other step is a multiple of it.
	step, err := bench.GeometryWorkloadOffset(bench.Geometry{
		TurnsPerSession: bench.DefaultTurnsPerSession,
		PromptTokens:    bench.DefaultPromptTokens + 1,
	})
	if err != nil {
		t.Fatalf("one token above the published geometry: %v", err)
	}
	// The sweep adds a cell's own offset to both partitions, so the step has to
	// clear the repetition and load axes as well. Three repetitions is what the
	// project runs; the span the constant is sized for is far above it.
	widest += bench.CellWorkloadOffset(bench.OpenLoopAt(512), 3)

	if step <= widest {
		t.Errorf("the geometry axis steps %d, which does not clear the %d a pressure point and a cell reach together: "+
			"a geometry point and a pressure point can add up to another pair's slice", step, widest)
	}
}

// The three partitions are added, so the only claim worth testing is that no
// two (geometry, pressure point, cell) triples land on one slice. Each
// partition being internally injective does not give that on its own.
func TestNoTwoGeometryPressureAndCellTriplesShareASlice(t *testing.T) {
	geometries := []bench.Geometry{
		{TurnsPerSession: bench.DefaultTurnsPerSession, PromptTokens: bench.DefaultPromptTokens},
		{TurnsPerSession: 4, PromptTokens: 112},
		{TurnsPerSession: 4, PromptTokens: 1792},
		{TurnsPerSession: 8, PromptTokens: 448},
		{TurnsPerSession: 16, PromptTokens: 448},
	}
	loads := []bench.Load{bench.ClosedLoopAt(1), bench.ClosedLoopAt(32), bench.OpenLoopAt(6), bench.OpenLoopAt(32)}

	type where struct {
		geometry bench.Geometry
		at       bench.GridPoint
		cell     string
	}
	sent := map[int]where{}
	for _, g := range geometries {
		geometry, err := bench.GeometryWorkloadOffset(g)
		if err != nil {
			t.Fatalf("%v: %v", g, err)
		}
		for _, at := range bench.PressureGrid() {
			pressure, err := bench.GridWorkloadOffset(at)
			if err != nil {
				t.Fatalf("%s: %v", at, err)
			}
			for _, load := range loads {
				for repetition := range 3 {
					offset := geometry + pressure + bench.CellWorkloadOffset(load, repetition)
					here := where{g, at, bench.CellID("p", load, repetition)}
					if other, clash := sent[offset]; clash {
						t.Fatalf("%v at %s (%s) and %v at %s (%s) both send from slice %d",
							here.geometry, here.at, here.cell, other.geometry, other.at, other.cell, offset)
					}
					sent[offset] = here
				}
			}
		}
	}
}

// The pressure grid's working set axis is bounded so the geometry partition has
// something finite to step over. A point beyond the bound would land inside a
// geometry point's slice, so it is refused rather than encoded.
func TestAWorkingSetBeyondThePartitionIsRefused(t *testing.T) {
	if _, err := bench.GridWorkloadOffset(bench.GridPoint{WorkingSet: 100, Skew: 0}); err == nil {
		t.Error("a working set ratio above the span the user space is partitioned for was accepted")
	}
	if _, err := bench.GridWorkloadOffset(bench.GridPoint{WorkingSet: 8, Skew: 1.4}); err != nil {
		t.Errorf("the top of the axis this project sweeps was refused: %v", err)
	}
}
