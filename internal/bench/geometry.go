package bench

import "fmt"

// The turn geometry is two axes the project has never varied: how many turns a
// conversation runs for, and how much new user text each turn contributes.
//
// Every cell in this repo ran at one geometry — four turns of 448 tokens, which
// multiplies out to the 2,048-token session the WS axis is stated in — and the
// pressure grid's own README names the two of them as the open axes: "the
// adaptive-router question has no region to work in on these two axes; the open
// axes are turns per session and prompt length." #38 sweeps one and #39 the
// other, so this is where a point on either becomes a thing a cell can carry.
//
// It is one type for both because the two are one physical quantity split in
// half. A session's KV footprint is turns × (prompt + output), so a cell that
// stated its turn count without its prompt size would name a pressure it may
// not have applied — the same argument that keeps working set and skew on one
// GridPoint rather than on two.

// Geometry is the turn geometry a cell's workload was configured with.
//
// A coordinate rather than a configuration, like GridPoint: the sweep is told a
// turn count and a prompt size as ordinary workload flags, and this is how the
// cells that come back are put back on an axis.
type Geometry struct {
	TurnsPerSession int
	PromptTokens    int
}

// TurnGeometry is implemented by a workload that has conversations to state a
// geometry for.
//
// An optional interface for the reason PressurePoint is one: the fixed workload
// is single-turn requests with no history, so it has no turn count, and a
// method it had to implement would report that absence as zero turns — an
// unreachable point on the axis rather than a workload that is not on it.
type TurnGeometry interface {
	TurnsPerSession() int
	PromptTokens() int
	// Sessions is the pool the trace actually drew from, which is not always the
	// pool it was configured with: a cell given a working set ratio has its pool
	// derived from the measured fleet capacity. #39 rescales that pool as it
	// moves along the turns axis, and the rescaling is only auditable if the
	// realised count is on the record beside the turn count that drove it.
	Sessions() int
}

// OfferedGeometry is the turn geometry a workload runs, or the zero Geometry
// when it has none.
//
// Asked through a function rather than at each call site, for the reason
// OfferedWorkingSet is: the sweep hands every cell a Shifted workload, and a
// wrapper that did not forward the question would report every cell as stating
// nothing — leaving both new axes as empty columns that no test failed over.
func OfferedGeometry(w Workload) Geometry {
	if g, states := w.(TurnGeometry); states {
		return Geometry{TurnsPerSession: g.TurnsPerSession(), PromptTokens: g.PromptTokens()}
	}
	return Geometry{}
}

// OfferedSessions is how many conversations a workload's pool holds, or zero
// when it has no pool.
func OfferedSessions(w Workload) int {
	if g, states := w.(TurnGeometry); states {
		return g.Sessions()
	}
	return 0
}

// Stated reports whether this names a geometry at all.
//
// Both halves, because half a geometry is not a point: a turn count without a
// prompt size does not say what a session weighs, and the axes are swept
// against session footprint. A cell recorded before these columns existed
// carries zero on both, and zero is an absence rather than the bottom of either
// axis — the generator refuses a turn count or a prompt size of zero, so no
// cell has ever run at one.
func (g Geometry) Stated() bool { return g.TurnsPerSession > 0 && g.PromptTokens > 0 }

// Default reports whether this is the geometry every published cell ran at.
//
// It is the pivot the user-space partition turns on: the frozen comparison must
// keep sending exactly the bytes it sent before, so the point it sits on is the
// one point of these axes that moves nothing.
//
// An unstated geometry is not this one. The two are both left where they are,
// but for different reasons — one is the axis's origin and the other is not on
// the axis at all — and a predicate that answered yes to both would make a
// half-stated geometry indistinguishable from the published point.
func (g Geometry) Default() bool {
	return g.TurnsPerSession == DefaultTurnsPerSession && g.PromptTokens == DefaultPromptTokens
}

func (g Geometry) String() string {
	if !g.Stated() {
		return "geometry unstated"
	}
	return fmt.Sprintf("%d turns of %d tokens", g.TurnsPerSession, g.PromptTokens)
}

// Two cells at two geometries must not send one prompt in common, and by
// default they would.
//
// This is the same failure the pressure grid partitions its user space against,
// and here it is worse rather than milder. The generator renders a turn's text
// as filler(seed, tokens) — a byte stream from a seed, cut to length — so the
// same (epoch, session, turn) at 112 prompt tokens is a byte-for-byte *prefix*
// of the same turn at 1,792. The turns axis is worse still: a 16-turn cell
// resends a 4-turn cell's whole first four turns verbatim, because the content
// seed is keyed on the turn index and nothing else.
//
// So the second point of either axis would read its opening prefix out of the
// replicas' caches instead of prefilling it — the contamination ADR-0004
// measured at a seven-fold TTFT difference, landing squarely on the independent
// variable, and flattering every mechanism column at once while it did.
// checkWorkloadPartition cannot catch it: each point is its own sweep
// invocation, and there is no clash inside any one of them.
//
// The partition is therefore keyed on the CONFIGURED geometry, and the default
// geometry is keyed to zero. That is what keeps every cell of the frozen
// comparison — and every cell of the pressure grid, which ran at the default —
// sending exactly the bytes it sent before, on the same reasoning ADR-0008
// applies to -kv-capacity.
const (
	// geometryPromptSpan bounds the prompt axis inside a turn count's block, so
	// the two halves of the geometry cannot encode into one another. The axis
	// this project sweeps tops out at 1,792, and this allows 32,767 — above any
	// prompt a 3090 will prefill inside an SLO.
	geometryPromptSpan = 1 << 15
	// geometryTurnSpan bounds the turn axis. #39 sweeps to 16; this allows 255,
	// which is more conversation than the generator's history growth leaves room
	// for at any prompt size.
	geometryTurnSpan = 1 << 8
	// geometryRepetitionSpan bounds the repetitions a geometry point steps over.
	// The sweep adds a cell's own offset to the two partition offsets, and a
	// cell's is repetition × 2 × LoadLevelsPerDriver blocks, so the geometry
	// block has to clear the repetition axis as well as the pressure grid. Cells
	// run at three repetitions and this allows 4,096, which is more runs of one
	// cell than the box has hours for.
	geometryRepetitionSpan = 1 << 12
	// geometryBlockSpan is how many blocks of the user space a geometry point
	// steps over, and it is what keeps these axes disjoint from the pressure
	// grid's and the cell's rather than interleaved with them. It is the first
	// block above everything the other two partitions can reach together: the
	// grid is bounded at gridWorkingSetSpan × gridSkewSpan blocks and a cell at
	// geometryRepetitionSpan × 2 × LoadLevelsPerDriver.
	geometryBlockSpan = gridWorkingSetSpan*gridSkewSpan + geometryRepetitionSpan*2*LoadLevelsPerDriver
)

// GeometryWorkloadOffset is the additional slice of the workload's user space
// this geometry sends from, on top of the offsets its pressure point, load
// level and repetition already earn it.
//
// Zero at the default geometry and zero for an unstated one, so nothing already
// recorded moves. Injective for every geometry inside the spans above, and a
// multiple of the whole block range the pressure grid can occupy, so a geometry
// point and a pressure point can never add up to another pair's slice.
//
// It reports an error rather than silently truncating a geometry it cannot
// encode, for the reason GridWorkloadOffset does: a collision here is invisible
// in every figure it corrupts.
func GeometryWorkloadOffset(g Geometry) (int, error) {
	// A workload with no conversations is on no point of either axis, so it is
	// not moved. A workload that named one half and not the other is refused
	// rather than left there: it would otherwise send the published cells' own
	// bytes while carrying a turn count or a prompt size that says it did not.
	if g == (Geometry{}) {
		return 0, nil
	}
	if !g.Stated() {
		return 0, fmt.Errorf("bench: a geometry of %d turns of %d tokens names one half and not the other, "+
			"so it has no slice of the user space of its own; state both or neither",
			g.TurnsPerSession, g.PromptTokens)
	}
	if g.Default() {
		return 0, nil
	}
	if g.TurnsPerSession >= geometryTurnSpan {
		return 0, fmt.Errorf("bench: %d turns per session is above the %d this axis's user space is partitioned for",
			g.TurnsPerSession, geometryTurnSpan)
	}
	if g.PromptTokens >= geometryPromptSpan {
		return 0, fmt.Errorf("bench: a %d-token turn is above the %d this axis's user space is partitioned for",
			g.PromptTokens, geometryPromptSpan)
	}
	units := g.TurnsPerSession*geometryPromptSpan + g.PromptTokens
	return units * geometryBlockSpan * WorkloadStride, nil
}
