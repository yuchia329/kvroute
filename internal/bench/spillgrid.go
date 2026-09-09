package bench

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/yuchia329/kvroute/internal/policy"
)

// KVHighWaterGrid and LoadImbalanceGrid are the two axes of the tunable sweep:
// the KV utilizations and the load multiples the spill rule is measured at.
//
// idea.md §6 budgets this as a 3x3 grid at a single concurrency, which is where
// the affinity-versus-balance tradeoff stops being an assertion and becomes a
// curve. Three points per axis is what that budget buys — 27 cells with three
// repetitions, about three GPU-hours — and the points are spread rather than
// clustered, because the first pass is looking for which end of each axis the
// answer is at, not refining a value it already has.
//
// The KV points bracket the pressure the fleet actually reaches. Below 0.70 a
// replica has cache room and declining a match there forfeits it for nothing;
// above 0.90 the engine is already preempting on its own, which is a different
// mechanism at a different layer and not something the router can spill its way
// out of. The load points start at 1.5 because a factor below 1 would decline
// the best match for replicas carrying more work than it, and end at 3.0
// because a replica three times as loaded as the fleet's quietest is imbalanced
// by any reading.
//
// These are grid points rather than defaults. Nothing here is a claim about the
// right threshold: which of the nine the run is written up under is decided by
// the resulting table, and until that table exists the router's own thresholds
// stay off.
var (
	KVHighWaterGrid   = []float64{0.70, 0.80, 0.90}
	LoadImbalanceGrid = []float64{1.5, 2.0, 3.0}
)

// SpillGrid is every point of the tunable sweep, in the order the cells run.
//
// The full cross-product, because the two thresholds are not independent in
// their effect even though they are separate conditions: a high-water mark that
// spills often leaves less load imbalance for the second condition to find, so
// a sweep that moved one axis while holding the other at a single value would
// report an interaction as a property of the axis it happened to move.
func SpillGrid() []policy.Spill {
	points := make([]policy.Spill, 0, len(KVHighWaterGrid)*len(LoadImbalanceGrid))
	for _, kv := range KVHighWaterGrid {
		for _, load := range LoadImbalanceGrid {
			points = append(points, policy.Spill{KVHighWater: kv, LoadImbalanceFactor: load})
		}
	}
	return points
}

// FormatSpill renders a grid point as the spec the commands take, so a flag's
// default can be the package's own value rather than a second copy of it that
// drifts.
func FormatSpill(s policy.Spill) string {
	return strconv.FormatFloat(s.KVHighWater, 'g', -1, 64) + "/" + strconv.FormatFloat(s.LoadImbalanceFactor, 'g', -1, 64)
}

// ParseSpill reads a grid point written as "kv/load", and refuses one the
// router would not run.
//
// Validated here rather than only at the router, because this is where a cell's
// label comes from: a sweep that accepted an impossible threshold would write
// the label onto every cell it produced and fail at the router afterwards, by
// which point the directory names a grid point that cannot exist.
func ParseSpill(spec string) (policy.Spill, error) {
	kv, load, found := strings.Cut(strings.TrimSpace(spec), "/")
	if !found {
		return policy.Spill{}, fmt.Errorf("bench: a spill grid point is written as <kv-high-water>/<load-imbalance-factor>, got %q", spec)
	}
	highWater, err := strconv.ParseFloat(strings.TrimSpace(kv), 64)
	if err != nil {
		return policy.Spill{}, fmt.Errorf("bench: %q is not a KV high-water mark: %w", kv, err)
	}
	factor, err := strconv.ParseFloat(strings.TrimSpace(load), 64)
	if err != nil {
		return policy.Spill{}, fmt.Errorf("bench: %q is not a load imbalance factor: %w", load, err)
	}
	point := policy.Spill{KVHighWater: highWater, LoadImbalanceFactor: factor}
	if err := point.Validate(); err != nil {
		return policy.Spill{}, err
	}
	return point, nil
}
