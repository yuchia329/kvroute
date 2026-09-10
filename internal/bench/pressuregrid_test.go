package bench_test

import (
	"slices"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/characterize"
)

// The WS axis is defined in internal/characterize and restated in internal/bench
// because characterize imports bench and cannot be imported back. This is the
// only place both can be seen at once, so it is the only place the restatement
// can be held to the original — and a pressure grid swept on a different axis
// from the one the capacity record sizes its session pools against would be a
// grid whose labels mean something else.
func TestTheGridsWorkingSetAxisIsTheOneCharacterizationSizes(t *testing.T) {
	if !slices.Equal(bench.PressureWorkingSets, characterize.WorkingSetPoints) {
		t.Errorf("the grid sweeps WS %v and the characterization sizes session pools for %v; "+
			"they are one axis and have drifted apart",
			bench.PressureWorkingSets, characterize.WorkingSetPoints)
	}
}

// idea.md §6 sizes the headline sweep at four WS points by three skew points.
// The budget rests on it: this is the most expensive sweep in the project, and a
// fifth point on either axis is another three hours of a shared box.
func TestTheGridIsFourWorkingSetsByThreeSkews(t *testing.T) {
	if len(bench.PressureWorkingSets) != 4 {
		t.Errorf("the WS axis has %d points, want the four §6 budgets for", len(bench.PressureWorkingSets))
	}
	if len(bench.PressureSkews) != 3 {
		t.Errorf("the skew axis has %d points, want the three §6 budgets for", len(bench.PressureSkews))
	}
	if got, want := len(bench.PressureGrid()), 12; got != want {
		t.Errorf("the grid has %d points, want %d", got, want)
	}
}

// Both axes ascend, and the skew axis opens on a uniform draw.
//
// Uniform leads because it is the control: it is the only point on that axis
// where the working set a cell was configured for is close to the working set it
// applies, so every other skew's discount is read against it.
func TestBothAxesAscendAndSkewOpensUniform(t *testing.T) {
	if !slices.IsSorted(bench.PressureWorkingSets) {
		t.Errorf("the WS axis does not ascend: %v", bench.PressureWorkingSets)
	}
	if !slices.IsSorted(bench.PressureSkews) {
		t.Errorf("the skew axis does not ascend: %v", bench.PressureSkews)
	}
	if bench.PressureSkews[0] != 0 {
		t.Errorf("the skew axis opens at %v, not at the uniform draw the rest are read against", bench.PressureSkews[0])
	}
	for _, ws := range bench.PressureWorkingSets {
		if ws <= 0 {
			t.Errorf("the WS axis holds %v, which is how a cell records that it stated no working set at all", ws)
		}
	}
}

// The grid is swept and drawn in one order, so a table can be read against the
// run that produced it without re-sorting either.
func TestTheGridClimbsWorkingSetThenSkew(t *testing.T) {
	points := bench.PressureGrid()
	for i := 1; i < len(points); i++ {
		prev, at := points[i-1], points[i]
		if prev.WorkingSet > at.WorkingSet {
			t.Fatalf("point %d (%s) follows %s: the WS axis does not ascend", i, at, prev)
		}
		if prev.WorkingSet == at.WorkingSet && prev.Skew >= at.Skew {
			t.Fatalf("point %d (%s) follows %s: skew does not ascend within a WS point", i, at, prev)
		}
	}
}

// Each point is swept into its own directory, because each sends its own
// workload and Compare refuses two workloads in one table. The key is that
// directory's name, so no two points may share one.
func TestEveryGridPointHasItsOwnDirectoryName(t *testing.T) {
	seen := map[string]bench.GridPoint{}
	for _, at := range bench.PressureGrid() {
		if clash, taken := seen[at.Key()]; taken {
			t.Errorf("%s and %s both key to %q, so one would be swept into the other's directory", at, clash, at.Key())
		}
		seen[at.Key()] = at
	}
	if got, want := (bench.GridPoint{WorkingSet: 3, Skew: 1.4}).Key(), "ws3-skew1.4"; got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
}

// A grid point is a label a command takes, so it has to survive the round trip
// through its spec.
func TestAPressureGridPointSurvivesItsSpec(t *testing.T) {
	for _, want := range bench.PressureGrid() {
		got, err := bench.ParseGridPoint(bench.FormatGridPoint(want))
		if err != nil {
			t.Errorf("%s does not survive its own spec %q: %v", want, bench.FormatGridPoint(want), err)
			continue
		}
		if got != want {
			t.Errorf("%q parsed to %s, want %s", bench.FormatGridPoint(want), got, want)
		}
	}
}

// Zero is how a cell records that it stated no working set at all, so it must
// not also be a point somebody can ask for: the two would be indistinguishable
// in the record, and a sweep run without -kv-capacity would look like a
// deliberate point of the axis.
func TestAZeroWorkingSetIsNotAPointOnTheAxis(t *testing.T) {
	for _, spec := range []string{"0/0", "0/1.4", "-1/0", "3/-1", "3", "x/1"} {
		if got, err := bench.ParseGridPoint(spec); err == nil {
			t.Errorf("%q parsed to %s, want a refusal", spec, got)
		}
	}
}

// A cell carries its point on two flat columns, and the map reads it off them
// rather than parsing it out of the workload name.
func TestAPointIsReadOffTheCellsOwnColumns(t *testing.T) {
	at := bench.GridPointOf(bench.Cell{WorkingSet: 3, Skew: 1.4})
	if want := (bench.GridPoint{WorkingSet: 3, Skew: 1.4}); at != want {
		t.Errorf("point = %s, want %s", at, want)
	}
	if !at.Stated() {
		t.Errorf("%s says it states no working set", at)
	}
	// A cell whose workload named a session count but no capacity to be a ratio
	// against is on no point of the grid, and zero must not read as the bottom
	// of the axis.
	unstated := bench.GridPointOf(bench.Cell{Skew: 1.4})
	if unstated.Stated() {
		t.Errorf("%s claims to state a working set", unstated)
	}
}

// Two points of the grid must not send one prompt in common.
//
// This is the failure the grid is uniquely exposed to. CellWorkloadOffset
// separates cells by load level and repetition, and every point of the grid runs
// at the same concurrency and the same repetitions — so those two terms are
// identical across the whole grid. The generator renders a conversation from
// (epoch, session, turn), and two points' session pools overlap wherever the
// smaller pool's ids exist in the larger, so without a third term WS 1 re-sends
// about a quarter of WS 0.25's conversations byte for byte and reads them back
// out of the replicas' prefix caches. ADR-0004 measured that at a seven-fold
// TTFT difference, and here it would land on the grid's own independent
// variable.
//
// The pools are deliberately different sizes here, which is what a real WS axis
// varies, and the smaller is a subset of the larger's id range.
func TestTwoGridPointsNeverSendEachOthersConversations(t *testing.T) {
	at := []bench.GridPoint{{WorkingSet: 0.25, Skew: 0}, {WorkingSet: 1, Skew: 0}, {WorkingSet: 1, Skew: 1.4}}
	sessions := map[float64]int{0.25: 8, 1: 32}

	bodies := func(p bench.GridPoint) map[string]bool {
		cfg := multiTurnConfig()
		cfg.Sessions = sessions[p.WorkingSet]
		cfg.Skew = p.Skew
		base := newMultiTurn(t, cfg)

		gridOffset, err := bench.GridWorkloadOffset(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		// The offset a real cell of the grid gets: its load level and repetition,
		// plus its pressure point. Every point shares the first two.
		w := bench.Shifted(base, bench.CellWorkloadOffset(gridLoad, 1)+gridOffset)
		seen := map[string]bool{}
		for user := range 8 {
			for turn := range 12 {
				seen[string(w.Next(user, turn).Body)] = true
			}
		}
		return seen
	}

	sent := map[string]bench.GridPoint{}
	for _, p := range at {
		for body := range bodies(p) {
			if other, clash := sent[body]; clash {
				t.Fatalf("%s re-sends a conversation %s already sent, so it measures the replicas' prefix cache rather than prefill", p, other)
			}
			sent[body] = p
		}
	}
}

// Every point of the standard grid gets its own slice, and no two of them
// collide. A collision here would be invisible in every figure it corrupts.
func TestEveryGridPointGetsItsOwnSliceOfTheUserSpace(t *testing.T) {
	seen := map[int]bench.GridPoint{}
	for _, at := range bench.PressureGrid() {
		offset, err := bench.GridWorkloadOffset(at)
		if err != nil {
			t.Fatalf("%s: %v", at, err)
		}
		if offset == 0 {
			t.Errorf("%s takes the unshifted slice, which belongs to a cell that states no working set", at)
		}
		if other, clash := seen[offset]; clash {
			t.Errorf("%s and %s share slice %d", at, other, offset)
		}
		seen[offset] = at
	}
}

// A cell that states no working set is not moved at all. This is what keeps
// every cell of the frozen comparison sending exactly the bytes it sent before:
// the frozen workload names a session count and no capacity to be a ratio
// against, so it states no point and must be untouched by all of this.
func TestACellThatStatesNoWorkingSetIsNotShifted(t *testing.T) {
	offset, err := bench.GridWorkloadOffset(bench.GridPoint{})
	if err != nil {
		t.Fatalf("an unstated point is refused: %v", err)
	}
	if offset != 0 {
		t.Errorf("offset = %d, want 0: the frozen workload's bytes must not move", offset)
	}
	// Skew alone does not state a point either — the frozen workload runs at
	// skew 0 and would still be unstated at any other skew without a capacity.
	if offset, err := bench.GridWorkloadOffset(bench.GridPoint{Skew: 1.4}); err != nil || offset != 0 {
		t.Errorf("offset = %d, err = %v; a point with no working set states nothing and moves nothing", offset, err)
	}
}

// The slice is refused rather than rounded when an axis is finer than it can
// encode. Rounding two points into one would be a silent collision, which is
// the one outcome this arithmetic must never produce.
func TestAnAxisTooFineToEncodeIsRefusedRatherThanRounded(t *testing.T) {
	if _, err := bench.GridWorkloadOffset(bench.GridPoint{WorkingSet: 1.0001, Skew: 0}); err == nil {
		t.Error("a working set finer than the partition can carry was accepted")
	}
	if _, err := bench.GridWorkloadOffset(bench.GridPoint{WorkingSet: 1, Skew: 1.0001}); err == nil {
		t.Error("a skew finer than the partition can carry was accepted")
	}
	// Above the skew span the two axes would encode into one another.
	if _, err := bench.GridWorkloadOffset(bench.GridPoint{WorkingSet: 1, Skew: 10}); err == nil {
		t.Error("a skew above the span the user space is partitioned for was accepted")
	}
}

// Passing -kv-capacity puts a run on the divergence report's working-set axis
// and must not change a byte of what it sends. ADR-0008 promises exactly that,
// and the grid's user-space partition is the thing most able to break it: the
// derived ratio is non-zero the moment a capacity is measured, so a partition
// keyed on it would send the frozen comparison one set of bytes with the flag
// and another without, splitting its own table in two.
//
// The partition is therefore keyed on the CONFIGURED point. This is that
// promise as a test.
func TestMeasuringCapacityPutsARunOnTheAxisWithoutMovingItsBytes(t *testing.T) {
	// The headline workload's shape: a pool stated as a session count.
	counted := multiTurnConfig()
	counted.Sessions = 16

	// The same workload, with the fleet's measured capacity supplied so the
	// divergence report can plot it against WS.
	measured := counted
	measured.CapacityTokens = 629_760

	without := newMultiTurn(t, counted)
	with := newMultiTurn(t, measured)

	// The capacity reaches the axis...
	if bench.OfferedWorkingSet(with) <= 0 {
		t.Fatal("supplying the measured capacity did not put the run on the working set axis")
	}
	if bench.OfferedWorkingSet(without) != 0 {
		t.Fatal("a pool stated as a session count with no capacity landed on the axis anyway")
	}
	// ...and not the partition.
	if got := bench.ConfiguredWorkingSet(with); got != 0 {
		t.Errorf("configured working set = %v, want 0: no grid point was asked for", got)
	}
	offsetWith, err := bench.GridWorkloadOffset(bench.GridPoint{WorkingSet: bench.ConfiguredWorkingSet(with), Skew: bench.OfferedSkew(with)})
	if err != nil {
		t.Fatalf("offset: %v", err)
	}
	if offsetWith != 0 {
		t.Errorf("offset = %d, want 0: measuring the capacity must not move the frozen workload's bytes", offsetWith)
	}

	// And the bytes themselves, which is the promise as the fleet sees it.
	for user := range 8 {
		for turn := range 12 {
			if string(with.Next(user, turn).Body) != string(without.Next(user, turn).Body) {
				t.Fatalf("user %d turn %d sends different bytes once the capacity is measured", user, turn)
			}
		}
	}
	if with.Name() != without.Name() {
		t.Errorf("the workload name changed when the capacity was measured:\n %s\n %s", with.Name(), without.Name())
	}
}

// A sweep asked for a grid point does get its own slice, which is the other
// half of the same rule: an explicit -working-set is a request to be on the
// grid, and being on the grid is what moves the bytes.
func TestAskingForAGridPointDoesMoveTheBytes(t *testing.T) {
	cfg := multiTurnConfig()
	cfg.Sessions, cfg.WorkingSet, cfg.CapacityTokens = 0, 3, 629_760
	asked := newMultiTurn(t, cfg)

	if got := bench.ConfiguredWorkingSet(asked); got != 3 {
		t.Fatalf("configured working set = %v, want the 3 it was told", got)
	}
	offset, err := bench.GridWorkloadOffset(bench.GridPoint{WorkingSet: bench.ConfiguredWorkingSet(asked), Skew: bench.OfferedSkew(asked)})
	if err != nil {
		t.Fatalf("offset: %v", err)
	}
	if offset == 0 {
		t.Error("a sweep asked for WS 3 shares its slice with every other grid point")
	}
	// The shift survives the wrapper the sweep hands every cell.
	if got := bench.ConfiguredWorkingSet(bench.Shifted(asked, bench.WorkloadStride)); got != 3 {
		t.Errorf("the shifted wrapper reports a configured working set of %v, want 3", got)
	}
}
