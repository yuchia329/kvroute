package bench_test

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// gridLoad is the single rung the whole grid runs at.
var gridLoad = bench.ClosedLoopAt(bench.PressureConcurrency)

// gridCell builds a clean, unflagged cell at one point of the pressure grid.
//
// The workload name is derived from the point, as a real run's is: each point
// offers its own session pool at its own concentration, so two points never
// share a name and Compare is never asked to put two workloads in one table.
func gridCell(at bench.GridPoint, policyName string, repetition int, goodput float64) bench.Cell {
	return bench.Cell{
		ID:          bench.CellID(policyName, gridLoad, repetition) + "-" + at.Key(),
		Policy:      policyName,
		Driver:      gridLoad.Driver,
		Concurrency: gridLoad.Concurrency,
		Repetition:  repetition,
		Workload:    fmt.Sprintf("multiturn(ws=%g,skew=%g,turns=4,seed=1)", at.WorkingSet, at.Skew),
		WorkingSet:  at.WorkingSet,
		Skew:        at.Skew,
		Summary: bench.Summary{
			Requests: 100, Successes: 100,
			GoodputRPS: goodput, ThroughputRPS: goodput + 1,
			SLOApplied: true,
			SLOTTFTNs:  derivedSLO.TTFT.Nanoseconds(),
			SLOITLNs:   derivedSLO.ITL.Nanoseconds(),
		},
		Contamination: bench.Contamination{GPUSamples: 12, Clean: true},
	}
}

// gridCells builds one policy's repetitions at one point.
func gridCells(at bench.GridPoint, policyName string, goodputs ...float64) []bench.Cell {
	out := make([]bench.Cell, 0, len(goodputs))
	for i, g := range goodputs {
		out = append(out, gridCell(at, policyName, i+1, g))
	}
	return out
}

// bothPolicies is the headline pair at one point: the baseline this project has
// to beat, and the policy that claims to.
func bothPolicies(at bench.GridPoint, session, prefix []float64) []bench.Cell {
	var out []bench.Cell
	out = append(out, gridCells(at, policy.SessionAffinityName, session...)...)
	out = append(out, gridCells(at, policy.PrefixAffinityName, prefix...)...)
	return out
}

func buildMap(t *testing.T, cs []bench.Cell) bench.PressureMap {
	t.Helper()
	m, err := bench.BuildPressureMap(cs)
	if err != nil {
		t.Fatalf("BuildPressureMap: %v", err)
	}
	return m
}

var (
	lowPressure  = bench.GridPoint{WorkingSet: 0.25, Skew: 0}
	highPressure = bench.GridPoint{WorkingSet: 8, Skew: 0}
	highSkew     = bench.GridPoint{WorkingSet: 1, Skew: 1.4}
)

// A cell that failed too many requests is in the map's figures, as it is in each
// point's comparison, and the map names it beside them rather than among the
// cells it does not rest on.
func TestTheMapShowsCellsThatFailedTooOftenMarked(t *testing.T) {
	failingCell := pastFailureThreshold(gridCell(highSkew, policy.SessionAffinityName, 3, 2.0))
	cs := bothPolicies(highSkew, []float64{6, 7}, []float64{20, 21, 22})
	cs = append(cs, failingCell)

	m := buildMap(t, cs)

	if len(m.Surfaced) != 1 || len(m.Excluded) != 0 {
		t.Fatalf("surfaced %v and excluded %v, want the failing cell surfaced only", m.Surfaced, m.Excluded)
	}
	report := m.Report()
	if !strings.Contains(report, "6.00 (2.00–7.00, n=3) ⚠") {
		t.Errorf("the goodput behind the map does not mark the figure resting on the failing cell:\n%s", report)
	}
	if !strings.Contains(report, m.Surfaced[0]) {
		t.Errorf("the map does not name the failing cell:\n%s", report)
	}
	// The headline square rests on that repetition too, so it is marked there:
	// 6 against 21 is +250%, separated, and not a healthy fleet's +250%.
	if !strings.Contains(report, "| +250.0% ⚠ |") {
		t.Errorf("the map's headline delta does not mark that it rests on the failing cell:\n%s", report)
	}
	delta := m.Figure().Deltas[0]
	if delta.Label != "+250.0% ⚠" || !delta.OverFailureThreshold {
		t.Errorf("the headline figure's square = %+v, want it labelled and flagged as resting on the failing cell", delta)
	}
}

// The map's shape: one comparison per grid point, in grid order, each resting
// only on its own point's cells.
//
// This is the whole reason the map exists as its own reduction rather than as a
// wider comparison. Two grid points send different workloads, and a single
// comparison over all of them would either refuse outright or, worse, report the
// difference between two workloads as a difference between two policies.
func TestTheMapIsOneComparisonPerGridPoint(t *testing.T) {
	var cs []bench.Cell
	cs = append(cs, bothPolicies(highPressure, []float64{8, 8, 8}, []float64{9, 9, 9})...)
	cs = append(cs, bothPolicies(lowPressure, []float64{10, 10, 10}, []float64{10, 10, 10})...)

	m := buildMap(t, cs)

	if len(m.Points) != 2 {
		t.Fatalf("%d points, want one per grid point", len(m.Points))
	}
	if m.Points[0].At != lowPressure || m.Points[1].At != highPressure {
		t.Errorf("points are %s then %s, want the WS axis ascending", m.Points[0].At, m.Points[1].At)
	}
	if m.SLO != derivedSLO {
		t.Errorf("SLO = %+v, want the one the cells were judged against %+v", m.SLO, derivedSLO)
	}
	// Each point's comparison rests on that point's cells alone: the high
	// pressure point separates and the low one does not, and nothing pools them.
	high := m.Points[1].DeltaAt(gridLoad, m.Baseline, m.Challenger)
	if !high.Separated {
		t.Errorf("at %s the policies scored 8 and 9 with no spread and the delta is not separated: %s", highPressure, high)
	}
	low := m.Points[0].DeltaAt(gridLoad, m.Baseline, m.Challenger)
	if low.PercentChange != 0 {
		t.Errorf("at %s both policies scored 10, and the delta is %s", lowPressure, low)
	}
}

// The headline pair is session affinity against prefix affinity, which is what
// #18 asks the map to plot.
func TestTheHeadlineDeltaIsSessionAffinityAgainstPrefixAffinity(t *testing.T) {
	m := buildMap(t, bothPolicies(highPressure, []float64{8, 8, 8}, []float64{9, 9, 9}))
	if m.Baseline != policy.SessionAffinityName {
		t.Errorf("baseline = %q, want %q", m.Baseline, policy.SessionAffinityName)
	}
	if m.Challenger != policy.PrefixAffinityName {
		t.Errorf("challenger = %q, want %q", m.Challenger, policy.PrefixAffinityName)
	}
	if got := m.Points[0].DeltaAt(gridLoad, m.Baseline, m.Challenger).PercentChange; got != 12.5 {
		t.Errorf("delta = %v%%, want 9 over 8 = +12.5%%", got)
	}
}

// #24 draws the same map for a different pair: exact residency against the
// approximate index it is the exact counterpart of.
func TestTheHeadlinePairCanBeExactResidencyAgainstPrefixAffinity(t *testing.T) {
	var cs []bench.Cell
	cs = append(cs, gridCells(highPressure, policy.PrefixAffinityName, 8, 8, 8)...)
	cs = append(cs, gridCells(highPressure, policy.ExactResidencyName, 9, 9, 9)...)

	m, err := bench.BuildPressureMapBetween(cs, policy.PrefixAffinityName, policy.ExactResidencyName)
	if err != nil {
		t.Fatalf("BuildPressureMapBetween: %v", err)
	}
	if m.Baseline != policy.PrefixAffinityName || m.Challenger != policy.ExactResidencyName {
		t.Errorf("pair = %s against %s, want %s against %s", m.Challenger, m.Baseline, policy.ExactResidencyName, policy.PrefixAffinityName)
	}
	if got := m.Points[0].DeltaAt(gridLoad, m.Baseline, m.Challenger).PercentChange; got != 12.5 {
		t.Errorf("delta = %v%%, want 9 over 8 = +12.5%%", got)
	}
}

// Both policies with a spill rule run it, at the same thresholds, so the map reads
// the configuration off both — whichever of them is the headline's challenger.
// Reading only the challenger would let the other run at a different point
// unnoticed.
func TestTheSpillConfigurationIsReadOffEveryPolicyWithARule(t *testing.T) {
	spilling := func(cs []bench.Cell, factor float64) []bench.Cell {
		for i := range cs {
			cs[i].LoadImbalanceFactor = factor
		}
		return cs
	}
	var agreeing []bench.Cell
	agreeing = append(agreeing, gridCells(highPressure, policy.SessionAffinityName, 8, 8, 8)...)
	agreeing = append(agreeing, spilling(gridCells(highPressure, policy.PrefixAffinityName, 9, 9, 9), 2)...)
	agreeing = append(agreeing, spilling(gridCells(highPressure, policy.ExactResidencyName, 10, 10, 10), 2)...)
	if m := buildMap(t, agreeing); m.SpillVaried || m.Spill.LoadImbalanceFactor != 2 {
		t.Errorf("spill = %v (varied: %v), want 2x from both valved policies agreeing", m.Spill, m.SpillVaried)
	}

	var disagreeing []bench.Cell
	disagreeing = append(disagreeing, gridCells(highPressure, policy.SessionAffinityName, 8, 8, 8)...)
	disagreeing = append(disagreeing, spilling(gridCells(highPressure, policy.PrefixAffinityName, 9, 9, 9), 2)...)
	disagreeing = append(disagreeing, spilling(gridCells(highPressure, policy.ExactResidencyName, 10, 10, 10), 3)...)
	if m := buildMap(t, disagreeing); !m.SpillVaried {
		t.Error("exact residency ran at a different spill point from prefix affinity, and the map did not say so")
	}
}

// The ticket's own figure: what fraction of the gain exact knowledge of the caches
// buys over session affinity the approximate index keeps without it. Here session
// affinity scores 10, the approximation 14 and exact residency 18, so the
// approximation keeps half of an 8-request gain.
func TestTheShareOfTheGainTheApproximateIndexKeeps(t *testing.T) {
	var cs []bench.Cell
	cs = append(cs, gridCells(highPressure, policy.SessionAffinityName, 10, 10, 10)...)
	cs = append(cs, gridCells(highPressure, policy.PrefixAffinityName, 14, 14, 14)...)
	cs = append(cs, gridCells(highPressure, policy.ExactResidencyName, 18, 18, 18)...)

	m := buildMap(t, cs)
	kept := m.Points[0].GainKept(gridLoad)
	if !kept.Defined || !closeTo(kept.Share, 0.5) {
		t.Errorf("share kept = %v (defined: %v, %s), want 0.5", kept.Share, kept.Defined, kept.Why)
	}
	if !strings.Contains(section(m.Report(), gainTable), "50%") {
		t.Errorf("the report does not state the share:\n%s", m.Report())
	}
}

// A share of a gain that is inside the run-to-run spread is a share of noise, so
// none is claimed there — and the reason says so rather than printing a ratio.
func TestNoShareIsClaimedWhereExactResidencyGainedNothingBeyondTheSpread(t *testing.T) {
	var cs []bench.Cell
	cs = append(cs, gridCells(highPressure, policy.SessionAffinityName, 9.5, 10, 10.5)...)
	cs = append(cs, gridCells(highPressure, policy.PrefixAffinityName, 12, 12, 12)...)
	cs = append(cs, gridCells(highPressure, policy.ExactResidencyName, 10, 10.4, 10.8)...)

	kept := buildMap(t, cs).Points[0].GainKept(gridLoad)
	if kept.Defined {
		t.Errorf("a share of %v was claimed of a gain inside the spread", kept.Share)
	}
	if kept.Why == "" {
		t.Error("no share was claimed and nothing says why")
	}
}

// llm-d published its precise-versus-approximate result as P90 TTFT, so the
// section that answers #24 sets this run's P90s beside those figures rather than
// leaving the reader to find them.
func TestTheShareSectionSetsTheP90TTFTBesideLlmDsPublishedFigures(t *testing.T) {
	var cs []bench.Cell
	cs = append(cs, gridCells(highPressure, policy.SessionAffinityName, 10, 10, 10)...)
	cs = append(cs, gridCells(highPressure, policy.PrefixAffinityName, 14, 14, 14)...)
	cs = append(cs, gridCells(highPressure, policy.ExactResidencyName, 18, 18, 18)...)
	for i := range cs {
		cs[i].TTFTP90Ns = 700e6
	}

	got := section(buildMap(t, cs).Report(), gainTable)
	for _, want := range []string{"0.54", "31.1", "700ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("the share table does not carry %q:\n%s", want, got)
		}
	}
}

// gainTable is the header the share-of-the-gain table opens with.
const gainTable = "| point | load | share of the gain kept |"

// A difference smaller than the run-to-run spread is a difference between a
// policy and itself, and this is the figure the whole project is read off. The
// qualification travels with the number rather than being applied when it is
// printed.
func TestADeltaInsideTheSpreadIsNotSeparated(t *testing.T) {
	// The ranges overlap: 8.0-9.5 against 8.5-10.0.
	m := buildMap(t, bothPolicies(highPressure, []float64{8.0, 8.5, 9.5}, []float64{8.5, 9.0, 10.0}))
	d := m.Points[0].DeltaAt(gridLoad, m.Baseline, m.Challenger)

	if !d.Measured || !d.Replicated {
		t.Fatalf("delta is not measured or not replicated: %+v", d)
	}
	if d.Separated {
		t.Errorf("delta %s is separated, but the two ranges overlap", d)
	}
	if !strings.Contains(d.String(), "within spread") {
		t.Errorf("delta renders as %q, want it to say the difference is inside the spread", d)
	}
	if m.Separates() {
		t.Errorf("the map says it separates somewhere, on one point whose ranges overlap")
	}
}

// One repetition cannot make a claim about reproducibility, so it is never
// reported as a separation.
func TestAnUnreplicatedDeltaIsNeverSeparated(t *testing.T) {
	m := buildMap(t, bothPolicies(highPressure, []float64{8}, []float64{12}))
	d := m.Points[0].DeltaAt(gridLoad, m.Baseline, m.Challenger)
	if d.Separated {
		t.Errorf("a single repetition apiece produced a separated delta: %s", d)
	}
	if !strings.Contains(d.String(), "unreplicated") {
		t.Errorf("delta renders as %q, want it to say it is unreplicated", d)
	}
}

// A cell whose workload stated no working set is on no point of the grid.
// Averaging it into WS 0 would report an unknown pressure as a known one, and
// the fix is a flag rather than a correction to the data.
func TestCellsWithNoWorkingSetAreNotPlacedAtZero(t *testing.T) {
	cs := bothPolicies(highPressure, []float64{8, 8, 8}, []float64{9, 9, 9})
	for _, unstated := range bothPolicies(bench.GridPoint{Skew: 1.4}, []float64{5, 5, 5}, []float64{5, 5, 5}) {
		unstated.WorkingSet = 0
		cs = append(cs, unstated)
	}

	m := buildMap(t, cs)

	if len(m.Points) != 1 {
		t.Fatalf("%d points, want only the one that stated a working set", len(m.Points))
	}
	if m.Points[0].At != highPressure {
		t.Errorf("the point is %s, want %s", m.Points[0].At, highPressure)
	}
	if m.Unstated != 6 {
		t.Errorf("Unstated = %d, want the 6 cells that named no working set", m.Unstated)
	}
	if report := m.Report(); !strings.Contains(report, "-kv-capacity") {
		t.Errorf("the report does not name the flag that fixes an unstated working set")
	}
}

// A whole grid run with no working set anywhere is the one failure that produces
// a full directory of good cells and no grid at all. It is refused by name, with
// its fix, rather than rendered as an empty map.
func TestAGridWithNoWorkingSetAnywhereIsRefusedByName(t *testing.T) {
	cs := bothPolicies(bench.GridPoint{Skew: 1.4}, []float64{5, 5, 5}, []float64{5, 5, 5})
	for i := range cs {
		cs[i].WorkingSet = 0
	}
	_, err := bench.BuildPressureMap(cs)
	if err == nil {
		t.Fatal("a grid run stating no working set anywhere was mapped")
	}
	if !strings.Contains(err.Error(), "-kv-capacity") {
		t.Errorf("the refusal does not name the flag that fixes it: %v", err)
	}
}

// §6 discards flagged and unclean cells and re-runs them rather than averaging
// them in. The map inherits that from each point's own comparison, and says so.
func TestUncleanCellsAreExcludedAndNamed(t *testing.T) {
	cs := bothPolicies(highPressure, []float64{8, 8, 8}, []float64{9, 9, 9})
	cs[0].Flagged = true
	cs[0].FlagReasons = []string{"a foreign process held the card"}

	m := buildMap(t, cs)

	if len(m.Excluded) != 1 {
		t.Fatalf("%d exclusions, want the one flagged cell: %v", len(m.Excluded), m.Excluded)
	}
	if !strings.Contains(m.Excluded[0], "a foreign process held the card") {
		t.Errorf("the exclusion does not carry the cell's own reason: %q", m.Excluded[0])
	}
	// The figure is pooled over what is left, not over what was thrown away.
	if got := m.Points[0].Comparison.Rows[0].Goodput[policy.SessionAffinityName].Repetitions; got != 2 {
		t.Errorf("the baseline pooled %d repetitions, want the 2 that were not flagged", got)
	}
	if !strings.Contains(m.Report(), "a foreign process held the card") {
		t.Error("the report does not name the excluded cell")
	}
}

// A run where nothing separated and the mechanism never fired has not compared
// the policies at all. §6 says to correct the workload before touching a policy,
// and that verdict is the one thing a script should be able to act on.
func TestAFlatMapWithNoMechanismSaysToFixTheWorkload(t *testing.T) {
	m := buildMap(t, bothPolicies(lowPressure, []float64{10, 10, 10}, []float64{10, 10, 10}))

	if m.Separates() {
		t.Error("a map where both policies scored 10 everywhere says it separates")
	}
	if m.MechanismFired() {
		t.Error("a map with no prefill difference, no hit rate difference and no spill says the mechanism fired")
	}
	if !m.SuspectTheWorkload() {
		t.Error("a flat map with flat validity does not say to suspect the workload")
	}
	if report := m.Report(); !strings.Contains(report, "Correct the\nworkload before touching any policy") {
		t.Error("the report does not tell the reader to correct the workload")
	}
}

// A map that separates nowhere but whose mechanism did fire is a real null
// result — the crossover is outside this fleet's reach — and one of the two
// outcomes this project is built to publish. It must not be reported as a broken
// workload.
func TestAFlatMapWhoseMechanismFiredIsANullResultNotABrokenRun(t *testing.T) {
	cs := bothPolicies(lowPressure, []float64{10, 10, 10}, []float64{10, 10, 10})
	// The policies routed differently and left the fleet different work, and it
	// did not move goodput.
	for i := range cs {
		cs[i].PrefillRead = true
		cs[i].PromptTokens = 1_000_000
		cs[i].PromptTokensCached = 200_000
		if cs[i].Policy == policy.PrefixAffinityName {
			cs[i].PromptTokensCached = 500_000
		}
	}

	m := buildMap(t, cs)

	if m.Separates() {
		t.Error("a map where both policies scored 10 everywhere says it separates")
	}
	if !m.MechanismFired() {
		t.Error("the policies computed different prefill and the map says the mechanism did not fire")
	}
	if m.SuspectTheWorkload() {
		t.Error("a null result with live mechanism evidence is reported as a workload to fix")
	}
	if report := m.Report(); !strings.Contains(report, "null result about") {
		t.Error("the report does not read the flat map as a null result")
	}
}

// The validity condition §0 names, all three parts, read off three different
// places in the record so no single failure can flatten them together.
func TestValidityReportsPrefillCacheAndBothSpillBranches(t *testing.T) {
	cs := bothPolicies(highPressure, []float64{8, 8, 8}, []float64{9, 9, 9})
	for i := range cs {
		cs[i].PrefillRead = true
		cs[i].PromptTokens = 1_000_000
		cs[i].PromptTokensCached = 200_000
		cs[i].PrefixCacheRead = true
		cs[i].PrefixCacheQueries = 1000
		cs[i].PrefixCacheHits = 400
		if cs[i].Policy == policy.PrefixAffinityName {
			cs[i].PromptTokensCached = 500_000
			cs[i].PrefixCacheHits = 700
			cs[i].Decisions = bench.DecisionMix{PrefixAffinity: 80, SpillHitRate: 15, SpillLoad: 5}
		}
	}

	v := buildMap(t, cs).Points[0].Validity(policy.PrefixAffinityName)

	// Pooled over the three repetitions, because these are counts of work and
	// two windows of work combine by adding: 3 x 800,000 recomputed against
	// 3 x 500,000, each over 3 x 100 requests. So 8,000 tokens per request
	// against 5,000, and the 900,000-token total is that 3,000 against the 300
	// requests the policy carrying it served.
	if !v.PrefillMeasured || v.RedundantPerRequest != 3000 {
		t.Errorf("redundant prefill = %v per request (measured %v), want the 3,000 tokens per request session affinity computed over prefix affinity",
			v.RedundantPerRequest, v.PrefillMeasured)
	}
	if v.RedundantTokens != 900_000 {
		t.Errorf("redundant prefill = %v tokens, want 3,000 against the 300 requests session affinity served", v.RedundantTokens)
	}
	if !v.HitRateMeasured || !closeTo(v.HitRateSpread, 0.3) {
		t.Errorf("hit rate spread = %v (measured %v), want 70%% against 40%%", v.HitRateSpread, v.HitRateMeasured)
	}
	// Three repetitions of 15 and 5, and the two branches never merge.
	if v.SpillHitRate != 45 || v.SpillLoad != 15 {
		t.Errorf("spill = KV %d, load %d; want 45 and 15, counted apart", v.SpillHitRate, v.SpillLoad)
	}
	if !v.Fired() {
		t.Error("validity says the mechanism did not fire on a point with all three kinds of evidence")
	}
}

// An unread counter is not a zero. A point whose fleet was never scraped must not
// report that its policies computed identical prefill.
func TestAnUnreadCounterIsNotEvidenceOfNoDifference(t *testing.T) {
	v := buildMap(t, bothPolicies(highPressure, []float64{8, 8, 8}, []float64{9, 9, 9})).Points[0].Validity(policy.PrefixAffinityName)
	if v.PrefillMeasured {
		t.Error("prefill reads as measured on cells whose counters were never read")
	}
	if v.HitRateMeasured {
		t.Error("the hit rate reads as measured on cells whose caches were never scraped")
	}
	if report := buildMap(t, bothPolicies(highPressure, []float64{8, 8, 8}, []float64{9, 9, 9})).Report(); !strings.Contains(report, "| — | — |") {
		t.Error("the validity table prints an unread counter as something other than an em dash")
	}
}

// The grid exists because the two pressures drive different branches of the
// spill rule. This is the table that shows it: each axis pooled over the other,
// so each row isolates one of them.
func TestTheSeparabilityTablePoolsEachAxisOverTheOther(t *testing.T) {
	var cs []bench.Cell
	// Memory pressure at the top of the WS axis, uniform draw: the KV branch.
	kv := bothPolicies(highPressure, []float64{8, 8, 8}, []float64{9, 9, 9})
	// Load imbalance at the top of the skew axis, roomy caches: the load branch.
	load := bothPolicies(highSkew, []float64{8, 8, 8}, []float64{9, 9, 9})
	for i := range kv {
		if kv[i].Policy == policy.PrefixAffinityName {
			kv[i].Decisions = bench.DecisionMix{SpillHitRate: 20}
		}
	}
	for i := range load {
		if load[i].Policy == policy.PrefixAffinityName {
			load[i].Decisions = bench.DecisionMix{SpillLoad: 30}
		}
	}
	cs = append(cs, kv...)
	cs = append(cs, load...)

	report := buildMap(t, cs).Report()

	// The WS marginal: the KV branch fires at WS 8 and not at WS 1.
	if !strings.Contains(report, "| 8 | 60 | 0 |") {
		t.Errorf("the working set marginal does not show the KV branch firing at WS 8:\n%s", section(report, "working set (skew pooled)"))
	}
	// The skew marginal: the load branch fires at skew 1.4 and not at skew 0.
	if !strings.Contains(report, "| 1.4 | 0 | 90 |") {
		t.Errorf("the skew marginal does not show the load branch firing at skew 1.4:\n%s", section(report, "skew (working set pooled)"))
	}
}

// A point where only one policy ran is a hole in the headline figure, which is a
// result about the run. It is named rather than dropped, and it does not stop the
// rest of the map being drawn.
func TestAPointWithOnePolicyIsRefusedByNameAndDoesNotStopTheMap(t *testing.T) {
	var cs []bench.Cell
	cs = append(cs, bothPolicies(highPressure, []float64{8, 8, 8}, []float64{9, 9, 9})...)
	cs = append(cs, gridCells(lowPressure, policy.SessionAffinityName, 10, 10, 10)...)

	m := buildMap(t, cs)

	if len(m.Points) != 1 || m.Points[0].At != highPressure {
		t.Fatalf("the map has %d points, want only the one that compared two policies", len(m.Points))
	}
	if len(m.Refused) != 1 || m.Refused[0].At != lowPressure {
		t.Fatalf("refusals = %+v, want the one-policy point named", m.Refused)
	}
	if !strings.Contains(m.Report(), "Points that ran but could not be compared") {
		t.Error("the report does not list the point it could not compare")
	}
}

// A partial run reads as partial: the points of the intended grid that have no
// cells at all are named, so a map drawn from four of twelve points cannot be
// mistaken for a finished one.
func TestAPartialGridNamesThePointsItIsMissing(t *testing.T) {
	m := buildMap(t, bothPolicies(highPressure, []float64{8, 8, 8}, []float64{9, 9, 9}))

	missing := m.MissingPoints()
	if got, want := len(missing), len(bench.PressureGrid())-1; got != want {
		t.Errorf("%d missing points, want %d", got, want)
	}
	for _, at := range missing {
		if at == highPressure {
			t.Errorf("%s was run and is reported missing", at)
		}
	}
	if !strings.Contains(m.Report(), "the run is this much short of complete") {
		t.Error("the report does not say the grid is incomplete")
	}
}

// Each point checks its own cells against one SLO; nothing but this checks the
// points against each other. A map whose corners were judged against different
// thresholds would put two definitions of goodput on one figure.
func TestPointsJudgedAgainstDifferentSLOsAreNotOneMap(t *testing.T) {
	cs := bothPolicies(highPressure, []float64{8, 8, 8}, []float64{9, 9, 9})
	other := bothPolicies(lowPressure, []float64{10, 10, 10}, []float64{11, 11, 11})
	for i := range other {
		other[i].SLOTTFTNs = (2 * derivedSLO.TTFT).Nanoseconds()
	}
	cs = append(cs, other...)

	if _, err := bench.BuildPressureMap(cs); err == nil {
		t.Fatal("two points judged against two SLOs were drawn as one map")
	} else if !strings.Contains(err.Error(), "different SLOs") {
		t.Errorf("the refusal does not name the cause: %v", err)
	}
}

// The pooled decision mix has to keep every reason the mix carries. A field
// added to DecisionMix and not to the pooling would silently vanish from the
// separability table, and Total is what catches it: it sums every field, so a
// pooled total that disagrees with the cells' own totals means one was dropped.
func TestPoolingDecisionsKeepsEveryReason(t *testing.T) {
	full := bench.DecisionMix{
		RoundRobin: 1, LeastOutstanding: 2, SessionAffinity: 3, SessionUnidentified: 4,
		PrefixAffinity: 5, Cold: 6, SpillHitRate: 7, SpillLoad: 8, Undecided: 9,
	}
	cs := bothPolicies(highPressure, []float64{8, 8, 8}, []float64{9, 9, 9})
	for i := range cs {
		cs[i].Decisions = full
	}

	m := buildMap(t, cs)

	pooled := m.Points[0].Decisions[policy.PrefixAffinityName]
	if got, want := pooled.Total(), full.Total()*3; got != want {
		t.Errorf("pooled total = %d over three repetitions of %d, want %d — a reason was dropped in pooling",
			got, full.Total(), want)
	}
}

// closeTo compares two rates without demanding they be bit-identical: both sides
// come from dividing summed counters, and a hit rate is not a value to test for
// exact equality.
func closeTo(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// section is the part of a report under one heading, for a failure message that
// should show the table it is about rather than the whole document.
func section(report, heading string) string {
	at := strings.Index(report, heading)
	if at < 0 {
		return "(no section " + heading + ")"
	}
	rest := report[at:]
	if end := strings.Index(rest, "\n\n"); end > 0 {
		return rest[:end]
	}
	return rest
}
