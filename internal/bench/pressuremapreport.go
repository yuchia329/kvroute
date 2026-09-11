package bench

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yuchia329/kvroute/internal/policy"
)

// Report renders the pressure map as markdown: the headline grid, the detail
// behind it, the evidence that the run applied the pressure it claims to, and
// everything that was left out.
//
// Tables rather than a drawn figure, for the reason the divergence report gives:
// the per-cell records carry every column a plot needs, so this is the shape the
// numbers are read and checked in, and a plot is drawn from the records rather
// than from a picture somebody would have to trust.
//
// The order is deliberate. The validity evidence comes before the reader can
// act on the headline, because a delta from a run that never applied pressure
// is not a small effect — it is no measurement, and §0 says to fix the workload
// rather than the policy when that happens.
func (m PressureMap) Report() string {
	var b strings.Builder

	fmt.Fprintf(&b, "# The pressure map — where cache-aware routing pays\n\n")
	fmt.Fprintf(&b, "Goodput delta between **%s** and **%s** across the working set × skew grid,\n", m.Baseline, m.Challenger)
	fmt.Fprintf(&b, "at a single concurrency. Positive means %s served more requests inside the SLO.\n\n", m.Challenger)
	fmt.Fprintf(&b, "SLO: TTFT < %v, inter-token p50 < %v. Goodput is requests per second that met it,\n", m.SLO.TTFT, m.SLO.ITL)
	fmt.Fprintf(&b, "so a policy that completed more requests can still score lower.\n\n")
	fmt.Fprintf(&b, "Each point of the grid is its own comparison: the two policies there sent the same\n")
	fmt.Fprintf(&b, "bytes, which is what makes them comparable, and two different points did not — so\n")
	fmt.Fprintf(&b, "figures are compared down a column and along a row, never against another grid's.\n\n")
	fmt.Fprintf(&b, "A delta marked *within spread* is smaller than the run-to-run range of the\n")
	fmt.Fprintf(&b, "repetitions behind it, which is a difference between a policy and itself.\n\n")

	m.reportAxisCaveat(&b)
	m.reportValidity(&b)
	m.reportMap(&b)
	m.reportGainShare(&b)
	m.reportDetail(&b)
	m.reportSeparability(&b)
	m.reportGaps(&b)
	return b.String()
}

// reportAxisCaveat states what the WS labels are and are not, at the top, where
// it cannot be read past.
//
// The axis is the headline's independent variable, and it is the one column of
// this table that is a configured intention rather than a measurement. A reader
// who takes "WS 8" for eight times the fleet's capacity will read a flat top end
// as cache affinity ceasing to pay, when it may be two points that applied
// nearly the same pressure.
func (m PressureMap) reportAxisCaveat(b *strings.Builder) {
	fmt.Fprintf(b, "> ⚠️ **The WS axis is labelled with what each cell was configured for, not what it\n")
	fmt.Fprintf(b, "> applied.** Skew discounts working set — concentrating the draws touches fewer\n")
	fmt.Fprintf(b, "> distinct conversations, so at WS 1 a cell realises about 0.97 of its label at\n")
	fmt.Fprintf(b, "> skew 0 and 0.40 at skew 1.4 — and a cell of finite length cannot touch a pool\n")
	fmt.Fprintf(b, "> bigger than its visit count, which bites hardest at the top of the axis. So\n")
	fmt.Fprintf(b, "> the realised pressure rises more slowly than the labels do, especially to the\n")
	fmt.Fprintf(b, "> right and at the top. Neither discount is corrected here: correcting either\n")
	fmt.Fprintf(b, "> would change the workload's name and refuse every cell recorded under the old\n")
	fmt.Fprintf(b, "> one (ADR-0004, ADR-0007). The realised draw is countable from the session\n")
	fmt.Fprintf(b, "> column of the rows, and a flat top end should be checked against it before it\n")
	fmt.Fprintf(b, "> is read as a result.\n\n")
}

// reportValidity renders the evidence that each point applied pressure, and the
// verdict §6 attaches to a run where none of it did.
func (m PressureMap) reportValidity(b *strings.Builder) {
	fmt.Fprintf(b, "## Did the run apply the pressure it claims to?\n\n")
	fmt.Fprintf(b, "Spill only fires under pressure, so a grid run where neither threshold is ever\n")
	fmt.Fprintf(b, "tripped collapses policy 4 into policy 3 and returns a flat map that says nothing\n")
	fmt.Fprintf(b, "about either. These three columns are read before the map below it.\n\n")
	fmt.Fprintf(b, "Redundant prefill is the worst policy's excess prompt tokens over the best at that\n")
	fmt.Fprintf(b, "point; hit rate spread is the gap between the highest and lowest prefix cache hit\n")
	fmt.Fprintf(b, "rate there. The two spill columns stay apart because they answer to different axes\n")
	fmt.Fprintf(b, "of this grid. An em dash is a counter nobody read, which is not a zero.\n\n")
	fmt.Fprintf(b, "Spill rate is the share of the challenger's decisions that declined a prefix match,\n")
	fmt.Fprintf(b, "and it is expected to be small. The valve is self-limiting: declining a match moves\n")
	fmt.Fprintf(b, "load off the replica that was over the threshold, so the condition that fired stops\n")
	fmt.Fprintf(b, "being true. #16 measured 0.674%% of decisions declined on a fleet where an\n")
	fmt.Fprintf(b, "uncorrected run would have qualified 55%% of the time. A near-zero rate here is the\n")
	fmt.Fprintf(b, "mechanism working, not a rule that failed to fire.\n\n")

	fmt.Fprintln(b, "| point | redundant prefill | hit rate spread | spill: KV | spill: load | spill rate | exercised |")
	fmt.Fprintln(b, "|---|---:|---:|---:|---:|---:|---|")
	for _, point := range m.Points {
		v := point.Validity(m.Challenger)
		prefill, hitRate := "—", "—"
		if v.PrefillMeasured {
			prefill = fmt.Sprintf("%.0f", v.RedundantPrefill)
		}
		if v.HitRateMeasured {
			hitRate = fmt.Sprintf("%.1f pp", v.HitRateSpread*100)
		}
		exercised := "no"
		if v.Fired() {
			exercised = "yes"
		}
		rate := "—"
		if v.Decisions > 0 {
			rate = fmt.Sprintf("%.3f%%", v.SpillRate*100)
		}
		fmt.Fprintf(b, "| %s | %s | %s | %d | %d | %s | %s |\n",
			point.At, prefill, hitRate, v.SpillKV, v.SpillLoad, rate, exercised)
	}
	fmt.Fprintln(b)

	switch {
	case m.SuspectTheWorkload():
		fmt.Fprintf(b, "🛑 **Nothing separated anywhere, and the mechanism never fired either.** No point\n")
		fmt.Fprintf(b, "of this grid shows the policies computing different prefill, serving different\n")
		fmt.Fprintf(b, "cache hit rates, or declining a single match. That is not a null result: a run\n")
		fmt.Fprintf(b, "that applied no pressure has not compared the policies at all. **Correct the\n")
		fmt.Fprintf(b, "workload before touching any policy** — check that the working set reached the\n")
		fmt.Fprintf(b, "fleet, that the load rung is high enough for a threshold to be reachable, and\n")
		fmt.Fprintf(b, "that the router was actually running the policy each cell is labelled with.\n\n")
	case !m.Separates():
		fmt.Fprintf(b, "**No point separated, but the mechanism did fire.** The policies routed\n")
		fmt.Fprintf(b, "differently and left the fleet different work to do, and it did not move goodput\n")
		fmt.Fprintf(b, "beyond the run-to-run spread anywhere on this grid. That is a null result about\n")
		fmt.Fprintf(b, "this fleet rather than a broken run: the crossover is outside the pressure these\n")
		fmt.Fprintf(b, "points reach. idea.md §0 records it as publishable, and the mechanism columns\n")
		fmt.Fprintf(b, "above are what make it a finding rather than an absence.\n\n")
	default:
		fmt.Fprintf(b, "The mechanism fired and at least one point separated by more than its spread.\n")
		fmt.Fprintf(b, "The map below is measuring the policies.\n\n")
	}
}

// reportMap renders the headline: the delta laid out on the two axes, one grid
// per load rung.
func (m PressureMap) reportMap(b *strings.Builder) {
	fmt.Fprintf(b, "## The map — Δ goodput, %s against %s\n\n", m.Challenger, m.Baseline)

	if !m.hasPair() {
		fmt.Fprintf(b, "Not drawn: this map has no point where both %s and %s produced a usable cell,\n", m.Baseline, m.Challenger)
		fmt.Fprintf(b, "so there is no delta to lay out. The policies present are %s.\n\n", namesOr(m.Policies, "none"))
		return
	}

	skews := m.skewAxis()
	for _, load := range m.Loads() {
		fmt.Fprintf(b, "At %s:\n\n", load)
		fmt.Fprintf(b, "| WS \\ skew |")
		for _, skew := range skews {
			fmt.Fprintf(b, " %s |", formatAxis(skew))
		}
		fmt.Fprintln(b)
		fmt.Fprintf(b, "|---|")
		for range skews {
			fmt.Fprintf(b, "---:|")
		}
		fmt.Fprintln(b)

		for _, ws := range m.workingSetAxis() {
			fmt.Fprintf(b, "| **%s** |", formatAxis(ws))
			for _, skew := range skews {
				at := GridPoint{WorkingSet: ws, Skew: skew}
				point, found := m.pointAt(at)
				if !found {
					fmt.Fprintf(b, " not run |")
					continue
				}
				fmt.Fprintf(b, " %s |", point.DeltaAt(load, m.Baseline, m.Challenger))
			}
			fmt.Fprintln(b)
		}
		fmt.Fprintln(b)
	}
}

// reportGainShare answers #24: how much of the gain exact knowledge of the
// caches buys over session affinity the approximate index keeps without it.
//
// Rendered only when the map holds all three policies, because the share is a
// ratio of two of their differences and means nothing with any of them missing.
// The P90 columns are there for the one comparison #24 replicates, and llm-d's
// published figures ride along as the last row so that they are set beside this
// run's rather than left for the reader to find.
func (m PressureMap) reportGainShare(b *strings.Builder) {
	if !m.hasGainTriple() {
		return
	}
	fmt.Fprintf(b, "## How much of the gain does the approximate index keep?\n\n")
	fmt.Fprintf(b, "The share of the gain exact knowledge of the caches buys over session affinity that prefix\n")
	fmt.Fprintf(b, "affinity keeps without it: (prefix affinity − session affinity) / (exact residency − session\n")
	fmt.Fprintf(b, "affinity), in goodput medians. 100%% is all of it; above 100%% the approximation beat the\n")
	fmt.Fprintf(b, "exact policy; below 0%% it did worse than the baseline both exist to beat. No share is claimed\n")
	fmt.Fprintf(b, "where exact residency's own gain is inside the run-to-run spread: a share of noise is noise.\n\n")
	fmt.Fprintf(b, "The TTFT columns are there for the comparison this replicates. llm-d published its\n")
	fmt.Fprintf(b, "precise-versus-approximate result as P90 TTFT — 0.54 s precise against 31.1 s approximate —\n")
	fmt.Fprintf(b, "at datacentre scale, and the last row carries it. It is set beside these figures, not ranked\n")
	fmt.Fprintf(b, "against them: a different fleet, model and workload, where this is one host of consumer cards\n")
	fmt.Fprintf(b, "whose caches are far smaller and evict far more often (ADR-0010).\n\n")

	fmt.Fprintln(b, "| point | load | share of the gain kept | session affinity | prefix affinity | exact residency | TTFT p90, prefix affinity | TTFT p90, exact residency |")
	fmt.Fprintln(b, "|---|---:|---:|---:|---:|---:|---:|---:|")
	var unclaimed []string
	for _, point := range m.Points {
		for _, row := range point.Comparison.Rows {
			kept := point.GainKept(row.Load)
			if !kept.Defined {
				unclaimed = append(unclaimed, fmt.Sprintf("%s at %s: %s", point.At, row.Load, kept.Why))
			}
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n",
				point.At, row.Load, kept,
				figure(row.Goodput, policy.SessionAffinityName),
				figure(row.Goodput, policy.PrefixAffinityName),
				figure(row.Goodput, policy.ExactResidencyName),
				ttftP90(row, policy.PrefixAffinityName), ttftP90(row, policy.ExactResidencyName))
		}
	}
	fmt.Fprintln(b, "| llm-d, published (datacentre scale) | — | — | — | — | — | 31.1 s | 0.54 s |")
	fmt.Fprintln(b)
	if len(unclaimed) > 0 {
		fmt.Fprintf(b, "Where no share is claimed, and why:\n\n")
		for _, why := range unclaimed {
			fmt.Fprintf(b, "- %s\n", why)
		}
		fmt.Fprintln(b)
	}
}

// ttftP90 is one policy's pooled P90 TTFT at a row, or an em dash where it has
// no usable cell.
func ttftP90(row ComparisonRow, name string) string {
	latency, ok := row.Latency[name]
	if !ok {
		return "—"
	}
	return ms(latency.P90Ns)
}

// hasGainTriple reports whether the map holds all three policies the share of
// the gain is a ratio between.
func (m PressureMap) hasGainTriple() bool {
	return slices.Contains(m.Policies, policy.SessionAffinityName) &&
		slices.Contains(m.Policies, policy.PrefixAffinityName) &&
		slices.Contains(m.Policies, policy.ExactResidencyName)
}

// reportDetail renders the goodput each policy actually scored, which is what
// the deltas above are differences of.
//
// Separate from the map rather than more columns on it: four policies with a
// median and a range apiece would be a table nobody reads across, and the map
// is the figure while this is the evidence for it.
func (m PressureMap) reportDetail(b *strings.Builder) {
	fmt.Fprintf(b, "## Goodput behind the map\n\n")
	fmt.Fprintf(b, "Each figure is the median of that point's repetitions with the range across them,\n")
	fmt.Fprintf(b, "and the spread beneath it as a share of that median. An em dash is a policy with no\n")
	fmt.Fprintf(b, "usable cell at that point, which is not a zero.\n\n")
	fmt.Fprintf(b, "⚠️ Read a wide spread as two states rather than as noise. #16 measured the spill-off\n")
	fmt.Fprintf(b, "reference at this load rung and skew 1.4 returning 10.76, 17.05 and 22.01, with SLO\n")
	fmt.Fprintf(b, "violation rates tracking each one exactly — which is bistability, not variance. The\n")
	fmt.Fprintf(b, "spill valve damps it for the challenger; the policies without one have nothing to\n")
	fmt.Fprintf(b, "damp it, so expect it in their high-skew cells. A median over a bimodal sample is\n")
	fmt.Fprintf(b, "whichever state won most draws, and more repetitions will not make it unimodal.\n\n")

	fmt.Fprintf(b, "| point | load |")
	for _, name := range m.Policies {
		fmt.Fprintf(b, " %s |", name)
	}
	fmt.Fprintln(b)
	fmt.Fprintf(b, "|---|---:|")
	for range m.Policies {
		fmt.Fprintf(b, "---:|")
	}
	fmt.Fprintln(b)

	for _, point := range m.Points {
		for _, row := range point.Comparison.Rows {
			fmt.Fprintf(b, "| %s | %s |", point.At, row.Load)
			for _, name := range m.Policies {
				fmt.Fprintf(b, " %s |", figureWithSpread(row.Goodput, name))
			}
			fmt.Fprintln(b)
		}
	}
	fmt.Fprintln(b)
}

// reportSeparability is the check that the two axes drive different things.
//
// The spill rule has two branches and they are counted apart all the way from
// the router to this table, so this is the direct evidence: if the grid is doing
// what it was built to do, the KV branch climbs with working set and the load
// branch climbs with skew. Two marginals rather than the full grid, because the
// question is whether each axis moves its own branch, and pooling the other axis
// is what isolates it.
//
// It is also the one place a reader can catch the axes having been crossed
// wrongly. A KV branch that climbs with skew rather than with working set is a
// grid whose two knobs are not doing what their names say.
func (m PressureMap) reportSeparability(b *strings.Builder) {
	fmt.Fprintf(b, "## Are the two pressures separable?\n\n")
	fmt.Fprintf(b, "The spill rule's two branches are counted apart, and the grid exists because they\n")
	fmt.Fprintf(b, "answer to different axes: memory pressure evicts and trips the KV high-water mark,\n")
	fmt.Fprintf(b, "load imbalance piles conversations up and trips the imbalance factor. Each axis is\n")
	fmt.Fprintf(b, "pooled over the other, so each row isolates one of them.\n\n")

	if !m.SpillVaried && m.Spill.KVHighWater == 0 {
		// The criterion cannot be answered, and the tables below cannot answer it
		// either. Said here, above them, because a column of zeros read without
		// this reads as a finding about the axis rather than as a condition that
		// was never armed.
		fmt.Fprintf(b, "🚧 **This grid cannot answer that question, and the tables below must not be read\n")
		fmt.Fprintf(b, "as though it had.** The KV high-water branch was switched off for these cells\n")
		fmt.Fprintf(b, "(`bench.Chosen`), so its column is zero everywhere by construction rather than by\n")
		fmt.Fprintf(b, "measurement.\n\n")
		fmt.Fprintf(b, "The reason is a property of the metric, not of this grid. `vllm:kv_cache_usage_perc`\n")
		fmt.Fprintf(b, "counts blocks held by *running* requests, so it reads the active batch and not cache\n")
		fmt.Fprintf(b, "residency: #16 measured kv = 0.02128 + 0.02135 x inflight at r = 0.973 over 13,658\n")
		fmt.Fprintf(b, "rows, and an idle replica holding a full cache reads 0.021. On this fleet the KV\n")
		fmt.Fprintf(b, "branch *is* the load branch, so any non-zero mark either cannot fire or fires on\n")
		fmt.Fprintf(b, "load — which the imbalance factor already covers. Arming it would make this\n")
		fmt.Fprintf(b, "section report a pass while measuring one pressure twice.\n\n")
		fmt.Fprintf(b, "So separability is **blocked on #28**, which needs a residency metric the engine\n")
		fmt.Fprintf(b, "does not currently expose. This is a real scoping loss for the grid rather than a\n")
		fmt.Fprintf(b, "presentational one: the other six acceptance criteria stand, and this one waits.\n")
		fmt.Fprintf(b, "The tables are still printed, because the load column remains a measurement and\n")
		fmt.Fprintf(b, "the KV column is the evidence that it was never armed.\n\n")
	} else {
		fmt.Fprintf(b, "If the grid is doing its job, KV spills climb down the first table and load spills\n")
		fmt.Fprintf(b, "climb down the second. A KV branch that climbs with skew instead would mean the two\n")
		fmt.Fprintf(b, "knobs are not driving what their names say.\n\n")
	}

	fmt.Fprintln(b, "| working set (skew pooled) | spill: KV | spill: load |")
	fmt.Fprintln(b, "|---|---:|---:|")
	for _, ws := range m.workingSetAxis() {
		kv, load := 0, 0
		for _, point := range m.Points {
			if point.At.WorkingSet != ws {
				continue
			}
			v := point.Validity(m.Challenger)
			kv, load = kv+v.SpillKV, load+v.SpillLoad
		}
		fmt.Fprintf(b, "| %s | %d | %d |\n", formatAxis(ws), kv, load)
	}
	fmt.Fprintln(b)

	fmt.Fprintln(b, "| skew (working set pooled) | spill: KV | spill: load |")
	fmt.Fprintln(b, "|---|---:|---:|")
	for _, skew := range m.skewAxis() {
		kv, load := 0, 0
		for _, point := range m.Points {
			if point.At.Skew != skew {
				continue
			}
			v := point.Validity(m.Challenger)
			kv, load = kv+v.SpillKV, load+v.SpillLoad
		}
		fmt.Fprintf(b, "| %s | %d | %d |\n", formatAxis(skew), kv, load)
	}
	fmt.Fprintln(b)
}

// reportGaps lists everything the map does not rest on: points never run,
// points that could not be compared, cells with no working set, and cells
// thrown out.
//
// All four together at the end, because they are one question — what is this
// figure missing — and answering it in four places would let three of them go
// unread.
func (m PressureMap) reportGaps(b *strings.Builder) {
	missing := m.MissingPoints()
	if len(missing) == 0 && len(m.Refused) == 0 && m.Unstated == 0 && len(m.Excluded) == 0 {
		return
	}
	fmt.Fprintf(b, "## What this map does not rest on\n\n")

	if len(missing) > 0 {
		fmt.Fprintf(b, "Points of the grid with no cells at all — the run is this much short of complete:\n\n")
		for _, at := range missing {
			fmt.Fprintf(b, "- %s\n", at)
		}
		fmt.Fprintln(b)
	}
	if len(m.Refused) > 0 {
		fmt.Fprintf(b, "Points that ran but could not be compared:\n\n")
		for _, refused := range m.Refused {
			fmt.Fprintf(b, "- %s: %s\n", refused.At, refused.Why)
		}
		fmt.Fprintln(b)
	}
	if m.Unstated > 0 {
		fmt.Fprintf(b, "%d cells state no working set ratio and so sit on no point of the grid. A\n", m.Unstated)
		fmt.Fprintf(b, "multi-turn pool given as a plain session count has no denominator to be a ratio\n")
		fmt.Fprintf(b, "against: pass `-kv-capacity` to the sweep, which derives the ratio without\n")
		fmt.Fprintf(b, "changing a byte of what the cell sends.\n\n")
	}
	if len(m.Excluded) > 0 {
		fmt.Fprintf(b, "Cells excluded from every figure above — §6 discards these rather than averaging\n")
		fmt.Fprintf(b, "them in:\n\n")
		for _, reason := range m.Excluded {
			fmt.Fprintf(b, "- %s\n", reason)
		}
		fmt.Fprintln(b)
	}
}

// figureWithSpread is one policy's goodput with its repetition spread, which at
// this load rung is load-bearing rather than a footnote: see PolicyGoodput.Spread.
func figureWithSpread(goodput map[string]PolicyGoodput, policy string) string {
	g, ok := goodput[policy]
	if !ok {
		return "—"
	}
	spread, replicated := g.Spread()
	if !replicated {
		return g.String()
	}
	return fmt.Sprintf("%s<br>±%.0f%%", g.String(), spread*100)
}

// hasPair reports whether any point measured both halves of the headline
// comparison.
func (m PressureMap) hasPair() bool {
	return slices.Contains(m.Policies, m.Baseline) && slices.Contains(m.Policies, m.Challenger)
}

// pointAt finds the comparison at one grid point.
func (m PressureMap) pointAt(at GridPoint) (GridComparison, bool) {
	for _, point := range m.Points {
		if point.At == at {
			return point, true
		}
	}
	return GridComparison{}, false
}

// workingSetAxis and skewAxis are the axes this map actually has cells on,
// ascending.
//
// Read off the points rather than taken from PressureWorkingSets and
// PressureSkews, so a partial run draws the grid it has instead of a full grid
// mostly full of holes — and so a run deliberately swept at a point off the
// standard axis still appears.
func (m PressureMap) workingSetAxis() []float64 {
	return m.axis(func(at GridPoint) float64 { return at.WorkingSet })
}

func (m PressureMap) skewAxis() []float64 {
	return m.axis(func(at GridPoint) float64 { return at.Skew })
}

func (m PressureMap) axis(of func(GridPoint) float64) []float64 {
	present := map[float64]bool{}
	for _, point := range m.Points {
		present[of(point.At)] = true
	}
	values := make([]float64, 0, len(present))
	for v := range present {
		values = append(values, v)
	}
	slices.Sort(values)
	return values
}

// namesOr renders a policy list for a sentence, or a stand-in when it is empty.
func namesOr(names []string, empty string) string {
	if len(names) == 0 {
		return empty
	}
	return strings.Join(names, ", ")
}
