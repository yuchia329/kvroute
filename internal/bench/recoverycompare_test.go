package bench_test

import (
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
)

// assessed is one policy's kill scenario, with the second the fault landed in
// supplied by the test.
func assessed(policyName string, faultSecond ...bench.Result) bench.ChaosRun {
	s := chaosRun(bench.FaultKill)
	s.Policy = policyName
	s.Assess(healthyThenFault(faultSecond...), []bench.Event{
		{AtNs: 0, Kind: bench.EventFault},
		{AtNs: (150 * time.Millisecond).Nanoseconds(), Kind: bench.EventOutOfRotation, Detail: "ejected"},
	})
	return s
}

// Criterion 8: the scenario runs under both session affinity and prefix affinity
// and the two recovery curves are compared. idea.md §7 is what turns that from a
// graph into a result — a hash ring rehashes and loses its cache when the fleet
// changes, and a prefix index degrades gracefully — so the comparison puts the
// two curves in one table, bucket against bucket, beside the numbers read off
// each.
func TestTwoPoliciesRecoveryCurvesAreComparedSideBySide(t *testing.T) {
	fault := chaosStart.Add(2 * time.Second)
	sticky := assessed(policy.SessionAffinityName,
		dropped(fault, "replica-2"), dropped(fault.Add(200*time.Millisecond), "replica-2"),
		dropped(fault.Add(400*time.Millisecond), "replica-2"), success(fault.Add(600*time.Millisecond), time.Second))
	index := assessed(policy.PrefixAffinityName,
		dropped(fault, "replica-2"), success(fault.Add(200*time.Millisecond), time.Second),
		success(fault.Add(400*time.Millisecond), time.Second), success(fault.Add(600*time.Millisecond), time.Second))

	// Given out of order, to check the comparison puts the baseline first.
	c, err := bench.CompareRecovery([]bench.ChaosRun{index, sticky})
	if err != nil {
		t.Fatalf("two runs of one scenario were refused: %v", err)
	}
	report := c.Report()

	if strings.Index(report, policy.SessionAffinityName) > strings.Index(report, policy.PrefixAffinityName) {
		t.Error("prefix affinity is reported before the session affinity baseline it is compared against")
	}
	// The fault's bucket, one policy's goodput beside the other's: one request of
	// four met the SLO under the ring, three under the index.
	if !strings.Contains(report, "| +0.00s | 1.00 | 3.00 |") {
		t.Errorf("the curves are not side by side at the fault:\n%s", report)
	}
	// Each run's deficit, and each run's drop accounting in full: a comparison is
	// no place to shorten the drop claim back to a bare number.
	for _, want := range []string{"| 3 requests |", "| 1 request |", sticky.DropAccounting(), index.DropAccounting()} {
		if !strings.Contains(report, want) {
			t.Errorf("the comparison does not say %q", want)
		}
	}
}

// A recovery comparison is refused, rather than drawn, whenever the two runs did
// not face the same failure. Two curves that differ in which replica died, how,
// when, under what load or against what SLO differ for reasons that are not the
// policy, and a table drawn from them would present that difference as the
// policy's.
func TestRecoveryRunsThatFacedDifferentFailuresAreNotCompared(t *testing.T) {
	fault := chaosStart.Add(2 * time.Second)
	base := func(policyName string) bench.ChaosRun {
		return assessed(policyName, offeredAt(fault, 4, record.OutcomeSuccess)...)
	}

	cases := map[string]struct {
		change func(*bench.ChaosRun)
		want   string
	}{
		"different fault":   {func(s *bench.ChaosRun) { s.Fault = bench.FaultDrain }, "drain"},
		"different replica": {func(s *bench.ChaosRun) { s.Replica = "replica-4" }, "replica-4"},
		"different load":    {func(s *bench.ChaosRun) { s.ArrivalRate = 8 }, "8"},
		"different timing":  {func(s *bench.ChaosRun) { s.RecoverAtNs += time.Second.Nanoseconds() }, "recover"},
		"different SLO":     {func(s *bench.ChaosRun) { s.SLO.TTFT *= 2 }, "SLO"},
		"different bytes":   {func(s *bench.ChaosRun) { s.Workload = "multiturn(other)" }, "workload"},
		"the same policy":   {func(s *bench.ChaosRun) { s.Policy = policy.SessionAffinityName }, policy.SessionAffinityName},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			other := base(policy.PrefixAffinityName)
			tc.change(&other)
			_, err := bench.CompareRecovery([]bench.ChaosRun{base(policy.SessionAffinityName), other})
			if err == nil {
				t.Fatal("two runs that did not face the same failure were compared")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to name %q", err, tc.want)
			}
		})
	}

	if _, err := bench.CompareRecovery([]bench.ChaosRun{base(policy.SessionAffinityName)}); err == nil {
		t.Error("one run was accepted as a comparison")
	}
}
