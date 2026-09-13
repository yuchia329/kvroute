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
//	Honoured low-water   WS 3, skew 0    replicas evict; load stays even
//	Load imbalance       WS 1, skew 1.4  conversations pile up; caches stay roomy
//
// Switching the other condition off at each point — a zero threshold disables
// it, see policy.Spill — is what makes each row single-factor. It also disposes
// of the interaction that made crossing them attractive in the first place: a
// low-water mark that spills often would leave less load imbalance for the
// second condition to find, and here there is no second condition running to
// find any.
const (
	// KVPressureWorkingSet is the WS point the honoured low-water mark is
	// measured at.
	//
	// 3 rather than 8 because a cell of 4,864 requests makes 1,216 session
	// visits and cannot touch a pool of 2,952 whatever it is labelled — WS 8
	// realises about 2.7 — so 3 is the practical ceiling of the axis at this
	// cell length rather than a step below the top of it.
	KVPressureWorkingSet = 3.0
	// KVPressureSkew is uniform, so that nothing but memory pressure moves.
	KVPressureSkew = 0.0

	// LoadImbalanceWorkingSet is WS 1, where the fleet holds its sessions and
	// nothing is evicted, so the low-water mark has nothing to fire on even if
	// it were enabled.
	LoadImbalanceWorkingSet = 1.0
	// LoadImbalanceSkew is the top of idea.md §5's skew axis, where a handful of
	// conversations take most of the draws and several virtual users hold the
	// same conversation at once. That simultaneous piling is the load imbalance
	// the condition exists to answer: session-sticky hashing sends every one of
	// those users to the replica the conversation hashed to.
	LoadImbalanceSkew = 1.4
)

// LoadImbalanceGrid is the levels the load threshold is measured at.
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
var LoadImbalanceGrid = []float64{2, 4, 8, 12, 16, 24, 32}

// HitRateLowWaterGrid is the levels the residency threshold is measured at,
// cut against the observed range in runs/spill-signal on 2026-09-12.
//
// #16 swept 0.70/0.85/0.95 against the old gauge and two of the three could not
// fire at the concurrency they ran at — a level is only a level if the signal
// reaches it, and nothing in that sweep checked. So these were left empty until
// a run had said where the rate actually sits, which is what the observing pass
// at KVPressureWorkingSet and KVPressureSkew was for: 4,022 decisions, spill
// off, the rate recorded on every row and routed on by nothing.
//
// What it saw, over the 3,955 rows that carried a reading:
//
//	min 0.014   p10 0.560   p50 0.662   p90 0.735   max 0.908
//
// Every level below is therefore reachable in the sense #28 asks for — the
// signal was observed beneath each of them — but reachability alone is the
// weaker half of the question. #16's 0.70 was reachable too and still caught
// 0.14% of decisions. What a level has to do is decline matches at a rate that
// separates it from its neighbours, and it has to decline them somewhere: the
// residency branch excludes every replica equally under the mark (see
// Spill.targets), so when the whole fleet is under, the set is empty and the
// match is kept. A level whose firings mostly land there measures the rule
// declining to act.
//
// Both, over the 3,510 decisions that carried a prefix match to decline:
//
//	mark   declines   of those, a target existed   effective
//	0.55      8.4%              61.6%                 5.2%
//	0.62     29.7%              78.6%                23.3%
//	0.70     72.1%              58.9%                42.5%
//
// So 0.55/0.62/0.70 spans light to heavy at roughly even steps in the rate that
// matters, and every point spends the majority of its firings actually moving a
// request. The ends are where they are for a reason. Below 0.55 the signal is
// into its cold-start tail — the readings under 0.50 are 5.5% of the run and
// cluster at the first seconds of each repetition — and 0.50 declines 5.8% of
// matches, too few to move a goodput number. Above 0.70 the no-op share takes
// over: 0.72 declines 84.8% and finds a target for 45.3% of them, 0.75 declines
// 93.8% and finds one for 23.7%, so those levels increasingly price the empty
// target set rather than the spill.
//
// This is the discipline the load axis arrived at the long way round, and the
// one ADR-0006 applies to the index's bounds: a threshold nobody has seen the
// signal's range for is not a grid point, it is a guess with a decimal place.
var HitRateLowWaterGrid = []float64{0.55, 0.62, 0.70}

// HitRateObserved is the distribution the grid above was cut from, kept so a
// test can check the levels against the run rather than against a comment, and
// so a later pass can say whether the fleet still behaves this way.
//
// runs/spill-signal, 2026-09-12: WS 3, skew 0, 32 users, 3 repetitions, spill
// off, five replicas.
var HitRateObserved = Range{
	Readings: 3955,
	Unread:   67,
	Min:      0.014,
	P10:      0.560,
	P50:      0.662,
	P90:      0.735,
	Max:      0.908,
}

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
// THIS POINT IS BOUNDED BELOW, and #31 measured where. Everything above is a
// closed-loop result at 32 users. Driven open-loop at 6 requests per second on
// the same five replicas, the fleet minimum was 0 on 96.4% of decisions and
// never once above 1 across 2,361 of them, so the comparison was not a ratio at
// any point in the run — and there the rule is pure cost:
//
//	off        5.99 goodput/s     0.7 SLO misses per 601
//	2x min     5.46               54.0
//
// Goodput falls monotonically with how often the condition fires, at every
// setting measured (LoadDenominatorGrid), so no point on that axis recovers it:
// what is worth +80% at c32 costs 8.8% here. The mechanism is the fleet's own
// utilisation rather than the driver — at 32 users the replicas are saturated
// and moving work off a buried one pays for the prefill it costs, and at 6
// req/s they are holding 1.2 requests each, nothing is queueing, and declining a
// match buys a prefill that nothing was waiting for.
//
// So this value describes policy 4 at a loaded fleet. A run whose fleet is not
// loaded should carry the spill-off point instead, as #19's re-run does, and
// RunSweep warns when an open-loop sweep runs this rule against the minimum.
//
// HitRateLowWater is zero — the residency condition is OFF, and that is a
// finding about the signal it used to read rather than about the condition.
// Through #16 that branch read vllm:kv_cache_usage_perc, which counts blocks
// held by *running* requests: over 13,658 rows it came to
// kv = 0.02128 + 0.02135 × inflight at r = 0.973, and an idle replica with a
// full cache read 0.021. Inverting that, its 0.85 level needed 38.8 concurrent
// requests on one replica and 0.95 needed 43.5, against 32 virtual users in the
// entire fleet — so two of the three levels could not fire, and the third
// caught 7 decisions in 12,000.
//
// The branch now reads the engines' own prefix cache hit rate, per replica over
// a moving window (ADR-0011), which responds to eviction because it measures
// what eviction leaves behind. HitRateLowWaterGrid was cut against that signal's
// observed range, and #18's residency arm then swept it: the mark alone with the
// load condition off, at all three levels, across the pressure grid's twelve
// points. Every level costs goodput at every point, and at skew 0 above WS 0.25
// the gentlest of them loses 74-85% against this factor.
//
// So the condition stays off on the evidence of a sweep rather than for want of
// one. What the sweep found is a loop the rule creates rather than a threshold
// set wrongly: declining a match because a replica is evicting sends the request
// to a replica that never held the conversation, which is a certain miss, so the
// fleet's hit rate falls and more decisions fall under the mark. At WS 1, skew 0
// the fleet read 77.5% under this factor and 45.5% at mark 0.55, where 44.1% of
// decisions spilled. Tightening the mark does not tighten the rule either: a
// residency spill excludes every replica also under the mark, and on a fleet
// evicting everywhere that set is empty and the match is kept, so the strictest
// level acts least. The signal is not what failed -- it answers to the working
// set axis where the load condition answers away from it, which is the
// separability #18 asked for -- and only the rule built on it is disabled.
var Chosen = policy.Spill{HitRateLowWater: 0, LoadImbalanceFactor: 2}

// HitRateLowWaterSweep is the residency mark measured alone, at
// KVPressureWorkingSet and KVPressureSkew. The load condition is off at every
// point, so nothing but the mark can decline a match.
//
// The zero Spill leads it: a run of policy 4 with no spill rule at all, at the
// same workload point, which is the reference every threshold is read against.
// The four-policy comparison's own prefix_affinity cells cannot serve as that
// reference because they run at the frozen workload — WS 1, skew 0 — and a row
// measured under different pressure is not a baseline.
//
// It returns the reference plus one point per level in HitRateLowWaterGrid,
// which was empty until the observing pass of 2026-09-12 cut it: see
// HitRateLowWaterGrid for the range the levels came from.
func HitRateLowWaterSweep() []policy.Spill {
	points := []policy.Spill{{}}
	for _, mark := range HitRateLowWaterGrid {
		points = append(points, policy.Spill{HitRateLowWater: mark})
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
//
// The denominator is written out whenever the load condition is on, even where
// it is the settled one. A point is a label a cell carries, and "2" without it
// names two different rules — the ambiguity #31 spent a chaos arm discovering.
// A disabled load condition has no denominator to name and is written as the
// two-part spec every run before #31 used.
func FormatSpill(s policy.Spill) string {
	spec := strconv.FormatFloat(s.HitRateLowWater, 'g', -1, 64) + "/" + strconv.FormatFloat(s.LoadImbalanceFactor, 'g', -1, 64)
	if s.LoadImbalanceFactor > 0 {
		spec += "/" + s.LoadDenominatorName()
	}
	return spec
}

// ParseSpill reads a grid point written as "honoured/load" or
// "honoured/load/denominator", and refuses one the router would not run.
//
// The denominator is optional and absent means the minimum, so every spec
// written before #31 — in a box script, a measurement's evidence, a resumed
// sweep's cells — still names the point it named when it was written.
//
// Validated here rather than only at the router, because this is where a cell's
// label comes from: a sweep that accepted an impossible threshold would write
// the label onto every cell it produced and fail at the router afterwards, by
// which point the directory names a grid point that cannot exist.
func ParseSpill(spec string) (policy.Spill, error) {
	honoured, rest, found := strings.Cut(strings.TrimSpace(spec), "/")
	if !found {
		return policy.Spill{}, fmt.Errorf("bench: a spill grid point is written as <hit-rate-low-water>/<load-imbalance-factor> or <hit-rate-low-water>/<load-imbalance-factor>/<min|mean>, got %q", spec)
	}
	lowWater, err := strconv.ParseFloat(strings.TrimSpace(honoured), 64)
	if err != nil {
		return policy.Spill{}, fmt.Errorf("bench: %q is not an honoured low-water mark: %w", honoured, err)
	}
	load, denominator, named := strings.Cut(rest, "/")
	factor, err := strconv.ParseFloat(strings.TrimSpace(load), 64)
	if err != nil {
		return policy.Spill{}, fmt.Errorf("bench: %q is not a load imbalance factor: %w", load, err)
	}
	point := policy.Spill{HitRateLowWater: lowWater, LoadImbalanceFactor: factor}
	if named {
		switch strings.TrimSpace(denominator) {
		case "min":
		case "mean":
			point.MeanInflightDenominator = true
		default:
			return policy.Spill{}, fmt.Errorf("bench: %q is not a denominator for the load imbalance factor: it is held against the fleet's inflight minimum (\"min\") or its mean (\"mean\")", denominator)
		}
	}
	if err := point.Validate(); err != nil {
		return policy.Spill{}, err
	}
	return point, nil
}
