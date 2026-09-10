package bench

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The pressure grid is idea.md §6's headline: working set ratio crossed with
// Zipf skew, at one concurrency, for every policy.
//
// It is two-dimensional because the two pressures are physically different and
// act on different parts of the policy. Working set ratio is offered session
// tokens over measured aggregate fleet KV, so it drives eviction and fires the
// spill rule's KV high-water branch. Skew concentrates the draws onto a few
// conversations, so it drives load imbalance and fires the imbalance branch —
// and load imbalance is the pressure session-sticky hashing has no answer to,
// which is the single clearest place a cache-aware policy can beat it.
//
// Holding either axis fixed would sweep the pressure that evicts while leaving
// the pressure that unbalances untested, or the reverse. That is why this is a
// grid and not a line, and it is why the two axes are never collapsed into one
// "pressure" number: the whole result is which of them moved the goodput.
//
// ⚠️ The labels on the WS axis are what a cell was configured for, not what it
// applied. Two separate discounts sit between them, both already recorded
// elsewhere in this repo, and neither is corrected here because correcting
// either would change the workload's name and refuse every cell recorded under
// the old one (ADR-0004, ADR-0007):
//
//   - Skew discounts working set. Concentrating the draws means touching fewer
//     distinct conversations, so at WS 1 a cell realises 0.97 of its label at
//     skew 0 and 0.40 at skew 1.4 — see MultiTurn.ExpectedDistinctSessions,
//     which is the arithmetic, and spillgrid.go, which is why the spill
//     thresholds are measured at two points rather than crossed on one.
//   - A cell of finite length cannot touch a pool larger than its visit count.
//     At the cell durations this grid runs, the top of the axis realises well
//     under its label.
//
// So the top two WS points may apply less distinct pressure than the gap
// between their labels suggests, and the honest reading of a flat top end is
// "these two points did not differ in realised pressure" before it is "cache
// affinity stopped paying". The map prints the axis with its labels and says
// this next to it; the realised draw is countable after the fact from the
// session column of the rows.
var (
	// PressureWorkingSets is the WS axis: offered session tokens over the
	// fleet's measured aggregate KV.
	//
	// The same four points as characterize.WorkingSetPoints, which is where the
	// axis is defined, restated here because internal/characterize imports this
	// package and cannot be imported back. A test in the external test package
	// imports both and fails if the two ever drift, which is the only place that
	// check can live.
	PressureWorkingSets = []float64{0.25, 1, 3, 8}

	// PressureSkews is the skew axis: the Zipf exponent over the session pool,
	// from uniform to heavily concentrated.
	//
	// Zero leads it because a uniform draw is the control the other two are read
	// against: it is the only point on this axis where the working set a cell
	// was configured for is close to the working set it applies.
	PressureSkews = []float64{0.0, 1.0, 1.4}
)

// PressureConcurrency is the single load rung the whole grid runs at.
//
// Single because the grid is 4 policies × 4 WS × 3 skew × 3 repetitions = 144
// cells at roughly seven minutes each, and idea.md §6 budgets it at one
// concurrency precisely because a second would double the most expensive sweep
// in the project.
//
// 32 rather than another rung, for two reasons that point the same way. The
// first is the one the tunable sweep already runs at 32 for: spill only fires
// under pressure, and an idle fleet crosses no high-water mark and has no load
// imbalance to speak of, so a grid run too low would report policy 4 collapsing
// into policy 3 everywhere and would be measuring the rung rather than the
// grid. The second is that the spill thresholds this grid runs with are chosen
// on the tunable sweep at 32 (see spillgrid.go). Running the grid at a
// different rung would apply thresholds at a load they were not chosen at, and
// a threshold is a statement about a distribution of utilizations and inflights
// that the rung moves.
const PressureConcurrency = 32

// GridPoint is one cell of the pressure grid: the pressure a run was
// configured to apply, on both axes at once.
//
// Both axes together, never one alone, because neither is readable without the
// other. Skew decides how much of the session pool a cell of finite length
// actually draws from, so a working set ratio quoted without its skew names a
// pressure the cell may not have applied — which is the whole reason the two
// travel together on the cell record, in the divergence report's bins, and
// here.
//
// It is a coordinate rather than a configuration: the sweep is told a working
// set and a skew as ordinary workload flags, and this is how the cells that
// come back are put on a grid again.
type GridPoint struct {
	WorkingSet float64
	Skew       float64
}

// PressureGrid is every point of the grid, in the order it is swept and
// tabulated: working set ascending, and within each, skew ascending.
//
// One order for both, so the run log, the directory listing and the rows of the
// published map climb the axes the same way. A reader comparing a table against
// the run that produced it should not have to re-sort either.
func PressureGrid() []GridPoint {
	points := make([]GridPoint, 0, len(PressureWorkingSets)*len(PressureSkews))
	for _, ws := range PressureWorkingSets {
		for _, skew := range PressureSkews {
			points = append(points, GridPoint{WorkingSet: ws, Skew: skew})
		}
	}
	return points
}

// GridPointOf is the point a cell was recorded at.
//
// Read off the cell's own flattened columns rather than parsed out of its
// workload name, for the reason those columns exist: a report that had to parse
// a name to find its own axis would break the first time the name gained a
// field.
func GridPointOf(cell Cell) GridPoint {
	return GridPoint{WorkingSet: cell.WorkingSet, Skew: cell.Skew}
}

// Stated reports whether this point names a working set at all.
//
// A cell whose workload stated no WS point carries zero here, and zero is an
// absence rather than the bottom of the axis: the fixed workload has no session
// pool, and a multi-turn pool given as a plain session count has no measured
// capacity to be a ratio against. Such a cell is not on the grid, and averaging
// it into a grid point would be reporting an unknown pressure as a known one.
//
// The fix when this is unexpectedly false is to pass the sweep -kv-capacity:
// the ratio is then derived from the session pool without changing a byte of
// what the cell sends, so the workload name — and every comparison that rests
// on it — is untouched.
func (p GridPoint) Stated() bool { return p.WorkingSet > 0 }

// Key is the point as a directory name: `ws3-skew1.4`.
//
// Each point of the grid is swept into its own directory because each sends a
// different workload, and Compare refuses to put two workloads in one table. A
// name rather than an index, so a half-finished grid can be read by looking at
// it.
func (p GridPoint) Key() string {
	return "ws" + formatAxis(p.WorkingSet) + "-skew" + formatAxis(p.Skew)
}

// String is the point as a table cell reads it.
func (p GridPoint) String() string {
	if !p.Stated() {
		return "WS unstated"
	}
	return fmt.Sprintf("WS %s, skew %s", formatAxis(p.WorkingSet), formatAxis(p.Skew))
}

// formatAxis renders an axis value the way both the directory name and the
// table do, so a point cannot be called `ws0.25` in one and `WS 0.250000` in
// the other.
func formatAxis(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// ParseGridPoint reads a point written as "<working-set>/<skew>", the spec the
// commands take it as.
//
// Here rather than in each command, for the reason ParseSpill is here: two
// parsers for one format are two places for a stray space or a negative to be
// accepted by one and rejected by the other.
func ParseGridPoint(spec string) (GridPoint, error) {
	ws, skew, split := strings.Cut(spec, "/")
	if !split {
		return GridPoint{}, fmt.Errorf("bench: grid point %q is not <working-set>/<skew>, e.g. 3/1.4", spec)
	}
	var p GridPoint
	var err error
	if p.WorkingSet, err = parseAxis(ws, "working set ratio"); err != nil {
		return GridPoint{}, err
	}
	if p.Skew, err = parseAxis(skew, "skew"); err != nil {
		return GridPoint{}, err
	}
	if p.WorkingSet <= 0 {
		// Zero is how a cell records that it stated no working set at all, so a
		// point deliberately written as zero would be indistinguishable from one
		// whose sweep was never given the capacity to be a ratio against.
		return GridPoint{}, fmt.Errorf("bench: a working set ratio of %v is not a point on the axis: it is how a cell records that it stated none", p.WorkingSet)
	}
	return p, nil
}

// FormatGridPoint renders a point into the spec the commands take, so a flag's
// default can be the package's own value rather than a second copy that drifts.
func FormatGridPoint(p GridPoint) string {
	return formatAxis(p.WorkingSet) + "/" + formatAxis(p.Skew)
}

func parseAxis(field, what string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(field), 64)
	if err != nil {
		return 0, fmt.Errorf("bench: %s %q is not a number", what, field)
	}
	if v < 0 {
		return 0, fmt.Errorf("bench: %s is %v, and it runs from 0 upward", what, v)
	}
	return v, nil
}

// The grid needs its own slice of the workload's user space, per point, and
// this is the arithmetic for it.
//
// CellWorkloadOffset separates cells by repetition and load level, which was
// every axis a sweep had when it was written. The grid adds one it does not know
// about, and the omission is not cosmetic: every point of the grid runs at the
// same concurrency and the same repetitions, so all twelve compute the identical
// offset. The generator renders a conversation from (epoch, session, turn), the
// epoch comes from that offset, and the session pools of two points overlap
// wherever the smaller pool's ids exist in the larger — so WS 1 following
// WS 0.25 re-sends about a quarter of its conversations byte for byte.
//
// The second point then reads those prompts back out of the replicas' prefix
// caches instead of prefilling them, which is the contamination ADR-0004 exists
// to prevent and which it measured at a seven-fold TTFT difference. Here it
// would land squarely on the independent variable: the WS axis is the grid's
// whole argument, and the corruption is order-dependent, so a point's numbers
// would depend on which points ran before it. Worse, it flatters every
// mechanism column at once — hit rates up, prefill down — so the validity
// evidence would look healthy while the axis underneath it meant nothing.
//
// checkWorkloadPartition cannot catch this. It checks one sweep invocation's
// cells against each other, and each grid point is its own invocation with a
// single load: there is no clash inside any one of them.
//
// So a stated pressure point shifts the user space again, and two points never
// share a slice. A cell that states no working set is unmoved — offset zero —
// which is what keeps every cell of the frozen comparison sending exactly the
// bytes it sent before.
const (
	// gridAxisScale is how finely a point's axes are read into its slice: two
	// decimal places, which covers every value either axis uses (WS 0.25, skew
	// 1.4) with room to spare.
	gridAxisScale = 100
	// gridSkewSpan bounds the skew axis inside a working set's block, so the two
	// axes cannot encode into one another. Skew runs to 1.4 and this allows 10.
	gridSkewSpan = 10 * gridAxisScale
)

// GridWorkloadOffset is the additional slice of the workload's user space this
// grid point sends from, on top of the offset its load level and repetition
// already earn it.
//
// Zero for a point that states no working set, so nothing recorded under the
// frozen workload moves. Injective for every value either axis takes to two
// decimal places, so no two points of any grid — the standard one or a
// deliberately off-axis exploration — can land on one slice.
//
// It reports an error rather than silently truncating a value it cannot encode.
// A collision here is invisible in every figure it corrupts, so the one thing
// this must not do is round two points into one and carry on.
func GridWorkloadOffset(at GridPoint) (int, error) {
	if !at.Stated() {
		return 0, nil
	}
	ws, err := axisUnits(at.WorkingSet, "working set ratio")
	if err != nil {
		return 0, err
	}
	skew, err := axisUnits(at.Skew, "skew")
	if err != nil {
		return 0, err
	}
	if skew >= gridSkewSpan {
		return 0, fmt.Errorf("bench: skew %v is above the %d this grid's user space is partitioned for",
			at.Skew, gridSkewSpan/gridAxisScale)
	}
	// Every stated point lands far above the blocks the repetition and load axes
	// occupy — the smallest is WS 0.01, at 1,000 blocks of its own — so the two
	// partitions cannot overlap.
	return (ws*gridSkewSpan + skew) * WorkloadStride, nil
}

// axisUnits reads an axis value into whole units of gridAxisScale, refusing one
// that would need finer precision than the slice can carry.
func axisUnits(v float64, what string) (int, error) {
	scaled := v * gridAxisScale
	units := int(math.Round(scaled))
	if math.Abs(scaled-float64(units)) > 1e-9 {
		return 0, fmt.Errorf("bench: %s %v is finer than the %d divisions a grid point's user space is partitioned into, "+
			"so two points near it could not be told apart; round it or widen gridAxisScale", what, v, gridAxisScale)
	}
	return units, nil
}
