package bench

import (
	"fmt"
	"slices"
	"time"

	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/stats"
)

// DefaultFailureThreshold is the failure rate above which a cell is flagged.
// §6 puts it at ~1%: past that the cell is not describing a healthy fleet and
// averaging it in with cells that are would hide the fact.
const DefaultFailureThreshold = 0.01

// DefaultWarmupDriftThreshold is how much slower a cell's first measured half
// may be than its second before the cell is flagged as still warming up.
//
// Generous on purpose. The point of the check is to catch a warm-up that was
// plainly too short, not to police ordinary run-to-run wobble: a threshold that
// fires on noise would flag every cell, and a flag that fires on every cell
// stops being read.
const DefaultWarmupDriftThreshold = 0.25

// DefaultScheduleLagThreshold is how far behind its own arrival schedule an
// open-loop cell may fall, at the 99th percentile, before it is flagged.
//
// The fleet's own latencies are hundreds of milliseconds, so a driver that is
// more than a tenth of a second late on one request in a hundred is
// contributing meaningfully to the numbers it reports — and, worse, is
// throttling itself in exactly the way the open-loop driver exists to avoid.
// Below that the lateness is host scheduling noise on top of load the fleet did
// receive.
const DefaultScheduleLagThreshold = 100 * time.Millisecond

// SummaryOptions are the thresholds a cell is judged against. They travel
// together everywhere, so they are one type rather than three arguments.
type SummaryOptions struct {
	SLO SLO
	// FailureThreshold is the dropped-plus-failed rate above which a cell is
	// flagged. Zero uses DefaultFailureThreshold.
	FailureThreshold float64
	// WarmupDriftThreshold is the first-half-to-second-half slowdown above
	// which a cell is flagged as under-warmed. Zero uses
	// DefaultWarmupDriftThreshold; a negative value disables the check.
	WarmupDriftThreshold float64
	// TurnsPerSession is how many turns a session sends before the workload
	// draws a fresh one, which is the period the turn index cycles with and the
	// stratifier the drift check compares within. Zero from a workload that
	// never returns to a turn index — the fixed workload's index is a counter,
	// not a position — and the drift check then pools.
	//
	// Stated rather than inferred from the rows. The two look alike inside one
	// cell: a closed-loop fixed cell at high concurrency has its virtual users
	// spread across a handful of turn numbers whose spans overlap exactly as a
	// multi-turn cell's indices do, and a rule that guessed from the rows read
	// that as a four-turn workload and reported four missing visits. The
	// workload knows its own period, so it says.
	TurnsPerSession int
	// ScheduleLagThreshold is how late an open-loop cell's requests may be sent
	// against the schedule that asked for them, at the 99th percentile, before
	// the cell is flagged. Zero uses DefaultScheduleLagThreshold; a negative
	// value disables the check. It says nothing about a closed-loop cell, which
	// has no schedule to be late against.
	ScheduleLagThreshold time.Duration
}

func (o SummaryOptions) failureThreshold() float64 {
	if o.FailureThreshold == 0 {
		return DefaultFailureThreshold
	}
	return o.FailureThreshold
}

func (o SummaryOptions) driftThreshold() float64 {
	if o.WarmupDriftThreshold == 0 {
		return DefaultWarmupDriftThreshold
	}
	return o.WarmupDriftThreshold
}

func (o SummaryOptions) scheduleLagThreshold() time.Duration {
	if o.ScheduleLagThreshold == 0 {
		return DefaultScheduleLagThreshold
	}
	return o.ScheduleLagThreshold
}

// Summary is the arithmetic over one cell's rows.
//
// Dropped, Failed and SLOViolations are three columns and stay three columns.
// Every percentile here is over successful responses only.
type Summary struct {
	Requests      int `json:"requests" parquet:"requests"`
	Warmup        int `json:"warmup" parquet:"warmup"`
	Successes     int `json:"successes" parquet:"successes"`
	Failed        int `json:"failed" parquet:"failed"`
	Dropped       int `json:"dropped" parquet:"dropped"`
	Cancelled     int `json:"cancelled" parquet:"cancelled"`
	SLOViolations int `json:"slo_violations" parquet:"slo_violations"`
	// Rerouted is how many measured requests the router moved to another replica
	// because the one it first chose failed before emitting anything. It is not
	// an outcome and is not one of the columns above: a rerouted request ends
	// like any other, usually as a success, and this counts the losses the
	// reroute hid. A drop count means something only beside it.
	Rerouted int `json:"rerouted" parquet:"rerouted"`

	// WindowNs is the span the rates below are computed over, derived from the
	// rows rather than from the driver's clock so that the denominator cannot
	// disagree with the rows it divides.
	//
	// It is the window load was *offered* in, which is not the same span under
	// the two drivers. A closed-loop cell offers load for as long as it is
	// receiving responses, so the window is its first measured request's start
	// to the last one's finish. An open-loop cell offers load on a schedule and
	// then waits for what is still in flight, and past saturation that drain
	// tail runs well past the last arrival — counting it would divide the
	// requests offered in a minute by a minute and a half and call the result a
	// rate. So a scheduled cell's window is its schedule: first arrival due to
	// last arrival due, plus the one inter-arrival gap the last arrival owns.
	//
	// Requests that finished after the window still count in the numerator.
	// They were offered inside it, and dropping them would let a fleet improve
	// its goodput by being too slow to answer before the cell ended.
	WindowNs int64 `json:"window_ns" parquet:"window_ns"`
	// ThroughputRPS counts every response that completed, however slow.
	// GoodputRPS counts only those that completed inside the SLO, and is the
	// primary metric. A dropped or failed request produced no response and is
	// in neither.
	ThroughputRPS float64 `json:"throughput_rps" parquet:"throughput_rps"`
	GoodputRPS    float64 `json:"goodput_rps" parquet:"goodput_rps"`

	TTFTP50Ns int64 `json:"ttft_p50_ns" parquet:"ttft_p50_ns"`
	// TTFTP90Ns is here for one comparison: llm-d published its precise-versus-
	// approximate result as P90 TTFT, and #24 replicates it, so the like-for-like
	// figure has to come off the same rows as every other percentile.
	TTFTP90Ns  int64 `json:"ttft_p90_ns" parquet:"ttft_p90_ns"`
	TTFTP95Ns  int64 `json:"ttft_p95_ns" parquet:"ttft_p95_ns"`
	TTFTP99Ns  int64 `json:"ttft_p99_ns" parquet:"ttft_p99_ns"`
	TotalP50Ns int64 `json:"total_p50_ns" parquet:"total_p50_ns"`
	TotalP99Ns int64 `json:"total_p99_ns" parquet:"total_p99_ns"`
	// ITL percentiles are taken across the per-request median inter-token
	// latencies, not across every pooled gap: the rows carry each request's ITL
	// summary rather than its individual gaps.
	ITLP50Ns int64 `json:"itl_p50_ns" parquet:"itl_p50_ns"`
	ITLP95Ns int64 `json:"itl_p95_ns" parquet:"itl_p95_ns"`

	// SLOApplied distinguishes "nothing violated" from "nothing was checked".
	// The concurrency-1 cell is what the SLO is derived from, so it necessarily
	// runs before one exists, and a bare zero in the violations column would
	// read as the former.
	SLOApplied bool  `json:"slo_applied" parquet:"slo_applied"`
	SLOTTFTNs  int64 `json:"slo_ttft_ns" parquet:"slo_ttft_ns"`
	SLOITLNs   int64 `json:"slo_itl_ns" parquet:"slo_itl_ns"`

	// WarmupDrift is how much slower the early part of the measured window was
	// than the late part, by TTFT p50: 0.30 means 30% slower early, and -0.30
	// means 30% slower late. The sign convention is unchanged from before #33 so
	// that a re-scored cell can be held against its recorded number.
	//
	// It exists so the warm-up length can be checked rather than trusted. A cell
	// whose measured window is still speeding up is still warming up, and no
	// constant chosen in advance can prove otherwise for every concurrency
	// level. It is tested two-sided: a cell that got twice as slow across its
	// window is as unpoolable as one that got twice as fast, and is a different
	// fault with a different fix.
	//
	// Split on the arrival window rather than on first-start-to-last-response,
	// and compared within each turn index rather than across the pooled halves.
	// See warmupdrift.go for why each of those is load-bearing. NaN-free: zero
	// when there are too few successes to compare.
	WarmupDrift float64 `json:"warmup_drift" parquet:"warmup_drift"`
	// WarmupDriftBasis says what WarmupDrift was measured over — "turn" when each
	// turn index was compared with itself, "pooled" when every success was
	// compared as one population, and empty when there was too little to
	// compare. A drift figure means different things under the two, so the
	// record carries which was used rather than leaving it to be inferred from
	// the workload name.
	WarmupDriftBasis string `json:"warmup_drift_basis,omitempty" parquet:"warmup_drift_basis"`
	// WarmupDriftTurnsCompared is how many turn indices the figure is an average
	// over, and WarmupDriftTurnsConfined how many of the workload's indices did
	// not appear on both sides of the split with enough requests to have a
	// median — absent from the window altogether, offered only early, or only
	// late.
	//
	// WarmupDriftTurnsConfined above zero is the cell saying its measured window
	// is not a whole number of visit periods: the two halves hold different turn
	// indices, so every percentile in this summary is over a mix no cell of
	// another length shares.
	WarmupDriftTurnsCompared int `json:"warmup_drift_turns_compared,omitempty" parquet:"warmup_drift_turns_compared"`
	WarmupDriftTurnsConfined int `json:"warmup_drift_turns_confined,omitempty" parquet:"warmup_drift_turns_confined"`

	// Scheduled is how many measured requests carried a due time, which is all
	// of them under the open-loop driver and none under the closed-loop one.
	// The lag figures below are over those requests, whatever their outcome:
	// how late the driver was is a property of the driver, not of what came
	// back.
	Scheduled        int   `json:"scheduled" parquet:"scheduled"`
	ScheduleLagP50Ns int64 `json:"schedule_lag_p50_ns" parquet:"schedule_lag_p50_ns"`
	ScheduleLagP99Ns int64 `json:"schedule_lag_p99_ns" parquet:"schedule_lag_p99_ns"`
	ScheduleLagMaxNs int64 `json:"schedule_lag_max_ns" parquet:"schedule_lag_max_ns"`
	// ScheduleLagThresholdNs is the p99 lag above which this cell was flagged
	// as not having held its schedule, so the flag can be read against what it
	// was judged by.
	ScheduleLagThresholdNs int64 `json:"schedule_lag_threshold_ns" parquet:"schedule_lag_threshold_ns"`
	// Backlog is the share of the requests an open-loop cell offered over its
	// measured window that were still unanswered when that window closed: 0.30 means three in ten
	// of what was offered had not come back by the time arrivals stopped.
	//
	// It is what says whether the fleet kept up. One that does answers all but
	// the last latency's worth of what it is offered, whatever the rate; one
	// past its knee serves less than it is offered, its queue grows for as long
	// as arrivals keep coming, and the excess is left behind at the close. A
	// cell like that never reaches a steady state, so its latency percentiles
	// are a transient and its goodput is the result (#33).
	//
	// Zero under the closed-loop driver, whose virtual users wait for their
	// answers and so offer only what the fleet serves — and zero on a record
	// written before it existed, which reads as a fleet that kept up and is
	// judged as strictly as it was when it ran.
	Backlog float64 `json:"backlog,omitempty" parquet:"backlog"`

	// PromptBytes is the prompt bytes this cell offered over its measured
	// window, counted across every request it sent rather than only the ones
	// that came back.
	//
	// Offered rather than served, because it is the numerator of the prompt
	// bytes-per-token ratio and the engines' prompt-token counters on the other
	// side of that ratio count what they were asked to process. Restricting it
	// to successes would put a fleet's failures into a conversion factor that
	// has nothing to do with them.
	PromptBytes int64 `json:"prompt_bytes" parquet:"prompt_bytes"`

	// Decisions is how this cell's requests were routed, counted by the reason
	// the policy gave. It is a reported result: see DecisionMix.
	Decisions DecisionMix `json:"decisions" parquet:"decisions"`

	// Placement is where those requests landed, counted per replica. It is the
	// other reported result beside the decision mix, and the pair says what a
	// policy did from both ends: the reasons it gave, and the fleet it left
	// behind.
	//
	// Counted from the rows rather than read off the router's per-decision
	// inflight column, which recorded zero on every session-affinity row ever
	// written before 9311e68 — the column idea.md §5's imbalance claim had been
	// resting on. See Placement.
	Placement Placement `json:"placement" parquet:"placement"`

	// FailureRate is dropped plus failed over every measured request.
	FailureRate      float64 `json:"failure_rate" parquet:"failure_rate"`
	FailureThreshold float64 `json:"failure_threshold" parquet:"failure_threshold"`
	// Flagged marks a cell that must not be averaged in with the others without
	// someone reading FlagReasons first.
	Flagged     bool     `json:"flagged" parquet:"flagged"`
	FlagReasons []string `json:"flag_reasons,omitempty" parquet:"flag_reasons"`
}

// Summarize computes a cell's summary from its rows.
//
// Warm-up rows are excluded from every count and every percentile but are still
// reported, so the record shows what was set aside as well as what was kept.
func Summarize(results []Result, opts SummaryOptions) Summary {
	slo := opts.SLO
	s := Summary{
		SLOApplied:       slo.Applied(),
		SLOTTFTNs:        slo.TTFT.Nanoseconds(),
		SLOITLNs:         slo.ITL.Nanoseconds(),
		FailureThreshold: opts.failureThreshold(),
	}

	var ttft, total, itl, lag []time.Duration
	var first, last time.Time
	var firstDue, lastDue time.Time
	var placements placementCounter
	rate := 0.0
	met := 0

	for _, r := range results {
		if r.Warmup {
			s.Warmup++
			continue
		}
		s.Requests++
		s.PromptBytes += r.PromptBytes
		s.Decisions.count(r.Decision)
		// Where it went, beside why it went there. Every measured row is
		// counted, whatever its outcome: a request a replica accepted and then
		// failed still occupied that replica.
		placements.count(r.Replica)
		if r.Reroutes > 0 {
			s.Rerouted++
		}

		started := time.Unix(0, r.StartedAtNs)
		if first.IsZero() || started.Before(first) {
			first = started
		}
		if ended := r.EndedAt(); ended.After(last) {
			last = ended
		}

		// Before the outcome switch: an arrival that was fired late was late
		// whether or not anything came back, and a driver that fell behind
		// while the fleet was dropping requests is exactly the case the figure
		// exists to catch.
		if r.ScheduledAtNs != 0 {
			s.Scheduled++
			lag = append(lag, r.ScheduleLag())
			due := time.Unix(0, r.ScheduledAtNs)
			if firstDue.IsZero() || due.Before(firstDue) {
				firstDue = due
			}
			if due.After(lastDue) {
				lastDue = due
			}
			// Off the rows, not off a caller's parameter: the rate the window is
			// derived from has to be the rate the requests in it were offered at.
			rate = r.ArrivalRate
		}

		switch r.Outcome {
		case record.OutcomeSuccess:
			s.Successes++
			// Percentiles are over successes only. A request that never
			// produced a response has no latency to contribute, and a fast
			// failure has one that would flatter the fleet.
			ttft = append(ttft, time.Duration(r.TTFTNs))
			total = append(total, time.Duration(r.TotalNs))
			itl = append(itl, time.Duration(r.ITLP50Ns))
			if !s.SLOApplied {
				break
			}
			if slo.Met(r) {
				met++
			} else {
				s.SLOViolations++
			}
		case record.OutcomeFailed:
			s.Failed++
		case record.OutcomeDropped:
			s.Dropped++
		case record.OutcomeCancelled:
			s.Cancelled++
		}
	}

	// Unconditionally, including for a cell that placed nothing: the flag is
	// what separates a fleet that served nothing from a cell nobody counted, and
	// setting it only when there was something to count would collapse the two.
	s.Placement = placements.placement()

	if s.Requests > 0 {
		s.FailureRate = float64(s.Failed+s.Dropped) / float64(s.Requests)
	}
	window := cellWindow{first: first, last: last, firstDue: firstDue, lastDue: lastDue, rate: rate}
	if offered := window.duration(); offered > 0 {
		s.WindowNs = offered.Nanoseconds()
		s.ThroughputRPS = float64(s.Successes) / offered.Seconds()
		s.GoodputRPS = float64(met) / offered.Seconds()
	}
	s.Backlog = window.backlog(results)

	slices.Sort(ttft)
	slices.Sort(total)
	slices.Sort(itl)
	slices.Sort(lag)
	s.ScheduleLagP50Ns = stats.Quantile(lag, 0.50).Nanoseconds()
	s.ScheduleLagP99Ns = stats.Quantile(lag, 0.99).Nanoseconds()
	if len(lag) > 0 {
		s.ScheduleLagMaxNs = lag[len(lag)-1].Nanoseconds()
	}
	s.TTFTP50Ns = stats.Quantile(ttft, 0.50).Nanoseconds()
	s.TTFTP90Ns = stats.Quantile(ttft, 0.90).Nanoseconds()
	s.TTFTP95Ns = stats.Quantile(ttft, 0.95).Nanoseconds()
	s.TTFTP99Ns = stats.Quantile(ttft, 0.99).Nanoseconds()
	s.TotalP50Ns = stats.Quantile(total, 0.50).Nanoseconds()
	s.TotalP99Ns = stats.Quantile(total, 0.99).Nanoseconds()
	s.ITLP50Ns = stats.Quantile(itl, 0.50).Nanoseconds()
	s.ITLP95Ns = stats.Quantile(itl, 0.95).Nanoseconds()

	drift := measureDrift(results, window, opts.TurnsPerSession)
	s.WarmupDrift = drift.drift
	s.WarmupDriftBasis = string(drift.basis)
	s.WarmupDriftTurnsCompared = drift.compared
	s.WarmupDriftTurnsConfined = drift.confined
	s.ScheduleLagThresholdNs = opts.scheduleLagThreshold().Nanoseconds()
	s.flag(opts.driftThreshold(), opts.scheduleLagThreshold())
	return s
}

// cellWindow is the stretch of time a cell's measured rows cover, held as the
// four instants the rows supply rather than as one duration.
//
// One type rather than a span passed around, because two different questions are
// asked of it and they want different answers. How long load was OFFERED is the
// denominator of every rate. WHEN load was offered is what the warm-up drift
// check splits on. Deriving both from one struct is what keeps the split and the
// denominator describing the same window.
type cellWindow struct {
	// first and last are the first measured request's start and the last one's
	// finish.
	first, last time.Time
	// firstDue and lastDue are the first and last arrival the schedule asked
	// for, and the zero time under the closed-loop driver, which has no
	// schedule.
	firstDue, lastDue time.Time
	// rate is the arrival rate the rows were offered at, read off the rows.
	rate float64
}

// scheduled reports whether this cell's rows carry an arrival schedule.
func (w cellWindow) scheduled() bool { return !w.firstDue.IsZero() && w.rate > 0 }

// duration is the span the cell's rates are computed over: the schedule a
// scheduled cell offered load on, and otherwise the span its rows cover.
//
// The distinction is the open-loop driver's whole point arriving in the
// arithmetic. Its cell fires for its duration and then waits out whatever is
// still in flight, so at and past saturation the last response lands well after
// the last arrival. Dividing by that would understate the rate exactly where the
// driver exists to stop the rate being understated.
func (w cellWindow) duration() time.Duration {
	opens, closes := w.arrivals()
	if !closes.After(opens) {
		return 0
	}
	return closes.Sub(opens)
}

// arrivals is the window as two instants: when the cell began offering load and
// when it stopped.
//
// A scheduled cell's is its schedule — first arrival due to last arrival due,
// plus the one inter-arrival gap the last arrival owns, since n arrivals at rate
// r occupy n/r seconds and the span between the first and the last is one gap
// short of that. A closed-loop cell has no schedule to be read, so its rows are
// the only account of when it was offering load.
//
// The drain is outside it either way, which is the point. A window that ran to
// the last RESPONSE would have its midpoint half a drain past its arrival
// midpoint — 57 to 71 seconds on #29's think-75s cells — and everything the
// halves are supposed to hold would be off by that much.
func (w cellWindow) arrivals() (opens, closes time.Time) {
	if w.scheduled() {
		return w.firstDue, w.lastDue.Add(time.Duration(float64(time.Second) / w.rate))
	}
	return w.first, w.last
}

// backlog is the share of the scheduled measured requests whose response had not
// ended when the arrival window closed, and zero for a cell with no schedule. See
// Summary.Backlog.
//
// Every outcome counts as answered once it ended, a failure included: what the
// figure measures is the queue, and a request that has failed is no longer in it.
func (w cellWindow) backlog(results []Result) float64 {
	if !w.scheduled() {
		return 0
	}
	_, closes := w.arrivals()
	offered, unanswered := 0, 0
	for _, r := range results {
		if r.Warmup || r.ScheduledAtNs == 0 {
			continue
		}
		offered++
		if r.EndedAt().After(closes) {
			unanswered++
		}
	}
	if offered == 0 {
		return 0
	}
	return float64(unanswered) / float64(offered)
}

// flag records every reason this cell should not be silently averaged in with
// the others.
func (s *Summary) flag(driftThreshold float64, scheduleLagThreshold time.Duration) {
	if s.Requests == 0 {
		s.Flag("cell produced no measured requests")
	}
	if s.FailureThreshold > 0 && s.FailureRate > s.FailureThreshold {
		s.Flag(fmt.Sprintf("failure rate %.2f%% exceeds the %.2f%% threshold (%d dropped, %d failed of %d)",
			s.FailureRate*100, s.FailureThreshold*100, s.Dropped, s.Failed, s.Requests))
	}
	s.flagDrift(driftThreshold)
	if s.Scheduled > 0 && scheduleLagThreshold > 0 && s.ScheduleLagP99Ns > scheduleLagThreshold.Nanoseconds() {
		// The open-loop driver's whole claim is that offered load is an input.
		// A driver that fell behind its own schedule offered less than the cell
		// says it did, and the goodput computed from it would be a figure for a
		// rate the fleet was never actually given.
		s.Flag(fmt.Sprintf("the driver did not hold its arrival schedule: 1%% of requests were sent more than %v late (worst %v), over a %v threshold. The offered rate is not the rate this cell reports",
			time.Duration(s.ScheduleLagP99Ns).Round(time.Millisecond),
			time.Duration(s.ScheduleLagMaxNs).Round(time.Millisecond),
			scheduleLagThreshold))
	}
	if s.Cancelled > 0 {
		// The driver lets in-flight requests finish, so a cancellation means
		// the cell was interrupted rather than run to its end.
		s.Flag(fmt.Sprintf("%d requests were cancelled: the cell did not run to completion", s.Cancelled))
	}
}

// Flag records a reason this summary must not be silently averaged in with the
// others. It is exported because the reasons do not all come from the rows: the
// GPUs supply some, and the characterization pass supplies others.
func (s *Summary) Flag(reason string) {
	s.Flagged = true
	s.FlagReasons = append(s.FlagReasons, reason)
}
