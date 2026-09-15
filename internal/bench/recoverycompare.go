package bench

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yuchia329/kvroute/internal/policy"
)

// RecoveryComparison is two or more policies' runs of one chaos scenario, side
// by side: idea.md §7's chaos test as a second result rather than a graph.
//
// The prediction it exists to check is about the shape of recovery. A hash ring
// loses the cache of every session that lived on a replica that leaves, and
// loses it again when the replica returns and its sessions move back onto an
// empty cache; a prefix index follows the blocks rather than an assignment, so
// it should dip once and shallower. Whether it does is what the curves say.
type RecoveryComparison struct {
	// Runs are the chaos runs compared, one per policy, baseline first.
	Runs []ChaosRun
}

// CompareRecovery puts chaos runs of one scenario side by side, refusing runs
// that did not face the same failure.
//
// Refused rather than drawn, for the reason Compare refuses cells: two curves
// that differ in which replica died, how, when, under what load or against what
// SLO differ for reasons that are not the policy, and a table drawn from them
// would present that difference as the policy's.
func CompareRecovery(runs []ChaosRun) (RecoveryComparison, error) {
	if len(runs) < 2 {
		return RecoveryComparison{}, fmt.Errorf("bench: %d chaos run given, and a recovery comparison needs one run of each of at least two policies", len(runs))
	}
	seen := map[string]bool{}
	for _, s := range runs {
		if seen[s.Policy] {
			return RecoveryComparison{}, fmt.Errorf("bench: two chaos runs of %s given: compare one run per policy, since a second run of the same policy is a repetition and not a column", s.Policy)
		}
		seen[s.Policy] = true
		if err := sameFailure(runs[0], s); err != nil {
			return RecoveryComparison{}, err
		}
	}
	ordered := slices.Clone(runs)
	slices.SortStableFunc(ordered, func(a, b ChaosRun) int {
		return cmp.Or(cmp.Compare(policyRank(a.Policy), policyRank(b.Policy)), cmp.Compare(a.Policy, b.Policy))
	})
	return RecoveryComparison{Runs: ordered}, nil
}

// policyRank is a policy's place in the comparison order, with policies the
// order does not know placed after the ones it does.
func policyRank(name string) int {
	if i := slices.Index(policy.Order, name); i >= 0 {
		return i
	}
	return len(policy.Order)
}

// sameFailure refuses a pair of runs whose curves would differ for reasons
// other than the policy.
func sameFailure(a, b ChaosRun) error {
	switch {
	case a.Fault != b.Fault:
		return fmt.Errorf("bench: the %s run was a %s and the %s run a %s, so their curves differ by more than the policy", a.Policy, a.Fault, b.Policy, b.Fault)
	case a.Replica != b.Replica:
		return fmt.Errorf("bench: the %s run took away %s and the %s run %s, which are different failures", a.Policy, a.Replica, b.Policy, b.Replica)
	case a.ArrivalRate != b.ArrivalRate || a.ThinkTimeNs != b.ThinkTimeNs:
		return fmt.Errorf("bench: the %s run offered %g req/s with a %v think time and the %s run %g req/s with %v, and a recovery is only comparable under one load",
			a.Policy, a.ArrivalRate, time.Duration(a.ThinkTimeNs), b.Policy, b.ArrivalRate, time.Duration(b.ThinkTimeNs))
	case a.FaultAtNs != b.FaultAtNs || a.RecoverAtNs != b.RecoverAtNs || a.DurationNs != b.DurationNs || a.WarmupNs != b.WarmupNs:
		return fmt.Errorf("bench: the runs took the replica away and started it again at different times — %s faulted at %v and began recovering at %v of %v, %s at %v and %v of %v — so their curves are not aligned",
			a.Policy, time.Duration(a.FaultAtNs), time.Duration(a.RecoverAtNs), time.Duration(a.DurationNs),
			b.Policy, time.Duration(b.FaultAtNs), time.Duration(b.RecoverAtNs), time.Duration(b.DurationNs))
	case a.BucketNs != b.BucketNs || a.Tolerance != b.Tolerance:
		return fmt.Errorf("bench: the runs were read in %v and %v buckets with %.0f%% and %.0f%% tolerances, so their curves and recovery times are not measured alike",
			time.Duration(a.BucketNs), time.Duration(b.BucketNs), a.Tolerance*100, b.Tolerance*100)
	case a.SLO != b.SLO:
		return fmt.Errorf("bench: the %s run was judged against the SLO %s and the %s run against %s, and goodput is defined by the SLO", a.Policy, describeSLO(a.SLO), b.Policy, describeSLO(b.SLO))
	case a.Workload != b.Workload:
		return fmt.Errorf("bench: the %s run sent the workload %s and the %s run %s, so the difference between the curves would include a difference in prompts", a.Policy, a.Workload, b.Policy, b.Workload)
	}
	return nil
}

// Report renders the comparison: the numbers read off each run side by side,
// each run's drop accounting in full, and the curves bucket against bucket.
func (c RecoveryComparison) Report() string {
	var b strings.Builder
	first := c.Runs[0]
	names := make([]string, len(c.Runs))
	for i, s := range c.Runs {
		names[i] = s.Policy
	}
	fmt.Fprintf(&b, "# Recovery: %s %s, %s\n\n", first.Replica, first.Fault.Verb(), strings.Join(names, " against "))
	b.WriteString(first.setting() + "\n\n")

	b.WriteString("| |")
	for _, name := range names {
		fmt.Fprintf(&b, " %s |", name)
	}
	b.WriteString("\n|---|" + strings.Repeat("---:|", len(names)) + "\n")
	c.row(&b, "left rotation", func(s ChaosRun) string { return eventTime(s, EventOutOfRotation) })
	c.row(&b, "back in rotation", func(s ChaosRun) string { return eventTime(s, EventInRotation) })
	c.row(&b, "goodput before the fault", func(s ChaosRun) string { return fmt.Sprintf("%.2f/s", s.Recovery.BaselineRPS) })
	c.row(&b, "lowest from the fault on", func(s ChaosRun) string {
		return fmt.Sprintf("%.2f/s at %s", s.Recovery.TroughRPS, signed(s.Recovery.TroughAtNs))
	})
	c.row(&b, "deficit", func(s ChaosRun) string { return requestCount(s.Recovery.DeficitRequests) })
	c.row(&b, fmt.Sprintf("back within %.0f%% for good", first.Tolerance*100), func(s ChaosRun) string {
		if !s.Recovery.Steady {
			return "not before the run ended"
		}
		return "from " + signed(s.Recovery.SteadyAtNs)
	})
	c.row(&b, "rerouted", func(s ChaosRun) string { return fmt.Sprint(s.Summary.Rerouted) })
	c.row(&b, "dropped mid-stream", func(s ChaosRun) string { return fmt.Sprint(s.DroppedMidStream) })
	c.row(&b, "dropped, never placed", func(s ChaosRun) string { return fmt.Sprint(s.DroppedUnplaced) })

	b.WriteString("\n## Drops\n\n")
	for _, s := range c.Runs {
		fmt.Fprintf(&b, "- **%s**: %s\n", s.Policy, s.DropAccounting())
	}

	b.WriteString("\n## Curves\n\n| from the fault |")
	for _, name := range names {
		fmt.Fprintf(&b, " %s goodput/s |", name)
	}
	b.WriteString("\n|---:|" + strings.Repeat("---:|", len(names)) + "\n")
	for _, at := range c.buckets() {
		fmt.Fprintf(&b, "| %s |", signed(at))
		for _, s := range c.Runs {
			i := slices.IndexFunc(s.Curve, func(p CurvePoint) bool { return p.AtNs == at })
			if i < 0 {
				b.WriteString(" — |")
				continue
			}
			fmt.Fprintf(&b, " %.2f |", s.Curve[i].GoodputRPS)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// row writes one line of the side-by-side table.
func (c RecoveryComparison) row(b *strings.Builder, label string, cell func(ChaosRun) string) {
	fmt.Fprintf(b, "| %s |", label)
	for _, s := range c.Runs {
		fmt.Fprintf(b, " %s |", cell(s))
	}
	b.WriteString("\n")
}

// buckets is every bucket any run's curve has, in order. A bucket one run has
// and another lacks is printed as a gap in the second, never as a zero.
func (c RecoveryComparison) buckets() []int64 {
	var at []int64
	for _, s := range c.Runs {
		for _, p := range s.Curve {
			at = append(at, p.AtNs)
		}
	}
	slices.Sort(at)
	return slices.Compact(at)
}

// eventTime is when an event first happened in a run, or a dash if it did not.
func eventTime(s ChaosRun, kind EventKind) string {
	at, ok := s.After(kind)
	if !ok {
		return "—"
	}
	return signed(at.Nanoseconds())
}

// requestCount renders a number of requests, rounded to whole ones.
func requestCount(n float64) string {
	if rounded := fmt.Sprintf("%.0f", n); rounded != "1" {
		return rounded + " requests"
	}
	return "1 request"
}
