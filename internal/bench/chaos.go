package bench

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
)

// Fault is how a chaos run takes its replica away.
//
// Two, and exercised separately, because they test different things and would
// hide each other's numbers if run as one. A kill forces the reroute decision
// and produces the drops nothing can save; a drain should cost nothing at all,
// and is the check that it does.
type Fault string

const (
	// FaultKill kills the replica's process outright, with requests in flight.
	FaultKill Fault = "kill"
	// FaultDrain drains the replica through the router, waits for what it holds
	// to finish, and only then stops its process.
	FaultDrain Fault = "drain"
)

// Verb is what the fault did to its replica, as a report says it.
func (f Fault) Verb() string {
	if f == FaultDrain {
		return "drained"
	}
	return "killed"
}

// EventKind names one thing that happened to the replica a chaos run took away.
type EventKind string

const (
	// EventFault is the fault being injected: the kill sent, or the drain asked
	// for. Every other event is timed from it.
	EventFault EventKind = "fault"
	// EventOutOfRotation is the router having stopped offering the replica.
	// After a kill, its distance from the fault is how long the router took to
	// notice.
	EventOutOfRotation EventKind = "out_of_rotation"
	// EventDrained is a drained replica's last request finishing.
	EventDrained EventKind = "drained"
	// EventDrainTimedOut is a drain that did not finish within its time limit,
	// after which the replica was stopped with requests still on it.
	EventDrainTimedOut EventKind = "drain_timed_out"
	// EventStopped is the replica's process being gone.
	EventStopped EventKind = "stopped"
	// EventRecovering is the replica being started again.
	EventRecovering EventKind = "recovering"
	// EventStarted is the replica answering again after its restart.
	EventStarted EventKind = "started"
	// EventInRotation is the router offering the replica again.
	EventInRotation EventKind = "in_rotation"
)

// eventLabels are the kinds as a reader says them.
var eventLabels = map[EventKind]string{
	EventFault:         "fault injected",
	EventOutOfRotation: "out of rotation",
	EventDrained:       "drained",
	EventDrainTimedOut: "drain ran out of time",
	EventStopped:       "stopped",
	EventRecovering:    "restart begun",
	EventStarted:       "restarted",
	EventInRotation:    "back in rotation",
}

// Event is one thing that happened to the replica, timed from the fault.
type Event struct {
	AtNs   int64     `json:"at_ns"`
	Kind   EventKind `json:"kind"`
	Detail string    `json:"detail,omitempty"`
}

// ChaosRun is one chaos run: a replica taken away from a fleet under steady
// open-loop load, brought back, and everything that happened recorded.
//
// It is an experiment rather than a cell, and is kept out of the cell tables on
// purpose. A cell is a point of the policy comparison, measured on a whole fleet
// in a steady state; this measures the transition between two fleets, and a
// goodput averaged across one would be a figure for neither of them.
type ChaosRun struct {
	ID     string `json:"id"`
	Policy string `json:"policy"`
	// HitRateLowWater and LoadImbalanceFactor are the spill point the router ran,
	// for the reason a cell records it: prefix affinity at two spill points is
	// two policies as far as any comparison is concerned.
	HitRateLowWater     float64 `json:"hit_rate_low_water"`
	LoadImbalanceFactor float64 `json:"load_imbalance_factor"`
	// MeanInflightDenominator is what that factor was held against. A chaos run
	// is driven open-loop at a low rate, which is exactly where the two
	// denominators are different rules (#31), so a run recording the factor
	// without it records half its own spill point.
	MeanInflightDenominator bool `json:"mean_inflight_denominator"`

	Replica     string  `json:"replica"`
	Fault       Fault   `json:"fault"`
	ArrivalRate float64 `json:"arrival_rate"`
	ThinkTimeNs int64   `json:"think_time_ns"`
	Workload    string  `json:"workload"`
	SLO         SLO     `json:"slo"`

	StartedAtNs int64 `json:"started_at_ns"`
	DurationNs  int64 `json:"duration_ns"`
	WarmupNs    int64 `json:"warmup_ns"`
	// FaultAtNs and RecoverAtNs are when, into the run, the replica was taken
	// away and when it was started again.
	FaultAtNs   int64 `json:"fault_at_ns"`
	RecoverAtNs int64 `json:"recover_at_ns"`
	// BucketNs is the width of the curve's buckets, and Tolerance how close to
	// its baseline goodput has to come back to count as recovered.
	BucketNs  int64   `json:"bucket_ns"`
	Tolerance float64 `json:"tolerance"`

	Events  []Event `json:"events"`
	Summary Summary `json:"summary"`
	// DroppedMidStream and DroppedUnplaced split the summary's dropped count by
	// whether the router had placed the request. One placed on a replica that then
	// died was streaming, and nothing could save it; one never placed found no
	// replica left in rotation at all. Different failures, and never summed into
	// one reason.
	DroppedMidStream int          `json:"dropped_mid_stream"`
	DroppedUnplaced  int          `json:"dropped_unplaced"`
	Curve            []CurvePoint `json:"curve"`
	Recovery         Recovery     `json:"recovery"`

	Contamination Contamination `json:"contamination"`
	// Throttle is what the cards said about their own clocks over the run. Its
	// own field beside the cleanliness evidence, for the reason a cell's is: a
	// run can be clean and still have been served by a card that could not keep
	// up with its siblings (#25).
	Throttle Throttle `json:"throttle"`
}

// Assess fills in everything a run's rows and events say about it: the
// summary, the split of its drops, its recovery curve and the recovery read off
// that curve. The run's configuration has to be set first.
//
// The summary's failure-rate and warm-up drift flags are switched off. Both
// exist to keep a cell describing a broken or cold fleet out of the comparison,
// and a chaos run describes a broken fleet on purpose: its drops are its result,
// reported in full below, and a run slower after its fault than before is the
// thing being measured rather than a warm-up that was too short. The schedule
// lag check stays, because a driver that fell behind offered less than this
// run says it did, which is as wrong here as anywhere.
func (s *ChaosRun) Assess(rows []Result, events []Event) {
	s.Events = slices.Clone(events)
	slices.SortStableFunc(s.Events, func(a, b Event) int { return cmp.Compare(a.AtNs, b.AtNs) })
	s.Summary = Summarize(rows, SummaryOptions{SLO: s.SLO, FailureThreshold: -1, WarmupDriftThreshold: -1})

	s.DroppedMidStream, s.DroppedUnplaced = 0, 0
	for _, r := range rows {
		if r.Warmup || r.Outcome != record.OutcomeDropped {
			continue
		}
		// The router names the replica only once it has placed a request and the
		// replica has begun answering, so a named replica on a dropped row is a
		// request that was lost mid-stream.
		if r.Replica != "" {
			s.DroppedMidStream++
		} else {
			s.DroppedUnplaced++
		}
	}

	bucket := time.Duration(s.BucketNs)
	s.Curve = RecoveryCurve(rows, time.Unix(0, s.StartedAtNs+s.FaultAtNs), time.Unix(0, s.StartedAtNs+s.DurationNs), bucket, s.SLO)
	s.Recovery = MeasureRecovery(s.Curve, bucket, s.Tolerance)
}

// After reports how long after the fault the first event of a kind happened, and
// whether one did.
func (s ChaosRun) After(kind EventKind) (time.Duration, bool) {
	for _, e := range s.Events {
		if e.Kind == kind {
			return time.Duration(e.AtNs), true
		}
	}
	return 0, false
}

// DropAccounting is the run's claim about drops, in one sentence that
// always carries the count of rerouted requests and the reason the figure is
// what it is.
//
// idea.md §7: "zero dropped requests" is not true as stated for streaming, and a
// bare count invites exactly that sentence. After a kill the count is low when
// few requests happened to be streaming at the instant the replica died, not
// because the router saved them, and the reroutes are the requests it did save.
// After a drain a zero is the drain's doing. Either way the number is only a
// claim beside its reason, so it is never printed without one.
func (s ChaosRun) DropAccounting() string {
	d := s.Summary
	var b strings.Builder
	fmt.Fprintf(&b, "%d of %d measured requests were dropped", d.Dropped, d.Requests)
	if d.Dropped > 0 {
		fmt.Fprintf(&b, " — %d %s streaming when their replica was lost, and %d %s never placed —",
			s.DroppedMidStream, wereOrWas(s.DroppedMidStream), s.DroppedUnplaced, wereOrWas(s.DroppedUnplaced))
	}
	fmt.Fprintf(&b, " and %d %s rerouted before their first token.", d.Rerouted, wereOrWas(d.Rerouted))
	switch s.Fault {
	case FaultDrain:
		if at, timedOut := s.After(EventDrainTimedOut); timedOut {
			fmt.Fprintf(&b, " %s was drained, but the drain did not finish, and %s after the fault it was stopped with requests still on it: those are among the drops above, so this run does not show a drain costing nothing.", s.Replica, at.Round(time.Millisecond))
			break
		}
		fmt.Fprintf(&b, " %s was drained before it was stopped, so every request it was serving finished on it and every request after the drain went elsewhere: the drop count measures the drain, and a kill would not match it.", s.Replica)
	default:
		fmt.Fprintf(&b, " A reroute is transparent only because nothing had reached its client when %s died. A request already streaming at that instant cannot be rerouted and is counted as dropped, so the drop count reflects how many requests happened to be mid-stream when the replica was killed, and the reroute count is what the router saved.", s.Replica)
	}
	return b.String()
}

// setting says what a run faced: the load, the SLO, and when the replica was
// taken away and brought back. One sentence shared by a run's report and a
// comparison's, because two runs are compared only if it is the same for both.
func (s ChaosRun) setting() string {
	return fmt.Sprintf("Open-loop at %g req/s with a %v think time, offering %s. SLO: %s. "+
		"The fault came %v in, after a %v warm-up, and the replica was started again %v in. "+
		"Goodput is bucketed every %v by when requests were offered, and read from the fault.",
		s.ArrivalRate, time.Duration(s.ThinkTimeNs), cmp.Or(s.Workload, "an unnamed workload"), describeSLO(s.SLO),
		time.Duration(s.FaultAtNs), time.Duration(s.WarmupNs), time.Duration(s.RecoverAtNs), time.Duration(s.BucketNs))
}

func wereOrWas(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

// Report renders the run for a reader: what was done to which replica,
// what happened to it, what became of the requests, and the curve.
func (s ChaosRun) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Chaos: %s %s under %s\n\n", s.Replica, s.Fault.Verb(), s.Policy)
	b.WriteString(s.setting() + "\n")
	if s.HitRateLowWater > 0 || s.LoadImbalanceFactor > 0 {
		fmt.Fprintf(&b, "Spill point: honoured low-water %g, load imbalance factor %g against the fleet's inflight %s.\n",
			s.HitRateLowWater, s.LoadImbalanceFactor, s.Spill().LoadDenominatorName())
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "## What happened to %s\n\n", s.Replica)
	if len(s.Events) == 0 {
		b.WriteString("Nothing was recorded.\n\n")
	} else {
		b.WriteString("| from the fault | event | detail |\n|---:|---|---|\n")
		for _, e := range s.Events {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", signed(e.AtNs), cmp.Or(eventLabels[e.Kind], string(e.Kind)), e.Detail)
		}
		b.WriteString("\n")
	}

	d := s.Summary
	b.WriteString("## Requests\n\n")
	b.WriteString(s.DropAccounting() + "\n\n")
	fmt.Fprintf(&b, "Of the rest, %d succeeded, %d of them outside the SLO; %d failed with an error their replica answered with; %d were cancelled by their client.\n",
		d.Successes, d.SLOViolations, d.Failed, d.Cancelled)
	for _, reason := range d.FlagReasons {
		fmt.Fprintf(&b, "\n⚠️ %s\n", reason)
	}
	for _, reason := range s.Contamination.Reasons() {
		fmt.Fprintf(&b, "\n⚠️ %s\n", reason)
	}
	for _, reason := range s.Throttle.Reasons() {
		fmt.Fprintf(&b, "\n⚠️ %s\n", reason)
	}

	b.WriteString("\n## Recovery\n\n")
	b.WriteString(s.describeRecovery())

	b.WriteString("\n## Curve\n\n")
	b.WriteString("| from the fault | offered | goodput/s | rerouted | dropped | failed | SLO violations |\n|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, p := range s.Curve {
		fmt.Fprintf(&b, "| %s | %d | %.2f | %d | %d | %d | %d |\n", signed(p.AtNs), p.Offered, p.GoodputRPS, p.Rerouted, p.Dropped, p.Failed, p.SLOViolations)
	}
	return b.String()
}

// describeRecovery says what the recovery numbers are, or why there are none.
func (s ChaosRun) describeRecovery() string {
	r := s.Recovery
	if !s.SLO.Applied() {
		return "No SLO was applied, so there is no goodput to have recovered.\n"
	}
	if r.BaselineRPS == 0 {
		return "Nothing met the SLO before the fault, so there was no healthy goodput to recover to.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- Baseline goodput before the fault: %.2f/s\n", r.BaselineRPS)
	fmt.Fprintf(&b, "- Lowest from the fault on: %.2f/s, in the bucket at %s\n", r.TroughRPS, signed(r.TroughAtNs))
	fmt.Fprintf(&b, "- Deficit: %s that would have met the SLO at the baseline did not\n", requestCount(r.DeficitRequests))
	if r.Steady {
		fmt.Fprintf(&b, "- Back within %.0f%% of the baseline from %s, and there to the end of the run\n", r.Tolerance*100, signed(r.SteadyAtNs))
	} else {
		fmt.Fprintf(&b, "- Not back within %.0f%% of the baseline for good before the run ended\n", r.Tolerance*100)
	}
	return b.String()
}

// describeSLO states the thresholds a run was judged against.
func describeSLO(slo SLO) string {
	if !slo.Applied() {
		return "none applied"
	}
	return fmt.Sprintf("TTFT < %v, inter-token p50 < %v", slo.TTFT, slo.ITL)
}

// signed renders an offset from the fault in seconds, with its sign.
func signed(ns int64) string {
	return fmt.Sprintf("%+.2fs", time.Duration(ns).Seconds())
}

// Spill is the grid point this run's router was at, rebuilt from the columns
// the record carries. It is the ChaosRun's CellSpill, and it exists for the same
// reason: a reader that assembled the point field by field would silently drop
// whichever column was added last.
func (s ChaosRun) Spill() policy.Spill {
	return policy.Spill{
		HitRateLowWater:         s.HitRateLowWater,
		LoadImbalanceFactor:     s.LoadImbalanceFactor,
		MeanInflightDenominator: s.MeanInflightDenominator,
	}
}
