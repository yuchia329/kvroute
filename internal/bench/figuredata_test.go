package bench_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// pointOf finds one policy's point in a figure, at its only load.
func pointOf(t *testing.T, f bench.ComparisonFigure, name string) bench.PolicyPoint {
	t.Helper()
	for _, p := range f.Points {
		if p.Policy == name {
			return p
		}
	}
	t.Fatalf("no point for %s in %+v", name, f.Points)
	return bench.PolicyPoint{}
}

// A point past saturation is drawn where the table puts it: its goodput, marked
// for the plot to ring, and no latency, which the table prints as an em dash.
func TestAPointPastSaturationIsMarkedAndHasNoLatency(t *testing.T) {
	at16 := bench.OpenLoopAt(16)
	var cs []bench.Cell
	for i := range 3 {
		cs = append(cs, saturatedCell(policy.RoundRobinName, at16, i+1, 0))
	}
	cs = append(cs, cells(policy.SessionAffinityName, at16, 12, 12, 12)...)

	rr := pointOf(t, compare(t, cs).Figure(), policy.RoundRobinName)

	if rr.Saturated != 3 {
		t.Errorf("round robin's point marks %d repetitions past saturation, want 3", rr.Saturated)
	}
	if rr.TTFTP50Ms != nil || rr.TTFTP99Ms != nil {
		t.Errorf("round robin's point has TTFT %v / %v, want none", deref(rr.TTFTP50Ms), deref(rr.TTFTP99Ms))
	}
}

// TestTheComparisonFigureCarriesTheTablesNumbers. The plot is drawn from the
// same arithmetic as the table beside it, so the figure data read off the
// hand-worked fixture carries the figures worked by hand.
func TestTheComparisonFigureCarriesTheTablesNumbers(t *testing.T) {
	cs, err := bench.LoadCells("testdata/sweep")
	if err != nil {
		t.Fatal(err)
	}
	f := compare(t, cs).Figure()

	rr := pointOf(t, f, policy.RoundRobinName)
	if rr.Driver != "closed-loop" || rr.Load != 32 || rr.LoadLabel != "32 users" {
		t.Errorf("round robin is at %s %v %q, want closed-loop, 32, \"32 users\"", rr.Driver, rr.Load, rr.LoadLabel)
	}
	if rr.GoodputMedian != 5 || rr.GoodputMin != 4 || rr.GoodputMax != 6 || rr.Repetitions != 3 {
		t.Errorf("round robin goodput = %+v, want median 5 over 4–6, n=3", rr)
	}
	if rr.PrefixCacheHitRate == nil || *rr.PrefixCacheHitRate != 0.1 || rr.RecomputedPrefill == nil || *rr.RecomputedPrefill != 2700 ||
		rr.RedundantPrefill == nil || *rr.RedundantPrefill != 2100 {
		t.Errorf("round robin mechanism = hit %v, recomputed %v, redundant %v; want 0.1, 2700, 2100",
			deref(rr.PrefixCacheHitRate), deref(rr.RecomputedPrefill), deref(rr.RedundantPrefill))
	}
	// The per-request figures a plot reads, and the denominator they are checkable
	// against: 2,700 tokens over 300 requests, 7 of each request's 9 redundant.
	if rr.Requests == nil || *rr.Requests != 300 || rr.RecomputedPrefillPerRequest == nil || *rr.RecomputedPrefillPerRequest != 9 ||
		rr.RedundantPrefillPerRequest == nil || *rr.RedundantPrefillPerRequest != 7 {
		t.Errorf("round robin per request = %v requests, recomputed %v, redundant %v; want 300, 9, 7",
			deref(rr.Requests), deref(rr.RecomputedPrefillPerRequest), deref(rr.RedundantPrefillPerRequest))
	}

	prefix := pointOf(t, f, policy.PrefixAffinityName)
	if prefix.GoodputMedian != 10 || prefix.OverFailureThreshold != 1 {
		t.Errorf("prefix affinity = %+v, want median 10 with one failing repetition", prefix)
	}
	if prefix.PrefixCacheHitRate == nil || *prefix.PrefixCacheHitRate != 0.8 || prefix.RedundantPrefill == nil || *prefix.RedundantPrefill != 0 {
		t.Errorf("prefix affinity mechanism = hit %v, redundant %v; want 0.8 and 0, the floor",
			deref(prefix.PrefixCacheHitRate), deref(prefix.RedundantPrefill))
	}

	if len(f.Excluded) != 2 || len(f.Surfaced) != 1 {
		t.Errorf("excluded %v and surfaced %v, want 2 and 1", f.Excluded, f.Surfaced)
	}
	if f.SLO.TTFTMs != 990 || f.SLO.ITLMs != 24 {
		t.Errorf("SLO = %+v, want 990 ms and 24 ms", f.SLO)
	}
}

// TestAnUnreadCounterIsNullInTheFigureNotZero. A plot that drew an unscraped
// fleet's hit rate at zero would show a cache that never hit, which is a claim
// nobody measured.
func TestAnUnreadCounterIsNullInTheFigureNotZero(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	var cs []bench.Cell
	cs = append(cs, cells(policy.RoundRobinName, at8, 8)...)
	cs = append(cs, cells(policy.LeastOutstandingName, at8, 9)...)

	f := compare(t, cs).Figure()

	p := pointOf(t, f, policy.RoundRobinName)
	if p.PrefixCacheHitRate != nil || p.RecomputedPrefill != nil || p.RedundantPrefill != nil ||
		p.RecomputedPrefillPerRequest != nil || p.RedundantPrefillPerRequest != nil {
		t.Errorf("unread counters became numbers: hit %v, recomputed %v (%v per request), redundant %v (%v per request)",
			deref(p.PrefixCacheHitRate), deref(p.RecomputedPrefill), deref(p.RecomputedPrefillPerRequest),
			deref(p.RedundantPrefill), deref(p.RedundantPrefillPerRequest))
	}
	encoded, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"prefix_cache_hit_rate":null`) {
		t.Errorf("an unread hit rate is not null in the JSON the plot reads: %s", encoded)
	}
}

// TestThePressureMapFigureCarriesEachDeltaQualifiedAsTheTableIs. The headline
// figure is coloured by the delta and annotated with what the repetitions can
// support, in the same words the table uses, so the picture cannot call a
// difference inside the spread a result.
func TestThePressureMapFigureCarriesEachDeltaQualifiedAsTheTableIs(t *testing.T) {
	var cs []bench.Cell
	cs = append(cs, bothPolicies(lowPressure, []float64{10, 11, 12}, []float64{11, 12, 13})...)
	cs = append(cs, bothPolicies(highSkew, []float64{6, 7, 8}, []float64{20, 21, 22})...)

	f := buildMap(t, cs).Figure()

	if f.Baseline != policy.SessionAffinityName || f.Challenger != policy.PrefixAffinityName {
		t.Errorf("the pair is %s against %s", f.Challenger, f.Baseline)
	}
	if len(f.WorkingSets) != 2 || f.WorkingSets[0] != 0.25 || f.WorkingSets[1] != 1 ||
		len(f.Skews) != 2 || f.Skews[0] != 0 || f.Skews[1] != 1.4 {
		t.Errorf("axes are WS %v and skew %v, want the two points' own: 0.25, 1 and 0, 1.4", f.WorkingSets, f.Skews)
	}
	if len(f.Deltas) != 2 {
		t.Fatalf("%d deltas, want one per point", len(f.Deltas))
	}
	low, high := f.Deltas[0], f.Deltas[1]
	if low.WorkingSet != 0.25 || low.Label != "+9.1% (within spread)" || low.Separated {
		t.Errorf("low pressure = %+v, want +9.1%% within spread, not separated", low)
	}
	if high.WorkingSet != 1 || high.Skew != 1.4 || high.Label != "+200.0%" || !high.Separated || high.PercentChange != 200 {
		t.Errorf("high skew = %+v, want +200%%, separated", high)
	}
	if len(f.Goodput) != 4 {
		t.Errorf("%d goodput points, want both policies at both points", len(f.Goodput))
	}
	encoded, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"working_set":1,"skew":1.4,"policy":"prefix_affinity"`) {
		t.Errorf("a goodput point does not carry its grid point beside its policy: %s", encoded)
	}
}

// TestTheRegimeMapFigureCarriesTheTilesLabelAndFlagsAsTheReportDoes. The
// regime map's figure is a view over the same comparison the report reads, so
// its tile's label and flags have to be the words the report's headline
// prints — the same discipline the pressure map figure above is held to.
func TestTheRegimeMapFigureCarriesTheTilesLabelAndFlagsAsTheReportDoes(t *testing.T) {
	m := buildMap(t, bothPolicies(highPressure, []float64{8, 8, 8}, []float64{20, 21, 22}))
	regime := m.Regime()
	f := regime.Figure()

	if len(f.Tiles) != 1 {
		t.Fatalf("%d tiles, want one", len(f.Tiles))
	}
	tile := f.Tiles[0]
	report := regime.Report()
	if !strings.Contains(report, tile.Label) {
		t.Errorf("the figure's label %q does not appear in the report", tile.Label)
	}
	if tile.Winner != policy.PrefixAffinityName || !tile.Measured || tile.WithinSpread {
		t.Errorf("tile = %+v, want prefix affinity measured and not within spread", tile)
	}
	if len(tile.Goodput) != 2 {
		t.Errorf("%d goodput points, want both policies' full points", len(tile.Goodput))
	}
	encoded, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"x":"0"`, `"y":"8"`, `"winner":"prefix_affinity"`, `"within_spread":false`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("encoded figure does not carry %q: %s", want, encoded)
		}
	}
}

// TestTheRecoveryFigureCarriesEachCurveAndWhatHappenedAlongIt. The recovery
// graph is the curves against time from the fault, with the replica's events
// marked on them and each run's drops beside it.
func TestTheRecoveryFigureCarriesEachCurveAndWhatHappenedAlongIt(t *testing.T) {
	fault := chaosStart.Add(2 * time.Second)
	sticky := assessed(policy.SessionAffinityName,
		dropped(fault, "replica-2"), dropped(fault.Add(200*time.Millisecond), "replica-2"),
		dropped(fault.Add(400*time.Millisecond), "replica-2"), success(fault.Add(600*time.Millisecond), time.Second))
	index := assessed(policy.PrefixAffinityName,
		dropped(fault, "replica-2"), success(fault.Add(200*time.Millisecond), time.Second),
		success(fault.Add(400*time.Millisecond), time.Second), success(fault.Add(600*time.Millisecond), time.Second))
	c, err := bench.CompareRecovery([]bench.ChaosRun{index, sticky})
	if err != nil {
		t.Fatal(err)
	}

	f := c.Figure()

	if len(f.Runs) != 2 || f.Runs[0].Policy != policy.SessionAffinityName {
		t.Fatalf("runs = %+v, want session affinity first", f.Runs)
	}
	atFault := func(run bench.RecoveryRunFigure) float64 {
		for _, p := range run.Curve {
			if p.AtS == 0 {
				return p.GoodputRPS
			}
		}
		t.Fatalf("%s has no bucket at the fault", run.Policy)
		return 0
	}
	if got := atFault(f.Runs[0]); got != 1 {
		t.Errorf("session affinity at the fault = %v goodput/s, want 1", got)
	}
	if got := atFault(f.Runs[1]); got != 3 {
		t.Errorf("prefix affinity at the fault = %v goodput/s, want 3", got)
	}
	if drops := f.Runs[0].DroppedMidStream + f.Runs[0].DroppedUnplaced; drops != 3 {
		t.Errorf("session affinity dropped %d, want 3", drops)
	}
	if drops := f.Runs[1].DroppedMidStream + f.Runs[1].DroppedUnplaced; drops != 1 {
		t.Errorf("prefix affinity dropped %d, want 1", drops)
	}
	var ejected bool
	for _, e := range f.Runs[0].Events {
		if e.Kind == string(bench.EventOutOfRotation) && e.AtS == 0.15 && e.Label == "out of rotation" {
			ejected = true
		}
	}
	if !ejected {
		t.Errorf("the ejection 150 ms after the fault is not marked: %+v", f.Runs[0].Events)
	}
	if f.Replica != "replica-2" || f.Fault != "kill" {
		t.Errorf("the scenario is %s of %s, want a kill of replica-2", f.Fault, f.Replica)
	}
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
