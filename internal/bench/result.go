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
	// Reroutes is how many times the router moved this request to another
	// replica before one answered it, read back off its response header for the
	// reason Replica is. A reroute is invisible in the body by design, so the
	// router saying so is the only way the harness can count one.
	Reroutes int `json:"reroutes" parquet:"reroutes"`
	// PrefixMatchBytes is what the router believed the chosen replica already
	// held of this prompt, read back off its response header for the same reason
	// Replica and Decision are: the belief is the router's, and a harness that
	// recomputed it would be reporting a second index rather than the one that
	// routed the request.
	PrefixMatchBytes int `json:"prefix_match_bytes" parquet:"prefix_match_bytes"`
	// PrefixMatchTokens is the same prediction made in the engine's own tokens,
	// by exact residency, read off its own header. Zero under every other
	// policy. It is not folded into PrefixMatchBytes because it needs no
	// bytes-per-token conversion to be held against the engine's cached tokens
	// for this request, and converting it into bytes would put an error into it
	// that it does not have.
	PrefixMatchTokens int `json:"prefix_match_tokens" parquet:"prefix_match_tokens"`
	// PromptBytes is how many bytes of prompt this request sent. It is the
	// numerator of the measured prompt bytes-per-token ratio — the denominator
	// being what the engines say they processed — and that ratio is what turns
	// every byte-denominated prefix figure into the engine's own units.
	PromptBytes int64 `json:"prompt_bytes" parquet:"prompt_bytes"`

	Status int `json:"status" parquet:"status"`

	// TTFTNs is client-observed time to the first response byte.
	TTFTNs int64 `json:"ttft_ns" parquet:"ttft_ns"`
	// TotalNs is request start to last byte received.
	TotalNs int64 `json:"total_ns" parquet:"total_ns"`

	// The engine's own account of this request's prompt, off the usage block it
	// returns when asked for one. This is the ground truth PrefixMatchBytes above
	// is a prediction of, and the pair is what belief divergence is measured from
	// (#17) — one field per side on one row, which is the entire cost of being
	// able to plot the two against each other.
	//
	// Per request rather than off vllm:request_prefill_kv_computed_tokens, which
	// carries the same quantity as a histogram and so cannot be joined to the
	// request whose prediction it would check. Over a cell's window the two agree,
	// and the divergence report puts them side by side rather than assuming it.
	//
	// EnginePromptTokens is what the engine says it processed; EngineCachedTokens
	// is how much of that it answered out of its KV cache instead of prefilling,
	// so the difference is what the GPU actually computed.
	EnginePromptTokens int `json:"engine_prompt_tokens" parquet:"engine_prompt_tokens"`
	EngineCachedTokens int `json:"engine_cached_tokens" parquet:"engine_cached_tokens"`
	// EngineUsageRead and EngineCacheRead say whether the engine reported at all,
	// and whether it broke the prompt down into cached and computed. Both are on
	// the row because a replica that cached nothing, one that does not report
	// caching, and one that returned no usage are three different things, and a
	// bare zero would read as the first.
	EngineUsageRead bool `json:"engine_usage_read" parquet:"engine_usage_read"`
	EngineCacheRead bool `json:"engine_cache_read" parquet:"engine_cache_read"`

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

// ComputedPrefillTokens is the prompt tokens the engine actually had to compute
// for this request, and whether it said.
//
// It is the per-request form of recomputed prefill: what the GPU spent, as
// opposed to what the router believed it would not have to. CONTEXT.md reserves
// "redundant prefill" for the comparison against another policy on the same
// bytes, which no single request can make.
func (r Result) ComputedPrefillTokens() (int, bool) {
	if !r.EngineUsageRead || !r.EngineCacheRead {
		return 0, false
	}
	return r.EnginePromptTokens - r.EngineCachedTokens, true
}

// PredictedCachedTokens is the router's prefix match expressed in the engine's
// units: the leading bytes it believed the chosen replica held, converted at
// this request's own measured bytes per token.
//
// Per request rather than through the run-wide ratio the calibration carries.
// Both sides of the conversion are already on the row — the bytes this request
// sent, and the tokens the engine says they came to — so the ratio is measured on
// the very request it is applied to and cannot be an average that fits no
// individual prompt. CONTEXT.md's prompt bytes per token is the same quantity
// measured over a whole run, and the two are expected to agree; the divergence
// report is where they are compared rather than assumed.
//
// Exact residency's prediction needs none of that: it is already in the engine's
// tokens, and it is taken as it stands, because converting it through bytes would
// put into it an error it does not have.
//
// belief.PredictedTokens does the same conversion for the router's live honoured
// rate and deliberately does *not* agree with this one in one place: a claim of
// zero is a real prediction here and no prediction there. Divergence counts a
// request the index claimed nothing for and the engine held nothing for as
// exactly right, which it was; the honoured rate must not, because a zero claim
// is perfectly honoured by arithmetic and would drag every replica's rate towards
// one. The two are not a duplicate to be unified.
func (r Result) PredictedCachedTokens() (float64, bool) {
	if !r.EngineUsageRead {
		return 0, false
	}
	if r.PrefixMatchTokens > 0 {
		return float64(r.PrefixMatchTokens), true
	}
	if r.EnginePromptTokens <= 0 || r.PromptBytes <= 0 {
		return 0, false
	}
	return float64(r.PrefixMatchBytes) * float64(r.EnginePromptTokens) / float64(r.PromptBytes), true
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
