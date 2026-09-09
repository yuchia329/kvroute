package bench

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/yuchia329/kvroute/internal/policy"
)

// The spill rule's two thresholds are measured on two workload points, one
// each, rather than crossed on a single one.
//
// Not a simplification of idea.md §6's 3x3 grid but a consequence of how the
// workload generator behaves. The two pressures the two conditions answer to
// are dialable independently and *realised* against each other: concentrating
// the draws onto hot conversations means touching fewer distinct conversations,
// so turning skew up turns memory pressure down. MultiTurn.OfferedTokens
// measures the discount — at WS 1 a cell realises 0.97 of its label at α=0 and
// 0.40 at α=1.4, and α=1.4 compresses the whole 32-fold WS axis into a 2.4-fold
// range of realised pressure.
//
// So there is no single point where both conditions fire hard enough to
// separate their thresholds, and a cross-product run at one point would be
// reading one live condition and one dormant one at every cell. Two points,
// each with the other condition switched off, is what the generator permits:
//
//	KV high-water     WS 3, skew 0    replicas evict; load stays even
//	Load imbalance    WS 1, skew 1.4  conversations pile up; caches stay roomy
//
// Switching the other condition off at each point — a zero threshold disables
// it, see policy.Spill — is what makes each row single-factor. It also disposes
// of the interaction that made crossing them attractive in the first place: a
// high-water mark that spills often would leave less load imbalance for the
// second condition to find, and here there is no second condition running to
// find any.
const (
	// KVPressureWorkingSet is the WS point the high-water mark is measured at.
	//
	// 3 rather than 8 because a cell of 4,864 requests makes 1,216 session
	// visits and cannot touch a pool of 2,952 whatever it is labelled — WS 8
	// realises about 2.7 — so 3 is the practical ceiling of the axis at this
	// cell length rather than a step below the top of it.
	KVPressureWorkingSet = 3.0
	// KVPressureSkew is uniform, so that nothing but memory pressure moves.
	KVPressureSkew = 0.0

	// LoadImbalanceWorkingSet is WS 1, where the fleet holds its sessions and
	// the caches stay roomy enough that the high-water mark has nothing to fire
	// on even if it were enabled.
	LoadImbalanceWorkingSet = 1.0
	// LoadImbalanceSkew is the top of idea.md §5's skew axis, where a handful of
	// conversations take most of the draws and several virtual users hold the
	// same conversation at once. That simultaneous piling is the load imbalance
	// the condition exists to answer: session-sticky hashing sends every one of
	// those users to the replica the conversation hashed to.
	LoadImbalanceSkew = 1.4
)

// KVHighWaterGrid and LoadImbalanceGrid are the levels each threshold is
// measured at.
//
// Spread rather than clustered, because a first pass is looking for which end
// of an axis the answer is at and not refining a value it already has. Where
// the useful region turns out to be is a question the run answers: every row
// records the KV utilization and inflight its decision was weighed against, so
// the second pass can be cut against the distribution the first one saw rather
// than against another guess.
//
// The load levels start at 2 rather than at 1.5. A cache hit on this workload
// is worth a great deal of queueing — the concurrency-1 cells put a full
// prefill at roughly 285 ms of TTFT against a per-request queueing cost in the
// low tens of milliseconds — so the break-even imbalance is large, and levels
// clustered near 1.5 would spend the run in a region that declines matches it
// should be keeping. 8 is included so the top of the range is a threshold that
// nearly never fires, which is the control the other two are read against.
var (
	KVHighWaterGrid   = []float64{0.70, 0.85, 0.95}
	LoadImbalanceGrid = []float64{2, 4, 8}
)

// KVHighWaterSweep is the high-water mark measured alone, at
// KVPressureWorkingSet and KVPressureSkew. The load condition is off at every
// point, so nothing but the mark can decline a match.
//
// The zero Spill leads it: a run of policy 4 with no spill rule at all, at the
// same workload point, which is the reference the three thresholds are read
// against. The four-policy comparison's own prefix_affinity cells cannot serve
// as that reference because they run at the frozen workload — WS 1, skew 0 —
// and a row measured under different pressure is not a baseline.
func KVHighWaterSweep() []policy.Spill {
	points := []policy.Spill{{}}
	for _, mark := range KVHighWaterGrid {
		points = append(points, policy.Spill{KVHighWater: mark})
	}
	return points
}

// LoadImbalanceSweep is the imbalance factor measured alone, at
// LoadImbalanceWorkingSet and LoadImbalanceSkew, led by the same spill-off
// reference.
func LoadImbalanceSweep() []policy.Spill {
	points := []policy.Spill{{}}
	for _, factor := range LoadImbalanceGrid {
		points = append(points, policy.Spill{LoadImbalanceFactor: factor})
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
