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
	if p.PrefixCacheHitRate != nil || p.RecomputedPrefill != nil || p.RedundantPrefill != nil {
		t.Errorf("unread counters became numbers: hit %v, recomputed %v, redundant %v",
			deref(p.PrefixCacheHitRate), deref(p.RecomputedPrefill), deref(p.RedundantPrefill))
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
