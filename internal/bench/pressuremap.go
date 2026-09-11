package bench

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/yuchia329/kvroute/internal/policy"
)

// PressureMap is the project's headline figure: what each policy did across the
// working set × skew grid, and where the difference between session affinity
// and prefix affinity was big enough to be a difference at all.
//
// It is a grid of comparisons rather than one comparison over every cell, and
// that is forced rather than chosen. Each grid point offers a different
// workload — a different session pool, drawn with a different concentration —
// and Compare refuses to put two workloads in one table, because the difference
// between two policies measured on two workloads includes the difference
// between the workloads. So the comparison happens inside a point, where both
// policies sent the same bytes, and the map is what those comparisons look like
// laid out on the two axes.
//
// The map is the deliverable under either outcome. A grid where prefix affinity
// wins somewhere says where cache-aware routing pays; a grid where it wins
// nowhere says the crossover is outside this fleet's reach, which idea.md §0
// records as publishable — but only if the mechanism evidence says the run
// actually applied the pressure it claims to have. That is what Validity is
// for, and why a flat map with flat validity is reported as a workload to fix
// rather than as a result.
type PressureMap struct {
	// SLO is the threshold every cell in every point was judged against.
	// Goodput is defined by it, so a map across two of them is not a map.
	SLO SLO
	// Baseline and Challenger are the pair the headline delta is between: by
	// default session affinity, the policy this project has to beat, and prefix
	// affinity, the policy that claims to; #24 draws the same map for prefix
	// affinity against exact residency. Named rather than assumed from position,
	// because a map missing either still renders its other tables and has to say
	// which one it could not draw.
	Baseline   string
	Challenger string
	// Policies is every policy any point measured, in the comparison's order.
	Policies []string
	// Points are the grid points that produced a comparison, in grid order.
	Points []GridComparison
	// Refused are the grid points that could not be compared, with the reason —
	// a point where only one policy ran, or where every cell was thrown away.
	// Listed rather than dropped: a hole in the headline figure is a result
	// about the run, and a map that silently omitted its holes would look
	// complete.
	Refused []RefusedPoint
	// Unstated is how many cells named no working set, and so are on no point of
	// the grid. Almost always one cause: a sweep that was not given
	// -kv-capacity, so its pool has no denominator to be a ratio against.
	Unstated int
	// Excluded is every cell left out of every figure, with the reason, pooled
	// from the points' own comparisons. §6 discards flagged and unclean cells
	// and re-runs them rather than averaging them in.
	Excluded []string
	// Surfaced is every cell kept in the figures although it dropped or failed more of its
	// requests than the threshold allows, pooled from the points' comparisons. The
	// figures resting on one are marked.
	Surfaced []string

	// Spill is the spill configuration the cells ran under, read off them rather
	// than assumed.
	//
	// The map has to know it because one of #18's criteria — that memory
	// pressure and load imbalance are separable, since they drive different
	// branches of the rule — is only answerable if both branches were switched
	// on. bench.Chosen turns the KV branch off, and a separability table drawn
	// without saying so would report a column of zeros as a finding about the
	// axis rather than as a condition that was never armed.
	Spill policy.Spill
	// SpillVaried is true when the cells did not agree on that configuration, in
	// which case Spill is one of several and the map says so instead of
	// implying the whole grid ran under it.
	SpillVaried bool
}

// GridComparison is one point of the grid: what the policies did there.
type GridComparison struct {
	At GridPoint
	// Comparison is the full comparison at this point, inheriting every refusal
	// and every exclusion Compare makes.
	Comparison Comparison
	// Decisions is how each policy routed at this point, pooled over its usable
	// cells. It is what makes the two axes separable: the spill rule's two
	// branches are counted apart all the way from the router to here, so the
	// grid can show memory pressure firing one and load imbalance the other.
	Decisions map[string]DecisionMix
}

// RefusedPoint is a grid point with no comparison, and why.
type RefusedPoint struct {
	At  GridPoint
	Why string
}

// BuildPressureMap reduces cells from anywhere in a grid run to the map, with
// its headline delta between session affinity and prefix affinity, which is
// what #18 asks it to plot.
//
// The cells may come from one directory or twelve. Each point is swept into its
// own directory because each sends its own workload, so the ordinary way to
// call this is with every directory of a grid run named at once.
func BuildPressureMap(cells []Cell) (PressureMap, error) {
	return BuildPressureMapBetween(cells, policy.SessionAffinityName, policy.PrefixAffinityName)
}

// BuildPressureMapBetween is BuildPressureMap with the headline pair named: the
// baseline the delta is measured from, and the challenger measured against it.
func BuildPressureMapBetween(cells []Cell, baseline, challenger string) (PressureMap, error) {
	if len(cells) == 0 {
		return PressureMap{}, fmt.Errorf("bench: no cells to map")
	}
	if baseline == "" || challenger == "" || baseline == challenger {
		return PressureMap{}, fmt.Errorf("bench: a map's headline is a delta between two different policies, got %q against %q", challenger, baseline)
	}

	m := PressureMap{
		Baseline:   baseline,
		Challenger: challenger,
	}

	grouped := map[GridPoint][]Cell{}
	for _, cell := range cells {
		at := GridPointOf(cell)
		if !at.Stated() {
			m.Unstated++
			continue
		}
		grouped[at] = append(grouped[at], cell)
	}
	if len(grouped) == 0 {
		// Every cell carried no working set. This is the one failure mode of a
		// grid run that produces a full directory of perfectly good cells and no
		// grid at all, so it is named with its fix rather than reported as an
		// empty map.
		return PressureMap{}, fmt.Errorf("bench: none of these %d cells states a working set ratio, so none of them is on the grid. "+
			"A multi-turn pool given as a plain session count has no denominator to be a ratio against: pass -kv-capacity to the sweep, "+
			"which derives the ratio without changing a byte of what the cell sends", len(cells))
	}

	policies := map[string]bool{}
	slos := map[SLO][]GridPoint{}
	spills := map[policy.Spill]bool{}
	for _, at := range sortedGridPoints(grouped) {
		group := grouped[at]
		comparison, err := Compare(group)
		if err != nil {
			m.Refused = append(m.Refused, RefusedPoint{At: at, Why: err.Error()})
			continue
		}
		slos[comparison.SLO] = append(slos[comparison.SLO], at)
		for _, cell := range group {
			// Only the policies that have a spill rule say anything about how it
			// was configured. A round-robin cell records zeros because it has no
			// valve, not because the valve was off. Every valved policy is read,
			// not only the challenger: prefix affinity and exact residency run
			// the same rule at the same point, and one of them running at another
			// would otherwise pass unnoticed.
			if policy.HasSpillRule(cell.Policy) {
				spills[policy.Spill{KVHighWater: cell.KVHighWater, LoadImbalanceFactor: cell.LoadImbalanceFactor}] = true
			}
		}
		for _, name := range comparison.Policies {
			policies[name] = true
		}
		m.Excluded = append(m.Excluded, comparison.Excluded...)
		m.Surfaced = append(m.Surfaced, comparison.Surfaced...)
		m.Points = append(m.Points, GridComparison{
			At:         at,
			Comparison: comparison,
			Decisions:  pointDecisions(group, comparison.Policies),
		})
	}

	if len(slos) > 1 {
		// Each point checked its own cells; nothing until here checked the
		// points against each other. A map whose corners were judged against
		// different thresholds would put two definitions of goodput on one
		// figure, and the axes would carry the blame for it.
		var described []string
		for slo, points := range slos {
			described = append(described, fmt.Sprintf("TTFT < %v, ITL < %v (%d points, e.g. %s)",
				slo.TTFT, slo.ITL, len(points), points[0]))
		}
		sort.Strings(described)
		return PressureMap{}, fmt.Errorf("bench: the points of this grid were judged against %d different SLOs, so their goodput figures are not one map: %s",
			len(slos), strings.Join(described, "; "))
	}
	for slo := range slos {
		m.SLO = slo
	}
	m.Policies = comparisonOrder(policies)
	m.SpillVaried = len(spills) > 1
	for spill := range spills {
		m.Spill = spill
		if !m.SpillVaried {
			break
		}
	}
	return m, nil
}

// pointDecisions pools each policy's decision mix over the cells that were fit
// to pool, so the counts stand on the same cells the goodput above them does.
func pointDecisions(cells []Cell, policies []string) map[string]DecisionMix {
	mixes := map[string]DecisionMix{}
	for _, cell := range cells {
		if len(whyExcluded(cell)) > 0 {
			continue
		}
		if !slices.Contains(policies, cell.Policy) {
			continue
		}
		mixes[cell.Policy] = addDecisions(mixes[cell.Policy], cell.Decisions)
	}
	return mixes
}

// addDecisions sums two mixes, field by field.
//
// Counts of requests, so they combine by adding — the same rule the prefix
// cache and prefill counters are pooled under, and for the same reason:
// averaging rates across repetitions would weigh a short cell like a long one.
func addDecisions(a, b DecisionMix) DecisionMix {
	return DecisionMix{
		RoundRobin:          a.RoundRobin + b.RoundRobin,
		LeastOutstanding:    a.LeastOutstanding + b.LeastOutstanding,
		SessionAffinity:     a.SessionAffinity + b.SessionAffinity,
		SessionUnidentified: a.SessionUnidentified + b.SessionUnidentified,
		PrefixAffinity:      a.PrefixAffinity + b.PrefixAffinity,
		Cold:                a.Cold + b.Cold,
		SpillKV:             a.SpillKV + b.SpillKV,
		SpillLoad:           a.SpillLoad + b.SpillLoad,
		PromptUntokenized:   a.PromptUntokenized + b.PromptUntokenized,
		PrefixHash:          a.PrefixHash + b.PrefixHash,
		HashDeflected:       a.HashDeflected + b.HashDeflected,
		PromptUnhashed:      a.PromptUnhashed + b.PromptUnhashed,
		Undecided:           a.Undecided + b.Undecided,
	}
}

// sortedGridPoints puts the points in the order the grid is swept and drawn:
// working set ascending, skew ascending within it.
func sortedGridPoints(grouped map[GridPoint][]Cell) []GridPoint {
	points := make([]GridPoint, 0, len(grouped))
	for at := range grouped {
		points = append(points, at)
	}
	slices.SortFunc(points, func(a, b GridPoint) int {
		if a.WorkingSet != b.WorkingSet {
			return cmpFloat(a.WorkingSet, b.WorkingSet)
		}
		return cmpFloat(a.Skew, b.Skew)
	})
	return points
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// GoodputDelta is the difference between two policies' goodput at one grid
// point, with what the repetitions can support attached to it.
//
// The qualification travels with the number rather than being applied when it
// is printed, because this is the figure the whole project is read off and the
// most likely way to misread it is to take a difference smaller than the
// run-to-run spread for a result. Separated is the only field that licenses the
// sentence "the policies differed here".
type GoodputDelta struct {
	Baseline   PolicyGoodput
	Challenger PolicyGoodput
	// Measured is false when either policy has no usable cell at this point, in
	// which case every other field is meaningless. Absent is not zero: a policy
	// that did not run here did not score nothing here.
	Measured bool
	// PercentChange is the challenger's median over the baseline's, as a
	// percentage of the baseline. Meaningless when the baseline is zero, which
	// BaselineZero says instead of dividing.
	PercentChange float64
	BaselineZero  bool
	// Replicated is whether both sides have more than one repetition. A spread
	// of zero across one run is a claim about reproducibility one run cannot
	// make.
	Replicated bool
	// Separated is whether the two policies' repetition ranges are disjoint —
	// the difference between them is bigger than the difference between either
	// and itself. This is the field a claim rests on.
	Separated bool
	// OverFailureThreshold is whether either side rests on a repetition that
	// dropped or failed more of its requests than the threshold allows, which the
	// delta is marked with wherever it is shown.
	OverFailureThreshold bool
}

// DeltaAt is the headline difference at one grid point, for one load.
func (g GridComparison) DeltaAt(load Load, baseline, challenger string) GoodputDelta {
	for _, row := range g.Comparison.Rows {
		if row.Load != load {
			continue
		}
		return deltaBetween(row.Goodput[baseline], row.Goodput[challenger],
			hasGoodput(row, baseline) && hasGoodput(row, challenger))
	}
	return GoodputDelta{}
}

func hasGoodput(row ComparisonRow, name string) bool {
	_, ok := row.Goodput[name]
	return ok
}

func deltaBetween(baseline, challenger PolicyGoodput, measured bool) GoodputDelta {
	d := GoodputDelta{Baseline: baseline, Challenger: challenger, Measured: measured}
	if !measured {
		return d
	}
	d.Replicated = baseline.Repetitions > 1 && challenger.Repetitions > 1
	d.Separated = d.Replicated && !baseline.overlaps(challenger)
	d.OverFailureThreshold = baseline.OverFailureThreshold > 0 || challenger.OverFailureThreshold > 0
	if baseline.MedianRPS == 0 {
		d.BaselineZero = true
		return d
	}
	d.PercentChange = (challenger.MedianRPS - baseline.MedianRPS) / baseline.MedianRPS * 100
	return d
}

// String is the delta as a cell of the map, qualified the same way the
// comparison table qualifies its own deltas — one rendering of one rule, so the
// two figures cannot disagree about what "within spread" means. A delta resting
// on a repetition past the failure threshold carries the ⚠ its goodput does.
func (d GoodputDelta) String() string {
	var s string
	switch {
	case !d.Measured:
		return "—"
	case d.BaselineZero:
		s = fmt.Sprintf("+%.2f/s over zero", d.Challenger.MedianRPS)
	case !d.Replicated:
		s = fmt.Sprintf("%+.1f%% (unreplicated)", d.PercentChange)
	case !d.Separated:
		s = fmt.Sprintf("%+.1f%% (within spread)", d.PercentChange)
	default:
		s = fmt.Sprintf("%+.1f%%", d.PercentChange)
	}
	if d.OverFailureThreshold {
		s += " ⚠"
	}
	return s
}

// GainShare is how much of the gain exact knowledge of the caches buys that the
// approximate index keeps without it, at one grid point and load. It is #24's
// figure.
//
// The gain is measured over session affinity, the baseline that matters
// (idea.md §5): (prefix affinity − session affinity) / (exact residency −
// session affinity), in goodput medians. One means the approximation kept all
// of it and zero that it kept none; above one it beat the exact policy, and
// below zero it did worse than the baseline both exist to beat.
type GainShare struct {
	Baseline, Approximate, Exact PolicyGoodput
	// Share is the fraction kept. Meaningless unless Defined.
	Share float64
	// Defined is whether there was a gain to share: all three policies measured
	// here, and exact residency's gain over the baseline replicated and bigger
	// than the run-to-run spread. A share of a gain inside the spread is a share
	// of noise, and its denominator can be as small as the noise is.
	Defined bool
	// Why is what stopped a share being claimed, when one was not.
	Why string
}

// String is the share as a table cell: the percentage, or an em dash where no
// share can be claimed.
func (s GainShare) String() string {
	if !s.Defined {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", s.Share*100)
}

// GainKept is the share of the gain at this point, for one load.
func (g GridComparison) GainKept(load Load) GainShare {
	for _, row := range g.Comparison.Rows {
		if row.Load != load {
			continue
		}
		base, hasBase := row.Goodput[policy.SessionAffinityName]
		approx, hasApprox := row.Goodput[policy.PrefixAffinityName]
		exact, hasExact := row.Goodput[policy.ExactResidencyName]
		s := GainShare{Baseline: base, Approximate: approx, Exact: exact}
		if !hasBase || !hasApprox || !hasExact {
			s.Why = "not all of session affinity, prefix affinity and exact residency have a usable cell here"
			return s
		}
		gain := deltaBetween(base, exact, true)
		switch {
		case !gain.Replicated:
			s.Why = "unreplicated: one repetition cannot say whether exact residency gained anything"
		case !gain.Separated:
			s.Why = "exact residency's gain over session affinity is inside the spread, so there is no gain to share"
		case exact.MedianRPS <= base.MedianRPS:
			s.Why = "exact residency did not beat session affinity here, so there is no gain to share"
		default:
			s.Defined = true
			s.Share = (approx.MedianRPS - base.MedianRPS) / (exact.MedianRPS - base.MedianRPS)
		}
		return s
	}
	return GainShare{Why: "this point did not run at that load"}
}

// Spread is a policy's repetition range as a share of its median, and whether
// there are enough repetitions for that to mean anything.
//
// Published as its own column because at this grid's load rung the range is not
// a small correction to the median. #16 measured the spill-off reference at
// concurrency 32 and skew 1.4 returning 10.76, 17.05 and 22.01 — with SLO
// violation rates tracking each one exactly, which is what makes it two states
// rather than one noisy measurement. The policies without a spill valve have
// nothing to damp it, so their high-skew cells will show the same thing.
//
// It matters for how the median is read. A median of a bimodal sample is
// whichever mode won two of three draws, not a central value, and more
// repetitions narrow the estimate of the mode weights without making the cell
// unimodal. So a wide spread here is a reason to look at the repetitions
// themselves rather than to average harder.
func (g PolicyGoodput) Spread() (float64, bool) {
	if g.Repetitions < 2 || g.MedianRPS == 0 {
		return 0, false
	}
	return (g.MaxRPS - g.MinRPS) / g.MedianRPS, true
}

// Validity is the evidence that a grid point applied the pressure it claims to.
//
// idea.md §0 makes this a precondition rather than a diagnostic: spill only
// fires under pressure, so a grid run at a load or a working set that trips
// neither threshold produces a flat, uninterpretable map after a night of GPU
// time — and a flat map is indistinguishable from a real null result unless
// something says whether the mechanism was ever exercised. These are the three
// things §0 names, each read off a different part of the record so that no one
// failure can flatten all three at once.
type Validity struct {
	// RedundantPerRequest is the spread in prompt tokens the policies left the
	// GPUs to compute at this point, per request served: the worst policy's
	// excess over the best. Zero means every policy computed the same prefill per
	// request, so no policy kept anything warm that another did not.
	//
	// Per request because this grid is run under the closed-loop driver, where a
	// faster policy offers more prompts in the same window. A spread in absolute
	// totals there is partly a spread in throughput, so a point could read as
	// having exercised the mechanism on the strength of one policy having served
	// more traffic — and, worse, the sign of the column would name the wrong
	// policy as the wasteful one.
	RedundantPerRequest float64
	// RedundantTokens is that same worst-case excess as prompt tokens, against
	// the requests the policy carrying it actually served. Printed beside the
	// per-request figure so the size of the waste is visible, never instead of
	// it.
	RedundantTokens float64
	PrefillMeasured bool
	// HitRateSpread is the gap between the highest and lowest prefix cache hit
	// rate at this point. Zero means the fleet's caches served every policy
	// alike.
	HitRateSpread   float64
	HitRateMeasured bool
	// SpillKV and SpillLoad are the two spill conditions, counted over every
	// policy that has them at this point. They stay apart here for the reason
	// they stay apart everywhere else: they answer to different axes of this
	// grid, and a single "spilled" count could not show that.
	SpillKV   int
	SpillLoad int
	// SpillRate is the share of the challenger's decisions that declined a
	// prefix match, and Decisions how many decisions that is out of.
	//
	// A rate rather than only the counts because the valve is self-limiting:
	// declining a match moves load off the replica that was over the threshold,
	// so the condition that fired stops being true. #16 measured 0.674% of
	// decisions declined where an uncorrected fleet would have qualified 55% of
	// the time. Read as a binary "did it fire", that reports as a whisker above
	// nothing; read as a rate against the decisions it had to work with, it is
	// the mechanism working.
	SpillRate float64
	Decisions int
}

// Fired reports whether anything at this point distinguishes the policies at
// all. A point where this is false ran the grid without exercising the
// mechanism the grid is about.
func (v Validity) Fired() bool {
	return v.RedundantPerRequest > 0 || v.HitRateSpread > 0 || v.SpillKV > 0 || v.SpillLoad > 0
}

// Validity is the evidence at this grid point.
func (g GridComparison) Validity(challenger string) Validity {
	var v Validity
	for _, mix := range g.Decisions {
		v.SpillKV += mix.SpillKV
		v.SpillLoad += mix.SpillLoad
	}
	// The rate is the challenger's own: it is the only policy with a valve, and
	// diluting it with policies that have none would understate it by a factor
	// of the policy count.
	if mix, valved := g.Decisions[challenger]; valved {
		v.SpillRate, v.Decisions = mix.SpillRate(), mix.Total()
	}
	for _, row := range g.Comparison.Rows {
		// The worst policy's excess is taken per request, and its token figure is
		// that same policy's — not the largest token figure in the row, which
		// under a closed loop can belong to a different policy entirely.
		var worstPerRequest, worstTokens float64
		for _, name := range g.Comparison.Policies {
			excess, ok := row.RedundantPerRequest(name)
			if !ok {
				continue
			}
			v.PrefillMeasured = true
			if excess > worstPerRequest {
				worstPerRequest = excess
				worstTokens, _ = row.RedundantTokens(name)
			}
		}
		if worstPerRequest > v.RedundantPerRequest {
			v.RedundantPerRequest, v.RedundantTokens = worstPerRequest, worstTokens
		}

		low, high, seen := 0.0, 0.0, false
		for _, name := range g.Comparison.Policies {
			cache, ok := row.PrefixCache[name]
			if !ok || !cache.Evidenced() {
				continue
			}
			rate := cache.HitRate()
			if !seen {
				low, high, seen = rate, rate, true
				continue
			}
			low, high = min(low, rate), max(high, rate)
		}
		if seen {
			v.HitRateMeasured = true
			v.HitRateSpread = max(v.HitRateSpread, high-low)
		}
	}
	return v
}

// Loads is every load rung any point of the map measured.
//
// The grid runs at one concurrency, so ordinarily this is a single rung and the
// map is one table. It is a list rather than a single value because nothing
// stops a run from sweeping two, and a map that silently drew only the first
// would be averaging two rungs' worth of behaviour into one claim without
// saying so.
func (m PressureMap) Loads() []Load {
	present := map[Load]bool{}
	for _, point := range m.Points {
		for _, row := range point.Comparison.Rows {
			present[row.Load] = true
		}
	}
	return sortedLoads(present)
}

// Separates reports whether any point of the map found a difference between the
// two policies that is bigger than the run-to-run spread.
func (m PressureMap) Separates() bool {
	for _, load := range m.Loads() {
		for _, point := range m.Points {
			if point.DeltaAt(load, m.Baseline, m.Challenger).Separated {
				return true
			}
		}
	}
	return false
}

// MechanismFired reports whether any point exercised the mechanism at all.
func (m PressureMap) MechanismFired() bool {
	for _, point := range m.Points {
		if point.Validity(m.Challenger).Fired() {
			return true
		}
	}
	return false
}

// SuspectTheWorkload is idea.md §6's instruction written as a check: if nothing
// separates anywhere and the validity evidence is also flat, the workload is
// corrected before any policy is touched.
//
// The two conditions together, never either alone. A map that separates nowhere
// but whose mechanism fired is a real null result and one of the two outcomes
// this project is built to publish — the crossover is outside this fleet's
// reach. A map that separates nowhere and never fired the mechanism has not
// measured the policies at all; it has measured a workload that applied no
// pressure, and tuning a policy against it would be tuning against noise.
func (m PressureMap) SuspectTheWorkload() bool {
	return !m.Separates() && !m.MechanismFired()
}

// MissingPoints is every point of the intended grid that this map has no
// comparison for, so a partial run reads as partial.
func (m PressureMap) MissingPoints() []GridPoint {
	have := map[GridPoint]bool{}
	for _, point := range m.Points {
		have[point.At] = true
	}
	var missing []GridPoint
	for _, at := range PressureGrid() {
		if !have[at] {
			missing = append(missing, at)
		}
	}
	return missing
}
