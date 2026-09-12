// Package router is the sole ingress to the fleet.
//
// It speaks the OpenAI chat completions API and passes SSE through untouched:
// break streaming and TTFT stops being measurable, which ends the project. It
// also times its own cost from accept to upstream dispatch, and writes one row
// per request, so that the router's overhead is reported separately rather than
// hidden inside the latency the fleet contributes.
package router

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yuchia329/kvroute/internal/belief"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/residency"
	"github.com/yuchia329/kvroute/internal/session"
	"github.com/yuchia329/kvroute/internal/stats"
)

// Headers the router adds to a response. They name the routing decision for
// clients and tests without touching the response body, so SSE bytes stay
// identical to hitting a replica directly.
const (
	ReplicaHeader  = "X-Kvroute-Replica"
	DecisionHeader = "X-Kvroute-Decision"
	RequestHeader  = "X-Kvroute-Request-Id"
	// PrefixMatchHeader carries the prefix match the decision was made on, in
	// bytes. The harness reads it back onto its own row rather than
	// reimplementing the index, which is the same reason the replica and the
	// decision are headers: what the router believed is the router's to report.
	PrefixMatchHeader = "X-Kvroute-Prefix-Match"
	// PrefixMatchTokensHeader carries the match exact residency made its
	// decision on, in the engine's tokens: the same prediction PrefixMatchHeader
	// carries, from the one policy that knows what a replica holds in the
	// engine's own units.
	PrefixMatchTokensHeader = "X-Kvroute-Prefix-Match-Tokens"
	// ReroutesHeader carries how many times the request was moved to another
	// replica before one answered it. A reroute is invisible in the body by
	// design, so this is the only way the harness can count one on its own row.
	ReroutesHeader = "X-Kvroute-Reroutes"
)

// ChatCompletionsPath is the surface clients POST to.
const ChatCompletionsPath = "/v1/chat/completions"

// Config builds a Router. Fleet, Policy and Records are required.
type Config struct {
	Fleet   *fleet.Fleet
	Policy  policy.Policy
	Records *record.Writer[record.Request]

	// Client dispatches to replicas. Defaults to one tuned for many concurrent
	// streaming connections with response compression disabled, so that bodies
	// pass through byte for byte.
	Client *http.Client
	Logger *slog.Logger
	// Residency is what feeds exact residency: every replica's KV cache event
	// stream and the index it keeps. Set only under that policy, and the router
	// does nothing with it but report it.
	Residency *residency.Feed
}

// Router fronts the fleet.
type Router struct {
	fleet    *fleet.Fleet
	policy   policy.Policy
	records  *record.Writer[record.Request]
	client   *http.Client
	log      *slog.Logger
	overhead *stats.Recorder
	metrics  *metrics
	requests atomic.Int64
	// residency is exact residency's feed, reported on /router/stats. Nil under
	// every other policy.
	residency *residency.Feed
	// maxAttempts bounds how many decisions one request may take to place. A
	// replica that fails a request is excluded from that request's next
	// decision, so a request runs out of candidates within one attempt per
	// replica. The bound is twice that, to leave room for the decisions a replica
	// leaving rotation between snapshot and dispatch sends back, and it exists so
	// that a fleet flapping in and out of rotation cannot hold a request in a loop.
	maxAttempts int
}

// Stats is what the router reports about itself.
type Stats struct {
	Policy string `json:"policy"`
	// Spill is the grid point the running policy is tuned to, when it has
	// tunables at all. Absent for the three policies that have none.
	//
	// Published for the same reason the policy name is: the router is started
	// with its configuration and the harness is only told what that was, so this
	// is the only party that knows. A grid of cells labelled with thresholds the
	// router was never running would be a tradeoff curve drawn from one point
	// measured nine times, and no later analysis could detect it.
	Spill *policy.Spill `json:"spill,omitempty"`
	// HashPoint is the grid point the stateless prefix hash is running: how many
	// leading blocks it hashes and what that hash is worth against load. Absent
	// under every other policy, which hash nothing.
	//
	// Published for the reason Spill is, and it is the whole configuration of
	// that policy: its cells are a table indexed by these two numbers, and they
	// reach the router as flags on a separate process.
	HashPoint           *policy.HashPoint   `json:"hash,omitempty"`
	Replicas       []ReplicaStats `json:"replicas"`
	Requests       int64          `json:"requests"`
	RouterOverhead stats.Summary  `json:"router_overhead"`
	// PrefixIndex is the belief the running policy routes on: how many blocks it
	// currently holds, and the cap and TTL it holds them under. Absent under the
	// three policies that consult no index, which is not the same as an index
	// holding nothing.
	//
	// It is reported because the node cap is a modelling decision (ADR-0006) and
	// the occupancy is the evidence for whether that decision ever bound. A run
	// that over-predicted against an index which never filled its cap did not
	// over-predict because of the cap, and #17 calibrates the cap on exactly that
	// distinction.
	PrefixIndex *prefix.Stats `json:"prefix_index,omitempty"`
	// Residency is what exact residency knows of each replica's cache and how
	// complete that knowledge is: the blocks each engine reported holding, the
	// runs the index could not place, and the state of the stream it is fed
	// from. Absent under every other policy.
	//
	// It is the evidence for the cold start ADR-0010 describes. A router that
	// subscribed after its engines had been publishing, or lost history
	// mid-run, says so here, and a cell run against it is a cell whose exact
	// index knew less than it claimed.
	Residency []residency.FeedStats `json:"residency,omitempty"`
}

// IndexReporter is implemented by a policy that routes on a prefix index.
//
// An optional interface rather than a member of policy.Policy: three of the four
// policies have no index, and a method they all had to implement would report
// their absence of one as an index holding nothing.
type IndexReporter interface {
	IndexStats() prefix.Stats
}

// ReplicaStats is one replica's load as the router knows it.
//
// Inflight is here rather than only in the rows because it is the signal the
// load-aware policies decide on, and a signal that can only be reconstructed
// after the run cannot be watched during one.
type ReplicaStats struct {
	ID       string `json:"id"`
	Inflight int    `json:"inflight"`
	// BatchKVOccupancy is the replica's last scraped batch KV occupancy, and
	// BatchKVRead whether any scrape has answered for it. A load signal nothing
	// routes on; see ADR-0011.
	BatchKVOccupancy float64 `json:"batch_kv_occupancy"`
	BatchKVRead      bool    `json:"batch_kv_read"`
	// HonouredRate is how much of what the router has recently claimed of this
	// replica the replica turned out to be holding, HonouredRead whether it has
	// answered enough scoring requests to say, and HonouredClaims how many.
	//
	// Reported here as well as on the rows so that a fleet the signal has gone
	// quiet on is visible while a run is happening rather than only afterwards.
	// The spill rule degrades silently by design — an unread rate declines
	// nothing — and this is the only place that shows before the rows are read
	// back.
	HonouredRate   float64 `json:"honoured_rate"`
	HonouredRead   bool    `json:"honoured_read"`
	HonouredClaims int     `json:"honoured_claims"`
	// Draining and Ejected say whether the router is sending this replica new
	// requests, and if not, why: an operator drained it, or it stopped answering.
	// Both false is a replica in rotation. Two negative flags rather than one
	// positive one, so that stats from a router that predates rotation decode as
	// a fleet in rotation, which is what it was.
	Draining bool `json:"draining"`
	Ejected  bool `json:"ejected"`
	// Ejections is how many times this replica has been ejected since the router
	// started. A cell during which it moved measured a smaller fleet for part of
	// its window, and the harness reads it either side of every cell to say so.
	Ejections int `json:"ejections"`
}

// InRotation reports whether the router is sending this replica new requests.
func (r ReplicaStats) InRotation() bool { return !r.Draining && !r.Ejected }

// Rotation says in words where the replica stands: in rotation, or why not.
func (r ReplicaStats) Rotation() string {
	switch {
	case r.Draining && r.Ejected:
		return "draining and ejected"
	case r.Draining:
		return "draining"
	case r.Ejected:
		return "ejected"
	}
	return "in rotation"
}

// DefaultClient dispatches to replicas.
//
// Compression is disabled and the client's own Accept-Encoding is forwarded
// verbatim, so the transport never transparently gzips or gunzips a body: the
// bytes a replica sends are the bytes the client receives. Idle connections are
// raised well above the default of two per host, because the router holds many
// concurrent streams to six replicas.
func DefaultClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.MaxIdleConns = 512
	transport.MaxIdleConnsPerHost = 256
	// Idle connections are closed by the router before a replica closes them.
	// vLLM's server drops a keep-alive connection after five idle seconds, and a
	// request written onto one in the instant it is being closed fails with
	// nothing emitted — which the router takes for a dead replica, ejecting it
	// and flagging the cell for a replica that was fine. The standard library's
	// ninety seconds leaves that race open on every connection that idles past
	// five; four closes it.
	transport.IdleConnTimeout = 4 * time.Second
	// No overall client timeout: a streaming response is long-lived by design,
	// and the request context already carries client cancellation.
	return &http.Client{Transport: transport}
}

// New builds a router.
func New(cfg Config) (*Router, error) {
	if cfg.Fleet == nil {
		return nil, errors.New("router: fleet is required")
	}
	if cfg.Policy == nil {
		return nil, errors.New("router: policy is required")
	}
	if cfg.Records == nil {
		return nil, errors.New("router: record writer is required")
	}
	if cfg.Client == nil {
		cfg.Client = DefaultClient()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Router{
		fleet:    cfg.Fleet,
		policy:   cfg.Policy,
		records:  cfg.Records,
		client:   cfg.Client,
		log:      cfg.Logger,
		overhead: stats.NewRecorder(stats.DefaultCapacity),
		metrics:  newMetrics(cfg.Policy.Name()),

		residency:   cfg.Residency,
		maxAttempts: 2 * len(cfg.Fleet.Replicas()),
	}, nil
}

// Handler returns the router's HTTP surface.
func (rt *Router) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+ChatCompletionsPath, rt.handleChatCompletions)
	mux.HandleFunc("GET /router/stats", rt.handleStats)
	mux.HandleFunc("POST /router/replicas/{id}/drain", rt.handleDrain)
	mux.HandleFunc("POST /router/replicas/{id}/restore", rt.handleRestore)
	mux.HandleFunc("GET /metrics", rt.handleMetrics)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// Stats reports the router's own cost.
func (rt *Router) Stats() Stats {
	// Every member rather than the routing snapshot: a replica out of rotation is
	// absent from the snapshot by design, and it is exactly the replica whose
	// in-flight count an operator waiting on a drain needs to see.
	members := rt.fleet.Members()
	replicas := make([]ReplicaStats, 0, len(members))
	for _, m := range members {
		replicas = append(replicas, statsOf(m))
	}
	var spill *policy.Spill
	if tuned, ok := rt.policy.(policy.Tuned); ok {
		s := tuned.Tunables()
		spill = &s
	}
	var hashPoint *policy.HashPoint
	if tuned, ok := rt.policy.(policy.HashTuned); ok {
		h := tuned.HashTunables()
		hashPoint = &h
	}
	var index *prefix.Stats
	if reporter, routes := rt.policy.(IndexReporter); routes {
		held := reporter.IndexStats()
		index = &held
	}
	var feedStats []residency.FeedStats
	if rt.residency != nil {
		feedStats = rt.residency.Stats()
	}
	return Stats{
		Policy:         rt.policy.Name(),
		Spill:          spill,
		HashPoint:           hashPoint,
		Replicas:       replicas,
		Requests:       rt.requests.Load(),
		RouterOverhead: rt.overhead.Summary(),
		PrefixIndex:    index,
		Residency:      feedStats,
	}
}

func (rt *Router) handleStats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rt.Stats())
}

// statsOf is one member of the fleet as the router reports it.
func statsOf(m fleet.Member) ReplicaStats {
	return ReplicaStats{
		ID:               m.ID,
		Inflight:         m.Inflight,
		BatchKVOccupancy: m.BatchKV.Fraction,
		BatchKVRead:      m.BatchKV.Read,
		HonouredRate:     m.Honoured.Fraction,
		HonouredRead:     m.Honoured.Read,
		HonouredClaims:   m.Honoured.Claims,
		Draining:         m.Draining,
		Ejected:          m.Ejected,
		Ejections:        m.Ejections,
	}
}

// handleDrain takes a replica out of rotation gracefully: it is given no new
// request, and the ones it is serving finish. handleRestore puts it back, and is
// the only thing that does — see fleet.Restore.
//
// Both answer with the replica as it stands afterwards, so an operator waiting
// for a drain to finish watches its in-flight count fall to zero on the surface
// they drained it through, and stops its process when it gets there.
func (rt *Router) handleDrain(w http.ResponseWriter, req *http.Request) {
	rt.changeRotation(w, req.PathValue("id"), "drain", rt.fleet.Drain)
}

func (rt *Router) handleRestore(w http.ResponseWriter, req *http.Request) {
	rt.changeRotation(w, req.PathValue("id"), "restore", rt.fleet.Restore)
}

// changeRotation applies an operator's drain or restore to one replica.
func (rt *Router) changeRotation(w http.ResponseWriter, id, action string, apply func(string) error) {
	w.Header().Set("Content-Type", "application/json")
	if err := apply(id); err != nil {
		// The fleet's only refusal is an id it does not front, and an operator
		// who named one has mistyped it: a success would leave them waiting on a
		// drain that is not happening.
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object":  "error",
			"message": err.Error(),
			"type":    "RouterError",
			"code":    http.StatusNotFound,
		})
		return
	}
	for _, m := range rt.fleet.Members() {
		if m.ID == id {
			rt.log.Info("replica rotation changed by operator", "replica", id, "action", action, "inflight", m.Inflight)
			_ = json.NewEncoder(w).Encode(statsOf(m))
			return
		}
	}
}

// chatRequestShape is the little the router reads off a request body for the
// record. Everything else is passed upstream untouched.
type chatRequestShape struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func (rt *Router) handleChatCompletions(w http.ResponseWriter, req *http.Request) {
	accepted := time.Now()
	rt.requests.Add(1)

	row := record.Request{
		RequestID: rand.Text(),
		StartedAt: accepted,
		Policy:    rt.policy.Name(),
	}
	defer func() {
		row.TotalNs = time.Since(accepted).Nanoseconds()
		// Counted before the row is written, so anything that has read a row can
		// already see it counted.
		rt.metrics.observe(row)
		if err := rt.records.Write(row); err != nil {
			rt.log.Error("could not write request row", "request_id", row.RequestID, "err", err)
		}
	}()

	body, err := io.ReadAll(req.Body)
	if err != nil {
		rt.drop(w, &row, http.StatusBadRequest, fmt.Errorf("read request body: %w", err))
		return
	}
	var shape chatRequestShape
	// A body the router cannot parse is still forwarded: deciding whether it is
	// valid is the replica's job, and its error is what the client should see.
	_ = json.Unmarshal(body, &shape)
	row.Model, row.Stream = shape.Model, shape.Stream
	// The prompt's own length, which is what a byte-denominated prefix match has
	// to be read through: the index counts in bytes and the engine answers in
	// tokens, and this row is the only place the two can be joined for this one
	// request. See belief.PredictedTokens.
	row.PromptBytes = len(body)

	// Resolved once, here, and then both recorded and handed to the policy. A
	// policy that worked its own identity out could route on a session the row
	// does not mention, and no later analysis could reconstruct why a request
	// went where it did.
	conversation := session.Identify(req.Header, body)
	row.Session, row.SessionDerived = conversation.ID, conversation.Derived

	placed, ok := rt.place(w, req, &row, policy.Request{Header: req.Header, Body: body, Session: conversation, Context: req.Context()}, body)
	if !ok {
		return
	}
	// The request is now committed to a replica, so it counts against that
	// replica until it ends. Released by a defer rather than at each return,
	// because the count has to come down on every path a request can take —
	// success, a replica erroring, a client hanging up, a client timing out — and
	// a release that has to be repeated at five returns is a release that will be
	// missed at the sixth. A count that only ever rises would leave the fleet
	// looking uniformly busy and every later decision made against that.
	defer placed.release()
	defer placed.resp.Body.Close()
	rt.relay(w, req, &row, placed)
}

// placement is a request committed to a replica: the decision that put it
// there, the replica's response with its first byte already read, and the
// release that counts the request out of the replica's inflight.
type placement struct {
	choice  policy.Choice
	resp    *http.Response
	body    *bufio.Reader
	release func()
}

// place commits a request to a replica that has begun to answer it, rerouting
// the request for as long as nothing of it has been emitted.
//
// The boundary is the first byte of the replica's body, drawn there because it
// is the first thing the client could see. Until then the router has written
// nothing — not even the status line, which it holds back although vLLM sends
// its own long before its first token — so a replica that dies in between
// leaves the client nothing to reconcile, and another replica's whole answer is
// indistinguishable from the first one's. From the first byte on the request
// belongs to its replica: see relay.
//
// Only a replica that failed to answer is rerouted from. One that answered with
// an error did answer, and its error goes to the client as it was sent:
// rerouting it would hide an overloaded fleet behind another replica's answer,
// which is the failure the outcome taxonomy exists to expose. A replica that failed to answer
// is ejected on the spot — the passive half of the fleet's health tracking,
// acting on the one piece of evidence a request gives that is certain — and a
// reroute never goes back to a replica that has already failed this request,
// whatever the fleet has decided about it since.
//
// It reports false when the request was never placed, having either answered
// the client with a drop or found the client gone.
func (rt *Router) place(w http.ResponseWriter, req *http.Request, row *record.Request, asked policy.Request, body []byte) (placement, bool) {
	// Allocated on the first failure, so the request that never meets one — all
	// of them, outside a chaos run — pays nothing for it on the hot path.
	var failed map[string]bool
	var failures []string
	dispatched := false
	for range rt.maxAttempts {
		choice, err := rt.policy.Choose(asked, excluding(rt.fleet.State(), failed))
		if err != nil {
			if len(failures) > 0 {
				// Every replica left to try has failed this request, so the last
				// thing that went wrong was a replica, and the client is told so.
				rt.drop(w, row, http.StatusBadGateway, fmt.Errorf("every replica this request was sent to failed before answering it: %s", strings.Join(failures, "; ")))
			} else {
				rt.drop(w, row, http.StatusServiceUnavailable, err)
			}
			return placement{}, false
		}
		recordChoice(row, choice)
		row.Reroutes = len(failed)

		release, err := rt.fleet.Dispatch(choice.Replica.ID)
		if errors.Is(err, fleet.ErrOutOfRotation) {
			// The replica left rotation between the snapshot and the dispatch.
			// Nothing was sent anywhere, so this is a fresh decision rather than a
			// reroute, and the next snapshot does not offer it.
			continue
		}
		if err != nil {
			rt.drop(w, row, http.StatusServiceUnavailable, err)
			return placement{}, false
		}
		if !dispatched {
			// Router overhead ends at the first dispatch. A reroute's wait on the
			// replica that failed is the fleet's latency rather than the router's,
			// and it lands where the client felt it: in TTFT.
			dispatched = true
			row.RouterOverheadNs = time.Since(row.StartedAt).Nanoseconds()
			rt.overhead.Observe(time.Duration(row.RouterOverheadNs))
		}

		resp, first, err := rt.open(req, choice.Replica, body, row.RequestID)
		if err == nil {
			return placement{choice: choice, resp: resp, body: first, release: release}, true
		}
		release()
		if req.Context().Err() != nil {
			// The client went away before any replica answered. No replica failed,
			// and nothing was lost that the client still wanted.
			row.Outcome = record.OutcomeCancelled
			row.Error = err.Error()
			return placement{}, false
		}
		var silent unanswered
		if !errors.As(err, &silent) {
			rt.drop(w, row, http.StatusInternalServerError, err)
			return placement{}, false
		}
		rt.eject(choice.Replica.ID, err)
		if failed == nil {
			failed = map[string]bool{}
		}
		failed[choice.Replica.ID] = true
		failures = append(failures, fmt.Sprintf("%s: %v", choice.Replica.ID, err))
		if row.ReroutedFrom == "" {
			row.ReroutedFrom = choice.Replica.ID
		}
	}
	rt.drop(w, row, http.StatusServiceUnavailable, fmt.Errorf("could not place the request in %d attempts: replicas kept leaving rotation under it", rt.maxAttempts))
	return placement{}, false
}

// unanswered is a replica failing a request before emitting any of its answer:
// refusing the connection, dropping it, or breaking it before the first byte of
// the body. It is the one failure a request can be rerouted from.
type unanswered struct{ err error }

func (u unanswered) Error() string { return u.err.Error() }
func (u unanswered) Unwrap() error { return u.err }

// open sends the request to one replica and waits for the first byte of its
// answer, without writing anything to the client.
func (rt *Router) open(req *http.Request, replica fleet.Replica, body []byte, requestID string) (*http.Response, *bufio.Reader, error) {
	upstream, err := http.NewRequestWithContext(req.Context(), http.MethodPost,
		replica.URL(req.URL.Path, req.URL.RawQuery), bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("build upstream request: %w", err)
	}
	copyHeader(upstream.Header, req.Header)
	upstream.Header.Set(RequestHeader, requestID)

	resp, err := rt.client.Do(upstream)
	if err != nil {
		return nil, nil, unanswered{fmt.Errorf("dispatch: %w", err)}
	}
	// Peeked rather than read, so the byte is still there to be relayed. A body
	// that ends before its first byte is an empty answer, and an empty answer is
	// an answer.
	first := bufio.NewReaderSize(resp.Body, streamBufferSize)
	if _, err := first.Peek(1); err != nil && !errors.Is(err, io.EOF) {
		resp.Body.Close()
		return nil, nil, unanswered{fmt.Errorf("answered %d and broke before its first byte: %w", resp.StatusCode, err)}
	}
	return resp, first, nil
}

// recordChoice puts a decision on the row. A rerouted request records the last
// decision made for it, which is the one that placed it: the row says where the
// request was served and why, and ReroutedFrom says where it failed first.
func recordChoice(row *record.Request, choice policy.Choice) {
	row.Replica = choice.Replica.ID
	row.DecisionReason = string(choice.Reason)
	row.Inflight = choice.Inflight
	row.PrefixMatchBytes = choice.PrefixMatchBytes
	row.BatchKVOccupancy, row.BatchKVRead = choice.BatchKV.Fraction, choice.BatchKV.Read
	row.HonouredRate, row.HonouredRead, row.HonouredClaims = choice.Honoured.Fraction, choice.Honoured.Read, choice.Honoured.Claims
	row.DeclinedMatchBytes = choice.DeclinedMatchBytes
	row.DeclinedBatchKVOccupancy, row.DeclinedBatchKVRead = choice.DeclinedBatchKV.Fraction, choice.DeclinedBatchKV.Read
	row.DeclinedHonouredRate, row.DeclinedHonouredRead = choice.DeclinedHonoured.Fraction, choice.DeclinedHonoured.Read
	row.DeclinedInflight = choice.DeclinedInflight
	row.PrefixMatchTokens, row.DeclinedMatchTokens = choice.PrefixMatchTokens, choice.DeclinedMatchTokens
	row.TokenizeNs = choice.Tokenize.Nanoseconds()
}

// excluding is a snapshot without the replicas that have already failed this
// request. They are usually out of rotation already, because a failure ejects,
// but a reroute must not go back to one whatever the fleet decides about it.
func excluding(state fleet.State, failed map[string]bool) fleet.State {
	if len(failed) == 0 {
		return state
	}
	kept := make([]fleet.Candidate, 0, len(state.Replicas))
	for _, c := range state.Replicas {
		if !failed[c.ID] {
			kept = append(kept, c)
		}
	}
	return fleet.State{Replicas: kept}
}

// eject takes a replica out of rotation on the evidence of a request that could
// not reach it. The health checks readmit it once it answers them again.
func (rt *Router) eject(id string, cause error) {
	ejected, err := rt.fleet.Eject(id)
	switch {
	case err != nil:
		rt.log.Error("could not eject a replica a request found dead", "replica", id, "err", err)
	case ejected:
		rt.log.Warn("ejected a replica a request could not reach", "replica", id, "cause", cause)
	}
}

// relay streams a placed request's answer to the client and books how it ended.
func (rt *Router) relay(w http.ResponseWriter, req *http.Request, row *record.Request, p placement) {
	choice, resp := p.choice, p.resp
	row.UpstreamStatus = resp.StatusCode
	rt.log.Debug("routed",
		"request_id", row.RequestID,
		"replica", choice.Replica.ID,
		"reason", choice.Reason,
		"inflight", choice.Inflight,
		"honoured", choice.Honoured,
		"batch_kv", choice.BatchKV,
		"prefix_match_b", choice.PrefixMatchBytes,
		"declined_match_b", choice.DeclinedMatchBytes,
		"prefix_match_tok", choice.PrefixMatchTokens,
		"declined_match_tok", choice.DeclinedMatchTokens,
		"tokenize_us", float64(choice.Tokenize.Nanoseconds())/1000,
		"declined_honoured", choice.DeclinedHonoured,
		"declined_batch_kv", choice.DeclinedBatchKV,
		"declined_inflight", choice.DeclinedInflight,
		"reroutes", row.Reroutes,
		"stream", row.Stream,
		"overhead_us", float64(row.RouterOverheadNs)/1000,
	)

	copyHeader(w.Header(), resp.Header)
	w.Header().Set(ReplicaHeader, choice.Replica.ID)
	w.Header().Set(DecisionHeader, string(choice.Reason))
	w.Header().Set(RequestHeader, row.RequestID)
	w.Header().Set(PrefixMatchHeader, strconv.Itoa(choice.PrefixMatchBytes))
	w.Header().Set(PrefixMatchTokensHeader, strconv.Itoa(choice.PrefixMatchTokens))
	w.Header().Set(ReroutesHeader, strconv.Itoa(row.Reroutes))
	w.WriteHeader(resp.StatusCode)

	var tail usageTail
	written, firstByte, upstreamErr, clientErr := streamBody(w, p.body, &tail)
	rt.score(row, choice, &tail)
	row.ResponseBytes = written
	if !firstByte.IsZero() {
		row.TTFTNs = firstByte.Sub(row.StartedAt).Nanoseconds()
	}

	// Which side of the exchange broke decides the outcome, and the two are
	// distinguished by which end reported rather than by racing the request
	// context: a client hanging up shows as a failed write to the client, and
	// also aborts the upstream read, so both orderings have to land on
	// cancelled.
	switch {
	case clientErr != nil, upstreamErr != nil && req.Context().Err() != nil:
		// The client went away. No replica errored, so counting this as a
		// failure would put a client's behaviour in the fleet's failure column.
		row.Outcome = record.OutcomeCancelled
		row.Error = cmp.Or(clientErr, upstreamErr).Error()
		rt.log.Debug("client went away mid-response", "request_id", row.RequestID, "replica", choice.Replica.ID)
	case upstreamErr != nil:
		// The replica was lost after the first byte had gone to the client. No
		// other replica's answer can be spliced onto what the client has already
		// read, so the request is dropped rather than rerouted (ADR-0009), and the
		// replica is ejected on the same evidence a failure before the first byte
		// would have given.
		row.Outcome = record.OutcomeDropped
		row.Error = fmt.Sprintf("%s was lost mid-stream after %d bytes: %v", choice.Replica.ID, written, upstreamErr)
		rt.log.Warn("replica lost mid-stream, request dropped", "request_id", row.RequestID, "replica", choice.Replica.ID, "err", upstreamErr)
		rt.eject(choice.Replica.ID, upstreamErr)
		// Abort the client's connection rather than end the response. Ending it
		// would send the stream's terminator, and a client reading a cut-off
		// answer as a finished one is the worst thing this path could produce.
		// The row is still written and the request still counted out: both are
		// deferred, and deferred calls run as the panic unwinds, before the
		// server recovers it.
		panic(http.ErrAbortHandler)
	case resp.StatusCode >= http.StatusBadRequest:
		row.Outcome = record.OutcomeFailed
		row.Error = fmt.Sprintf("upstream status %d", resp.StatusCode)
	default:
		row.Outcome = record.OutcomeSuccess
	}
}

// score folds this request's answer back into what the router believes about
// the replica that served it: what the index claimed that replica was holding,
// against what the engine's usage block says it actually had.
//
// This is the residency signal's whole feed. It rides responses the router
// already proxies, so it costs no scrape and asks the engines for nothing they
// are not already sending; what it costs is the tail buffer and one parse per
// request, after the last byte has gone to the client.
//
// Silence at every step rather than an error. A response with no usage block, a
// replica without --enable-prompt-tokens-details, a policy that claimed nothing
// for this request, a prompt the row cannot convert: each of them means this
// request has nothing to say about what its replica was holding, and the rate
// hears nothing rather than a zero. That is the degradation the spill rule rests
// on, and it is the same shape at every layer — an unfed rate goes unread, and
// an unread rate declines nothing.
func (rt *Router) score(row *record.Request, choice policy.Choice, tail *usageTail) {
	usage, ok := readUsage(tail.buf)
	if !ok {
		return
	}
	claimed, converts := belief.PredictedTokens(choice.PrefixMatchBytes, choice.PrefixMatchTokens, row.PromptBytes, usage.PromptTokens)
	if !converts {
		return
	}
	if err := rt.fleet.ObserveHonoured(choice.Replica.ID, time.Now(), claimed, float64(usage.CachedTokens)); err != nil {
		rt.log.Warn("could not record what a replica honoured", "replica", choice.Replica.ID, "request_id", row.RequestID, "err", err)
	}
}

// drop answers a request the router could never place. The client sees the
// router's own error because there is no upstream error to surface.
func (rt *Router) drop(w http.ResponseWriter, row *record.Request, status int, err error) {
	row.Outcome = record.OutcomeDropped
	row.Error = err.Error()
	rt.log.Error("dropped request", "request_id", row.RequestID, "status", status, "err", err)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(RequestHeader, row.RequestID)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object":  "error",
		"message": err.Error(),
		"type":    "RouterError",
		"code":    status,
	})
}

// streamBufferSize is how much of a replica's response is read at a time. It
// bounds nothing about latency: a read returns as soon as the replica has sent
// anything, and whatever it returned is flushed to the client at once.
const streamBufferSize = 32 * 1024

// streamBody copies the replica's response to the client, flushing every chunk
// so that a token reaches the client when the replica emits it rather than when
// the response ends.
//
// It reports the bytes written, when the first one left, and separately which
// side broke if either did: an upstream error is the replica failing, a client
// error is the client going away, and those are different outcomes. It also
// fills the tail the request's usage block is read back out of.
// The tail is filled after the client's copy has gone out and been flushed, so
// that reading the engine's own account of the request back off the stream
// costs the client nothing in time to first token. The bytes the client
// receives are the replica's, unchanged: nothing here rewrites, buffers or
// reorders them.
func streamBody(w http.ResponseWriter, body io.Reader, tail *usageTail) (written int64, firstByte time.Time, upstreamErr, clientErr error) {
	flusher := http.NewResponseController(w)
	buf := make([]byte, streamBufferSize)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if firstByte.IsZero() {
				firstByte = time.Now()
			}
			wrote, writeErr := w.Write(buf[:n])
			written += int64(wrote)
			if writeErr != nil {
				return written, firstByte, nil, writeErr
			}
			if flushErr := flusher.Flush(); flushErr != nil {
				return written, firstByte, nil, flushErr
			}
			tail.Write(buf[:n])
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, firstByte, nil, nil
			}
			return written, firstByte, readErr, nil
		}
	}
}

// hopByHop headers belong to a single connection and must not be forwarded.
var hopByHop = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func copyHeader(dst, src http.Header) {
	for k, values := range src {
		dst[k] = append([]string(nil), values...)
	}
	for _, k := range hopByHop {
		dst.Del(k)
	}
	// Connection names further headers that are also hop-by-hop.
	for _, name := range src.Values("Connection") {
		dst.Del(name)
	}
}
