// Package bench drives the fleet and accounts for what came back.
//
// Two things here are load-bearing for every number the project publishes.
//
// The first is the outcome taxonomy. A dropped request, a failed request and an
// SLO violation are three different events and they never share a column. Fast
// failures are the trap: under overload a replica rejects in milliseconds, so
// folding those into a latency distribution makes a fleet look quicker the
// worse it gets.
//
// The second is that the per-request rows are the system of record, not the
// summaries computed from them. Every published figure has to be recomputable
// from the rows, so the rows carry the raw timings and the summary carries only
// arithmetic over them.
package bench

import (
	"time"

	"github.com/yuchia329/kvroute/internal/record"
)

// Result is one request as the driver observed it from the client side.
//
// It is deliberately all scalars: the rows are written as JSONL during a run
// and compacted to Parquet after, and a flat row maps to a column without a
// nested schema in between.
//
// Client-observed timings are kept even though the router records its own,
// because the difference between the two is what separates transport cost from
// inference cost.
type Result struct {
	// Labels are embedded rather than copied field by field, so a row's cell
	// identity has exactly one definition and cannot disagree with the cell it
	// was written under.
	Labels

	RequestID string `json:"request_id" parquet:"request_id"`
	// Session is the multi-turn exchange this request belongs to. Under the
	// fixed workload one virtual user is one session; the multi-turn generator
	// replaces that with sessions that outlive a single user.
	Session string `json:"session" parquet:"session"`
	Turn    int    `json:"turn" parquet:"turn"`
	// VirtualUser is the slot of the workload's user space this request drew
	// from. Under the closed-loop driver that is the virtual user that sent it;
	// the open-loop driver has no virtual users, and each of its arrivals draws
	// its own slot.
	VirtualUser int `json:"virtual_user" parquet:"virtual_user"`
	// Warmup marks a request the summary excludes. The row is kept rather than
	// dropped: discarding it would make the record unable to show what was
	// discarded or why.
	Warmup bool `json:"warmup" parquet:"warmup"`

	StartedAtNs int64 `json:"started_at_ns" parquet:"started_at_ns"`
	// ScheduledAtNs is when the arrival schedule said this request was due.
	// Zero under the closed-loop driver, which has no schedule: there the next
	// request is due when the last one finished.
	//
	// It sits beside StartedAtNs rather than replacing it so the driver's own
	// lateness is visible in the record. An open-loop driver that fell behind
	// its schedule would be self-throttling exactly like a closed-loop one, and
	// a row that recorded only when it was sent could not show that it had.
	ScheduledAtNs int64 `json:"scheduled_at_ns" parquet:"scheduled_at_ns"`

	// Replica and Decision are read back off the router's response headers, so
	// the row says where the request actually went and why without the harness
	// having to reimplement the policy.
	Replica  string `json:"replica" parquet:"replica"`
	Decision string `json:"decision" parquet:"decision"`

	Status int `json:"status" parquet:"status"`

	// TTFTNs is client-observed time to the first response byte.
	TTFTNs int64 `json:"ttft_ns" parquet:"ttft_ns"`
	// TotalNs is request start to last byte received.
	TotalNs int64 `json:"total_ns" parquet:"total_ns"`

	OutputTokens int `json:"output_tokens" parquet:"output_tokens"`
	// ITL fields summarise the gaps between successive token chunks of this one
	// response. The SLO is evaluated against ITLP50Ns so that a single stall
	// does not fail an otherwise healthy response.
	ITLMeanNs int64 `json:"itl_mean_ns" parquet:"itl_mean_ns"`
	ITLP50Ns  int64 `json:"itl_p50_ns" parquet:"itl_p50_ns"`
	ITLMaxNs  int64 `json:"itl_max_ns" parquet:"itl_max_ns"`

	ResponseBytes int64 `json:"response_bytes" parquet:"response_bytes"`

	Outcome record.Outcome `json:"outcome" parquet:"outcome"`
	Error   string         `json:"error,omitempty" parquet:"error"`
}

// ScheduleLag is how late this request was sent against the schedule that asked
// for it. Zero when nothing scheduled it, which is every closed-loop row.
func (r Result) ScheduleLag() time.Duration {
	if r.ScheduledAtNs == 0 {
		return 0
	}
	return time.Duration(r.StartedAtNs - r.ScheduledAtNs)
}

// EndedAt is when the last byte of this request arrived.
func (r Result) EndedAt() time.Time {
	return time.Unix(0, r.StartedAtNs+r.TotalNs)
}

// SLO is the per-request pass/fail threshold, derived from the measured
// concurrency-1 floor rather than chosen a priori. A zero threshold on either
// dimension means that dimension is not checked; a zero SLO means none was
// applied at all, which is the honest state of the cell the floor itself comes
// from.
type SLO struct {
	TTFT time.Duration `json:"ttft"`
	ITL  time.Duration `json:"itl"`
}

// Applied reports whether this SLO constrains anything.
func (s SLO) Applied() bool { return s.TTFT > 0 || s.ITL > 0 }

// Met reports whether a successful response met the SLO. It says nothing about
// requests that did not succeed: those are counted in their own columns and are
// never SLO violations.
func (s SLO) Met(r Result) bool {
	if s.TTFT > 0 && r.TTFTNs > s.TTFT.Nanoseconds() {
		return false
	}
	if s.ITL > 0 && r.ITLP50Ns > s.ITL.Nanoseconds() {
		return false
	}
	return true
}
