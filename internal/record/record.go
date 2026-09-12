// Package record writes the per-request rows that are this project's system of
// record.
//
// Prometheus is a sampled TSDB and the wrong shape for per-request tail
// latency across hundreds of cells, so every reported figure is instead
// recomputable from these rows. They are written as JSONL during a run, one
// line flushed per request, so that a crashed sweep still leaves readable
// partial data.
package record

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Outcome is how a request ended. Dropped, failed and SLO violation are kept
// distinct: folding fast failures into a latency distribution makes an
// overloaded fleet look faster. SLO violation is derived during analysis, from
// the timings on a successful row.
type Outcome string

const (
	// OutcomeSuccess is a complete response from a replica.
	OutcomeSuccess Outcome = "success"
	// OutcomeFailed is a request a replica accepted and then errored on.
	OutcomeFailed Outcome = "failed"
	// OutcomeDropped is a request that received no complete response because
	// the router could not place it, or because the replica it was placed on was
	// lost after its stream had begun. The second is the chaos test's case: a
	// replica that dies before a request's first token has emitted nothing and
	// the request is rerouted, but one that dies mid-stream has, and nothing the
	// router can do gives the client a whole answer. Neither is failed, because
	// no replica answered either with an error.
	OutcomeDropped Outcome = "dropped"
	// OutcomeCancelled is a request whose client went away before the response
	// finished. It is neither dropped nor failed: no replica errored and the
	// router placed it fine. It has its own value so that a disconnect cannot
	// inflate the failure count, which is the one number an overloaded fleet
	// must not be able to hide behind.
	OutcomeCancelled Outcome = "cancelled"
)

// Request is one row: everything observed about a single request through the
// router.
// The parquet tags mirror the json names exactly. Without them the compacted
// columns would carry Go field names, and joining the router's rows to the
// harness's on request id would mean matching request_id against RequestID.
type Request struct {
	RequestID string    `json:"request_id" parquet:"request_id"`
	StartedAt time.Time `json:"started_at" parquet:"started_at"`

	// Session is the conversation this request belonged to, and SessionDerived
	// says whether the client supplied that identity or the router derived it
	// from the request's opening messages.
	//
	// Both are on the row because a session-affinity policy's whole claim is
	// that it kept a conversation on one replica, and a row naming only the
	// replica cannot be read to check it. The derived flag travels with it
	// because routing on a supplied key and routing on a derived one are two
	// different measurements (idea.md §4.2), and a run that recorded only the
	// key could not say which of them it was.
	Session        string `json:"session" parquet:"session"`
	SessionDerived bool   `json:"session_derived" parquet:"session_derived"`

	Policy         string `json:"policy" parquet:"policy"`
	Replica        string `json:"replica" parquet:"replica"`
	DecisionReason string `json:"decision_reason" parquet:"decision_reason"`
	// Reroutes is how many times the request was moved to another replica
	// because the one it had been sent to failed before emitting anything, and
	// ReroutedFrom is the first replica that failed it. Zero and empty for nearly
	// every request; Replica and DecisionReason above describe where it was
	// finally served.
	//
	// On the row because a reroute is invisible in the response by design: the
	// client receives exactly what the second replica would have sent it, so the
	// row is the only place the failure the reroute hid can be counted. A chaos
	// run reports rerouted requests beside dropped ones, and a count of drops
	// means something only next to the count of requests that would have been
	// dropped had nothing been rerouted.
	Reroutes     int    `json:"reroutes" parquet:"reroutes"`
	ReroutedFrom string `json:"rerouted_from" parquet:"rerouted_from"`
	// PrefixMatchBytes is how much of this prompt's leading bytes the router
	// believed the chosen replica already held. Zero under the policies that
	// consult no prefix index.
	//
	// In bytes, because the index has no tokenizer: this is a length of prompt,
	// and the measured prompt bytes-per-token ratio published with a run is what
	// converts it. It is the router's prediction, and the engine's own
	// vllm:request_prefill_kv_computed_tokens for the same request is the truth
	// it is predicting — one field per row is the entire cost of being able to
	// plot the two against each other.
	PrefixMatchBytes int `json:"prefix_match_bytes" parquet:"prefix_match_bytes"`
	// Inflight is the chosen replica's inflight when the decision was made, not
	// counting this request. It is the load the policy
	// weighed, recorded so that how balanced a policy left the fleet is a figure
	// the rows can show rather than a claim about the policy's code.
	Inflight int `json:"inflight" parquet:"inflight"`
	// FleetMinInflight and FleetMeanInflight are what the rest of the fleet was
	// carrying when this decision was made: the quietest replica's inflight, and
	// the inflight of the snapshot's replicas averaged over them. FleetLoadRead
	// says the row carries them.
	//
	// Inflight above is one replica's; these are the fleet's, and the spill
	// rule's load condition is a comparison between the two. Which of these two
	// figures it compares against is the difference between a ratio and an
	// absolute inflight threshold at low load, so a row carrying only the
	// chosen replica's count cannot say which rule a run was running (#31).
	//
	// Written under every policy, including the three with no spill rule, for the
	// reason the honoured rate is: the pass that cuts a grid runs with the
	// condition disabled, and a column that appeared only where something read it
	// could not be used to project what a threshold would have declined.
	//
	// Read is a third column rather than an inferred one because a fleet that was
	// genuinely idle and a run whose rows predate these columns both write zeros,
	// and they are opposite findings.
	FleetMinInflight  int     `json:"fleet_min_inflight" parquet:"fleet_min_inflight"`
	FleetMeanInflight float64 `json:"fleet_mean_inflight" parquet:"fleet_mean_inflight"`
	FleetLoadRead     bool    `json:"fleet_load_read" parquet:"fleet_load_read"`
	// PromptBytes is how long this request's rendered prompt was.
	//
	// It is the denominator every byte-denominated figure on this row has to be
	// read through. The prefix index counts in its own bytes and the engine
	// answers in tokens, and this column is what converts between them for this
	// one request rather than through a run-wide average that fits no individual
	// prompt — which is the conversion the honoured rate is computed across.
	PromptBytes int `json:"prompt_bytes" parquet:"prompt_bytes"`
	// BatchKVOccupancy is the chosen replica's scraped batch KV occupancy when
	// the decision was made, and BatchKVRead says whether any scrape had
	// answered for it.
	//
	// Two fields rather than one, for the reason the outcome taxonomy is four
	// columns rather than three: an unscraped replica and an idle one are
	// different states, and only the flag can tell them apart.
	//
	// Nothing routes on this since ADR-0011 — it counts the blocks held by the
	// running batch, which Inflight above already counts locally and exactly. It
	// is still written on every row, beside the inflight it was taken at the same
	// moment as, precisely so that the correlation ADR-0011 rests on can be
	// re-derived from any run rather than only from the one that measured it.
	BatchKVOccupancy float64 `json:"batch_kv_occupancy" parquet:"batch_kv_occupancy"`
	BatchKVRead      bool    `json:"batch_kv_read" parquet:"batch_kv_read"`
	// HonouredRate is the chosen replica's honoured rate when the decision was
	// made, HonouredRead says whether it had answered enough scoring requests to
	// have one, and HonouredClaims how many that reading rested on.
	//
	// This is the residency signal the spill rule's first branch reads, and the
	// three columns are what the grid is cut against: a mark can only be chosen
	// from the range the signal was observed over, and a rate without its
	// evidence count cannot be told apart from a rate off two unlucky requests.
	//
	// Written under every policy, including the three that consult no index and
	// so never feed it. A column that appeared only where it was read could not
	// be used to show what it would have done elsewhere.
	HonouredRate   float64 `json:"honoured_rate" parquet:"honoured_rate"`
	HonouredRead   bool    `json:"honoured_read" parquet:"honoured_read"`
	HonouredClaims int     `json:"honoured_claims" parquet:"honoured_claims"`
	// HitRate is the chosen replica's prefix cache hit rate over its window when
	// the decision was made, HitRateRead whether the window held traffic enough
	// to say, and HitRateQueries how many block queries it rested on.
	//
	// This is the residency signal the spill rule reads, and the three columns
	// are what its grid is cut against. HonouredRate above is the candidate it
	// replaced, kept beside it: #28 spent a fleet run establishing that the
	// honoured rate saturates at 1.0, and a column showing that in every later
	// run is cheaper than rediscovering it.
	HitRate        float64 `json:"hit_rate" parquet:"hit_rate"`
	HitRateRead    bool    `json:"hit_rate_read" parquet:"hit_rate_read"`
	HitRateQueries float64 `json:"hit_rate_queries" parquet:"hit_rate_queries"`
	// DeclinedMatchBytes is the prefix match the spill rule gave up on this
	// request, in bytes. Zero on every decision that was not a spill.
	//
	// PrefixMatchBytes says what the replica that served the request was
	// believed to hold; this says what was on the table when the rule declined
	// to use it. The pair is what makes a threshold's cost countable: a grid
	// point that spills often but forfeits almost nothing is cheap, and one
	// that spills rarely but gives up whole conversations is not, and the
	// spill count alone cannot tell those apart.
	DeclinedMatchBytes int `json:"declined_match_bytes" parquet:"declined_match_bytes"`
	// DeclinedHonouredRate, DeclinedHonouredRead, DeclinedBatchKVOccupancy,
	// DeclinedBatchKVRead and DeclinedInflight are the pressure on the replica
	// the spill rule turned down. Zero and unread on every decision that
	// declined nothing.
	//
	// The columns above describe the replica that served the request; on a spill
	// that is by construction one *under* the threshold, so without these the
	// figure the rule actually fired on is the one figure the row does not
	// carry. A grid point can then say how often it spilled but not what it was
	// reacting to — which is how the 2026-09-10 run came to show a KV column
	// peaking below the very threshold that was declining matches.
	DeclinedHonouredRate     float64 `json:"declined_honoured_rate" parquet:"declined_honoured_rate"`
	DeclinedHonouredRead     bool    `json:"declined_honoured_read" parquet:"declined_honoured_read"`
	DeclinedHitRate          float64 `json:"declined_hit_rate" parquet:"declined_hit_rate"`
	DeclinedHitRateRead      bool    `json:"declined_hit_rate_read" parquet:"declined_hit_rate_read"`
	DeclinedBatchKVOccupancy float64 `json:"declined_batch_kv_occupancy" parquet:"declined_batch_kv_occupancy"`
	DeclinedBatchKVRead      bool    `json:"declined_batch_kv_read" parquet:"declined_batch_kv_read"`
	DeclinedInflight         int     `json:"declined_inflight" parquet:"declined_inflight"`
	// PrefixMatchTokens and DeclinedMatchTokens are the two match columns above
	// for exact residency, which knows what a replica holds in the engine's own
	// tokens rather than in bytes of prompt. Zero under every other policy, and
	// the byte columns are zero under that one: each prediction is recorded in
	// the unit it was made in. The engine's usage block reports the cached
	// tokens of the same request, so this one can be held to it without any
	// conversion at all.
	PrefixMatchTokens   int `json:"prefix_match_tokens" parquet:"prefix_match_tokens"`
	DeclinedMatchTokens int `json:"declined_match_tokens" parquet:"declined_match_tokens"`
	// TokenizeNs is how long the engine took to tokenize this prompt before
	// exact residency could decide where it went. It is inside RouterOverheadNs,
	// and carried separately so that the cost of knowing exactly can be told
	// apart from the cost of deciding. Zero under every other policy, which
	// never asks.
	TokenizeNs int64 `json:"tokenize_ns" parquet:"tokenize_ns"`

	Model  string `json:"model" parquet:"model"`
	Stream bool   `json:"stream" parquet:"stream"`

	// RouterOverheadNs is accept to upstream dispatch: the router's own cost,
	// reported separately so it is never hidden inside TTFT.
	RouterOverheadNs int64 `json:"router_overhead_ns" parquet:"router_overhead_ns"`
	// TTFTNs is client-observed time to first response byte. Zero when no byte
	// ever reached the client.
	TTFTNs int64 `json:"ttft_ns" parquet:"ttft_ns"`
	// TotalNs is accept to last byte written to the client.
	TotalNs int64 `json:"total_ns" parquet:"total_ns"`

	UpstreamStatus int     `json:"upstream_status" parquet:"upstream_status"`
	ResponseBytes  int64   `json:"response_bytes" parquet:"response_bytes"`
	Outcome        Outcome `json:"outcome" parquet:"outcome"`
	Error          string  `json:"error,omitempty" parquet:"error"`
}

// Writer appends rows as JSONL. It is safe for concurrent use.
//
// It is generic over the row type because there is more than one kind of row.
// The router writes what it observed of a request; the harness writes what the
// client observed of the same request, and a cell summary alongside it. All of
// them want the same durability property — one line, flushed — and a second
// implementation of it would be a second place for a partial write to hide.
type Writer[T any] struct {
	mu  sync.Mutex
	buf *bufio.Writer
	c   io.Closer
}

// Open appends rows to path, creating it if needed. An empty path discards
// rows, so a router can run without a record file.
func Open[T any](path string) (*Writer[T], error) {
	if path == "" {
		return NewWriter[T](io.Discard), nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("record: open %s: %w", path, err)
	}
	w := NewWriter[T](f)
	w.c = f
	return w, nil
}

// NewWriter writes rows to w.
func NewWriter[T any](w io.Writer) *Writer[T] {
	return &Writer[T]{buf: bufio.NewWriter(w)}
}

// Write appends one row and flushes it, so a crash loses at most the row in
// flight.
func (w *Writer[T]) Write(r T) error {
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("record: marshal: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.buf.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("record: write: %w", err)
	}
	return w.buf.Flush()
}

// Close flushes and closes the underlying file. It is idempotent, because the
// callers that want the flush error also defer a Close for the paths that
// return early, and a second close reporting "file already closed" would turn
// a successful run into a failed one.
func (w *Writer[T]) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.buf.Flush(); err != nil {
		return err
	}
	c := w.c
	w.c = nil
	if c != nil {
		return c.Close()
	}
	return nil
}
