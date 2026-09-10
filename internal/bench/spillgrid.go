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
// should be keeping.
//
// The load axis carries a second pass. The first ran 2/4/8, read goodput as
// flat across them, and concluded that any escape valve was worth the same as
// any other — which was wrong, and wrong in a way worth recording because the
// measurement looked sound. Goodput here counts crossings of a 24 ms
// inter-token SLO, and all three points sat near 14 ms, so it is a censored
// reading: the tail was separating monotonically underneath it and the
// threshold could not see it. The axis was also bracketed only from below, so
// nothing in it said where the rule stopped working. 12/16/24/32 closed both
// gaps and found the turn.
var (
	KVHighWaterGrid   = []float64{0.70, 0.85, 0.95}
	LoadImbalanceGrid = []float64{2, 4, 8, 12, 16, 24, 32}
)

// Chosen is the grid point the sweep settled on, and the one every later
// measurement of policy 4 runs at unless it is deliberately sweeping this axis.
//
// It lives here rather than in a run script or a ticket comment because it is
// part of what policy 4 *is*, in the same sense ADR-0007 gives the index node
// cap: a table of cells measured at two different spill points is not one
// table, and the value that decides which is a number somebody would otherwise
// retype per run.
//
//	factor   goodput   ITL p95   spill%   hit%
//	  2       29.89     13.67    0.674   95.49
//	  4       29.71     14.43    0.234   95.46
//	  8       29.63     14.83    0.123   95.58
//	 12       29.27     15.26    0.079   95.46
//	 16       28.86     17.13    0.012   95.78
//	 24       23.82     27.52    0.110   95.48
//	 32       14.45     28.73    0.000   95.58
//	off       16.61     30.08    0.000   95.52
//
// Goodput falls monotonically from 2 downward — there is no plateau — and the
// rule is worth +80% against no rule at all. It collapses between 16 and 24 as
// the tail crosses the SLO, and by 32 it is indistinguishable from having no
// rule, because the ratio it tests never exceeded 29 across 10,469 spill-off
// decisions at this rung: a factor of 32 cannot fire, and did not. So the
// benefit comes from declining and not from the flag being set.
//
// 2 wins on every column measured, and the prefix cache hit rate is 95.5% at
// every point, so spilling three times as often as factor 8 costs nothing in
// cache terms — which is the tradeoff the grid existed to price.
//
// Two things this value does not say. The 1–2 range is untested, because
// Spill.Validate refuses a factor below 1 and the curve is still rising as the
// factor falls; the gain from 4 to 2 is +0.18 goodput against a repetition
// spread of 0.95, so what is left is below this measurement's resolution.
// And the rule is rung-dependent at exactly the moments it fires: the fleet
// minimum was genuinely 0 in 16% of decisions, and there factor × 1 is an
// absolute inflight threshold rather than the ratio it is written as.
//
// KVHighWater is zero — the condition is OFF, and that is a finding rather than
// an omission. vllm:kv_cache_usage_perc counts blocks held by *running*
// requests, so it reads the active batch and not cache residency: over 13,658
// rows it is kv = 0.02128 + 0.02135 × inflight at r = 0.973, and an idle
// replica with a full cache reads 0.021. Inverting that, 0.85 needs 38.8
// concurrent requests on one replica and 0.95 needs 43.5, against 32 virtual
// users in the entire fleet — so two of the three levels could not fire, and
// the third caught 7 decisions in 12,000. Any non-zero value here either cannot
// fire or fires on load, which the other condition already covers. See #28.
var Chosen = policy.Spill{KVHighWater: 0, LoadImbalanceFactor: 2}

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
