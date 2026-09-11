package bench_test

import (
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/record"
)

// offeredAt builds n rows offered at t, each ending as the outcome says.
func offeredAt(t time.Time, n int, o record.Outcome) []bench.Result {
	rows := make([]bench.Result, 0, n)
	for i := range n {
		// Spread inside the bucket, as an even arrival schedule would.
		at := t.Add(time.Duration(i) * 200 * time.Millisecond)
		if o == record.OutcomeSuccess {
			rows = append(rows, success(at, time.Second))
		} else {
			rows = append(rows, outcome(at, o))
		}
	}
	return rows
}

// The curve is goodput in buckets of when each request was offered, read
// relative to the fault, with the losses beside it bucket by bucket. The
// criterion it answers is that goodput is sampled continuously through the
// failure and the recovery rather than read at two endpoints, so the test
// checks the buckets one by one against figures worked out by hand.
func TestTheRecoveryCurveIsGoodputBucketedAroundTheFault(t *testing.T) {
	fault := time.Unix(1757000000, 0)
	second := time.Second
	var rows []bench.Result
	// Two seconds of a healthy fleet: four requests a second, all inside the SLO.
	rows = append(rows, offeredAt(fault.Add(-2*second), 4, record.OutcomeSuccess)...)
	rows = append(rows, offeredAt(fault.Add(-1*second), 4, record.OutcomeSuccess)...)
	// The second the replica dies: two requests lost mid-stream, one rerouted and
	// answered, one untouched.
	rows = append(rows, offeredAt(fault, 2, record.OutcomeDropped)...)
	rerouted := success(fault.Add(600*time.Millisecond), time.Second)
	rerouted.Reroutes = 1
	rows = append(rows, rerouted, success(fault.Add(800*time.Millisecond), time.Second))
	// Then one SLO violation among three that met it.
	slow := success(fault.Add(1*second), 9*time.Second)
	rows = append(rows, slow)
	rows = append(rows, offeredAt(fault.Add(1*second+200*time.Millisecond), 3, record.OutcomeSuccess)...)
	// A second in which the driver offered nothing at all, which must show as a
	// gap in the curve rather than be closed over.
	// Then the fleet whole again.
	rows = append(rows, offeredAt(fault.Add(3*second), 4, record.OutcomeSuccess)...)
	// Warm-up is excluded from the curve as it is from every other figure.
	warm := success(fault.Add(-5*second), time.Second)
	warm.Warmup = true
	rows = append(rows, warm)

	curve := bench.RecoveryCurve(rows, fault, second, slo)

	want := []struct {
		at                                 time.Duration
		offered, rerouted, dropped, missed int
		goodput                            float64
	}{
		{at: -2 * second, offered: 4, goodput: 4},
		{at: -1 * second, offered: 4, goodput: 4},
		{at: 0, offered: 4, rerouted: 1, dropped: 2, goodput: 2},
		{at: 1 * second, offered: 4, missed: 1, goodput: 3},
		{at: 2 * second, offered: 0, goodput: 0},
		{at: 3 * second, offered: 4, goodput: 4},
	}
	if len(curve) != len(want) {
		t.Fatalf("curve has %d points, want %d: %+v", len(curve), len(want), curve)
	}
	for i, w := range want {
		got := curve[i]
		if time.Duration(got.AtNs) != w.at {
			t.Errorf("point %d is at %v, want %v", i, time.Duration(got.AtNs), w.at)
		}
		if got.Offered != w.offered || got.Rerouted != w.rerouted || got.Dropped != w.dropped || got.SLOViolations != w.missed {
			t.Errorf("point at %v: offered=%d rerouted=%d dropped=%d violations=%d, want %d/%d/%d/%d",
				w.at, got.Offered, got.Rerouted, got.Dropped, got.SLOViolations, w.offered, w.rerouted, w.dropped, w.missed)
		}
		if got.GoodputRPS != w.goodput {
			t.Errorf("point at %v: goodput %.2f/s, want %.2f/s", w.at, got.GoodputRPS, w.goodput)
		}
	}
}

// The curve read as numbers: what the fleet did before the fault, how far it
// fell, how much it lost in all, and when it was back for good.
//
// "For good" is the part that is easy to get wrong. The two affinity policies
// are predicted to differ twice — once when the replica leaves, and again when
// it returns and session affinity's ring moves its sessions back onto an empty
// cache — so a recovery time read as the first moment goodput looked healthy
// would stop the clock between the two dips and report the policy that dips
// twice as having recovered as fast as the one that dips once.
func TestRecoveryIsMeasuredToTheLastDipNotTheFirst(t *testing.T) {
	curve := goodputCurve(
		-3, 4, // the baseline: 4/s before the fault
		-2, 4,
		-1, 4,
		0, 1, // the replica dies
		1, 4, // looks recovered...
		2, 2, // ...until it comes back and its sessions move onto an empty cache
		3, 4,
		4, 3.8, // within 10% of the baseline, which is steady
	)

	got := bench.MeasureRecovery(curve, time.Second, 0.10)

	if got.BaselineRPS != 4 {
		t.Errorf("baseline = %.2f/s, want 4", got.BaselineRPS)
	}
	if got.TroughRPS != 1 || time.Duration(got.TroughAtNs) != 0 {
		t.Errorf("trough = %.2f/s at %v, want 1/s at the fault", got.TroughRPS, time.Duration(got.TroughAtNs))
	}
	// (4-1) + (4-2) + (4-3.8) requests below the baseline, one second at a time.
	if want := 5.2; got.DeficitRequests < want-1e-9 || got.DeficitRequests > want+1e-9 {
		t.Errorf("deficit = %.2f requests, want %.2f", got.DeficitRequests, want)
	}
	if !got.Steady || time.Duration(got.SteadyAtNs) != 3*time.Second {
		t.Errorf("steady=%v at %v, want steady from 3s, after the second dip", got.Steady, time.Duration(got.SteadyAtNs))
	}
}

// A request offered just before the fault and lost to it lands, by the time it
// was offered, in the bucket before the fault. That bucket belongs to the fault
// rather than to the baseline: counted into the baseline it would lower the bar
// recovery is measured against, and hide from the deficit exactly the requests
// the fault cost in flight — which a kill always costs, and which differ by
// policy with how much each had on the replica that died. Found by a local run
// whose one dropped request left its deficit reading zero.
func TestRequestsInFlightWhenTheFaultCameCountAgainstItNotTheBaseline(t *testing.T) {
	curve := goodputCurve(
		-3, 4,
		-2, 4,
		-1, 2, // the bucket the fault came at the end of: two of its requests were still streaming
		0, 4,
		1, 4,
	)

	got := bench.MeasureRecovery(curve, time.Second, 0.10)

	if got.BaselineRPS != 4 {
		t.Errorf("baseline = %.2f/s, want 4: the bucket in flight at the fault is not the healthy fleet", got.BaselineRPS)
	}
	if got.DeficitRequests != 2 {
		t.Errorf("deficit = %.2f requests, want the 2 the fault cost in flight", got.DeficitRequests)
	}
	if got.TroughRPS != 2 || time.Duration(got.TroughAtNs) != -time.Second {
		t.Errorf("trough = %.2f/s at %v, want 2/s in the bucket the fault cut short", got.TroughRPS, time.Duration(got.TroughAtNs))
	}
}

// A run that ends still below its baseline did not recover, and says so rather
// than reporting the last bucket as the moment it did.
func TestARunThatEndsBelowItsBaselineDidNotRecover(t *testing.T) {
	curve := goodputCurve(-2, 4, -1, 4, 0, 1, 1, 2, 2, 3)

	got := bench.MeasureRecovery(curve, time.Second, 0.10)

	if got.Steady {
		t.Errorf("a run that ended at 3/s against a 4/s baseline reported steady from %v", time.Duration(got.SteadyAtNs))
	}
}

// goodputCurve builds a curve from (seconds from the fault, goodput) pairs.
func goodputCurve(pairs ...float64) []bench.CurvePoint {
	var curve []bench.CurvePoint
	for i := 0; i+1 < len(pairs); i += 2 {
		curve = append(curve, bench.CurvePoint{
			AtNs:       (time.Duration(pairs[i]) * time.Second).Nanoseconds(),
			GoodputRPS: pairs[i+1],
		})
	}
	return curve
}
