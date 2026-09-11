package bench_test

import (
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/record"
)

// chaosStart is when the runs below began; the fault is two seconds in.
var chaosStart = time.Unix(1757000000, 0)

func chaosRun(fault bench.Fault) bench.ChaosRun {
	return bench.ChaosRun{
		Policy:      "session_affinity",
		Replica:     "replica-2",
		Fault:       fault,
		ArrivalRate: 4,
		StartedAtNs: chaosStart.UnixNano(),
		DurationNs:  (4 * time.Second).Nanoseconds(),
		FaultAtNs:   (2 * time.Second).Nanoseconds(),
		RecoverAtNs: (3 * time.Second).Nanoseconds(),
		BucketNs:    time.Second.Nanoseconds(),
		Tolerance:   0.10,
		SLO:         slo,
	}
}

// healthyThenFault is two healthy seconds of four requests each, then the rows
// the test supplies for the second the fault landed in, then a healthy second.
func healthyThenFault(faultSecond ...bench.Result) []bench.Result {
	fault := chaosStart.Add(2 * time.Second)
	var rows []bench.Result
	rows = append(rows, offeredAt(chaosStart, 4, record.OutcomeSuccess)...)
	rows = append(rows, offeredAt(chaosStart.Add(time.Second), 4, record.OutcomeSuccess)...)
	rows = append(rows, faultSecond...)
	rows = append(rows, offeredAt(fault.Add(time.Second), 4, record.OutcomeSuccess)...)
	return rows
}

func reroutedAt(t time.Time) bench.Result {
	r := success(t, time.Second)
	r.Replica, r.Reroutes = "replica-0", 1
	return r
}

// dropped builds a dropped row, placed on a replica or not. A dropped request the
// router named a replica for is one whose replica was lost after it began; one it
// named none for is one it never placed.
func dropped(t time.Time, replica string) bench.Result {
	r := outcome(t, record.OutcomeDropped)
	r.Replica = replica
	return r
}

// idea.md §7 and criterion 5: a count of dropped requests is not a claim on its
// own. After a kill, the figure means something only beside the number of
// requests the router rerouted — the ones that would have been dropped had it
// not — and a zero means only that no request happened to be streaming at the
// instant the replica died. The report says so every time, in the same breath
// as the number.
func TestAKillWithNoDropsStillReportsWhatWasReroutedAndWhyZeroIsNotAGuarantee(t *testing.T) {
	fault := chaosStart.Add(2 * time.Second)
	s := chaosRun(bench.FaultKill)
	s.Assess(healthyThenFault(
		reroutedAt(fault), reroutedAt(fault.Add(200*time.Millisecond)),
		success(fault.Add(400*time.Millisecond), time.Second), success(fault.Add(600*time.Millisecond), time.Second),
	), nil)

	claim := s.DropAccounting()
	for _, want := range []string{"0 of 16", "2 were rerouted", "streaming"} {
		if !strings.Contains(claim, want) {
			t.Errorf("drop accounting %q does not say %q", claim, want)
		}
	}
	report := s.Report()
	if !strings.Contains(report, claim) {
		t.Error("the report does not carry the drop accounting")
	}
	for _, bare := range []string{"zero dropped", "Zero dropped", "no dropped", "No dropped"} {
		if strings.Contains(report, bare) {
			t.Errorf("the report makes the unqualified claim %q", bare)
		}
	}
}

// Drops are split by whether the router had placed the request. One placed on a
// replica that then died was streaming and could not be saved; one never placed
// found nowhere to go at all, which is a different failure — a fleet with no
// replica left in rotation — and the two are never summed into one reason.
func TestDropsAreSplitByWhetherTheRequestWasEverPlaced(t *testing.T) {
	fault := chaosStart.Add(2 * time.Second)
	s := chaosRun(bench.FaultKill)
	s.Assess(healthyThenFault(
		dropped(fault, "replica-2"),
		dropped(fault.Add(200*time.Millisecond), "replica-2"),
		dropped(fault.Add(400*time.Millisecond), ""),
		success(fault.Add(600*time.Millisecond), time.Second),
	), nil)

	if s.DroppedMidStream != 2 || s.DroppedUnplaced != 1 {
		t.Errorf("dropped mid-stream=%d unplaced=%d, want 2 and 1", s.DroppedMidStream, s.DroppedUnplaced)
	}
	claim := s.DropAccounting()
	for _, want := range []string{"3 of 16", "2 were streaming", "1 was never placed"} {
		if !strings.Contains(claim, want) {
			t.Errorf("drop accounting %q does not say %q", claim, want)
		}
	}
}

// Criterion 2's own claim. A drained replica finishes what it holds, so a drain
// can report zero drops — but it reports why, because the zero is the drain's
// doing and would not survive the same replica being killed.
func TestADrainsZeroDropsNamesTheDrainAsTheReason(t *testing.T) {
	fault := chaosStart.Add(2 * time.Second)
	s := chaosRun(bench.FaultDrain)
	s.Assess(healthyThenFault(offeredAt(fault, 4, record.OutcomeSuccess)...), nil)

	claim := s.DropAccounting()
	for _, want := range []string{"0 of 16", "drained"} {
		if !strings.Contains(claim, want) {
			t.Errorf("drop accounting %q does not say %q", claim, want)
		}
	}
}

// What happened to the replica is timed from the fault, so how long the router
// took to notice and how long the replica was gone read straight off the report.
func TestTheReportTimesWhatHappenedToTheReplicaFromTheFault(t *testing.T) {
	s := chaosRun(bench.FaultKill)
	s.Assess(healthyThenFault(offeredAt(chaosStart.Add(2*time.Second), 4, record.OutcomeSuccess)...), []bench.Event{
		{AtNs: 0, Kind: bench.EventFault, Detail: "ops/replica.sh kill 2"},
		{AtNs: (120 * time.Millisecond).Nanoseconds(), Kind: bench.EventOutOfRotation, Detail: "ejected"},
		{AtNs: (61500 * time.Millisecond).Nanoseconds(), Kind: bench.EventInRotation, Detail: "readmitted"},
	})

	if got, ok := s.After(bench.EventOutOfRotation); !ok || got != 120*time.Millisecond {
		t.Errorf("out of rotation after %v (found=%v), want 120ms", got, ok)
	}
	report := s.Report()
	for _, want := range []string{"+0.12s", "ejected", "+61.50s", "readmitted"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not show %q:\n%s", want, report)
		}
	}
}

// A drain that ran out of time stopped its replica with requests still on it.
// The drain's whole promise is that nothing is lost, so a run whose drain broke
// that promise says so, rather than claiming — as a finished drain may — that
// every request the replica held finished on it.
func TestADrainThatRanOutOfTimeDoesNotClaimEverythingFinished(t *testing.T) {
	fault := chaosStart.Add(2 * time.Second)
	s := chaosRun(bench.FaultDrain)
	s.Assess(healthyThenFault(
		dropped(fault, "replica-2"), success(fault.Add(200*time.Millisecond), time.Second),
		success(fault.Add(400*time.Millisecond), time.Second), success(fault.Add(600*time.Millisecond), time.Second),
	), []bench.Event{
		{AtNs: 0, Kind: bench.EventFault, Detail: "drain requested"},
		{AtNs: (2 * time.Minute).Nanoseconds(), Kind: bench.EventDrainTimedOut, Detail: "1 still in flight"},
	})

	claim := s.DropAccounting()
	if strings.Contains(claim, "finished on it") {
		t.Errorf("a drain that ran out of time claims everything finished: %q", claim)
	}
	if !strings.Contains(claim, "did not finish") {
		t.Errorf("drop accounting %q does not say the drain did not finish", claim)
	}
}
