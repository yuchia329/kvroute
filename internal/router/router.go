// Package router is the sole ingress to the fleet.
//
// It speaks the OpenAI chat completions API and passes SSE through untouched:
// break streaming and TTFT stops being measurable, which ends the project. It
// also times its own cost from accept to upstream dispatch, and writes one row
// per request, so that the router's overhead is reported separately rather than
// hidden inside the latency the fleet contributes.
package router

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/stats"
)

// Headers the router adds to a response. They name the routing decision for
// clients and tests without touching the response body, so SSE bytes stay
// identical to hitting a replica directly.
const (
	ReplicaHeader  = "X-Kvroute-Replica"
	DecisionHeader = "X-Kvroute-Decision"
	RequestHeader  = "X-Kvroute-Request-Id"
)

// ChatCompletionsPath is the surface clients POST to.
const ChatCompletionsPath = "/v1/chat/completions"

// Config builds a Router. Fleet, Policy and Records are required.
type Config struct {
	Fleet   *fleet.Fleet
	Policy  policy.Policy
	Records *record.Writer

	// Client dispatches to replicas. Defaults to one tuned for many concurrent
	// streaming connections with response compression disabled, so that bodies
	// pass through byte for byte.
	Client *http.Client
	Logger *slog.Logger

	// OverheadCapacity bounds how many overhead samples are retained for the
	// percentile summary. Zero uses the default.
	OverheadCapacity int
}

// Router fronts the fleet.
type Router struct {
	fleet    *fleet.Fleet
	policy   policy.Policy
	records  *record.Writer
	client   *http.Client
	log      *slog.Logger
	overhead *stats.Recorder
	requests atomic.Int64
}

// Stats is what the router reports about itself.
type Stats struct {
	Policy         string        `json:"policy"`
	Replicas       []string      `json:"replicas"`
	Requests       int64         `json:"requests"`
	RouterOverhead stats.Summary `json:"router_overhead"`
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
		overhead: stats.NewRecorder(cfg.OverheadCapacity),
	}, nil
}

// Handler returns the router's HTTP surface.
func (rt *Router) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+ChatCompletionsPath, rt.handleChatCompletions)
	mux.HandleFunc("GET /router/stats", rt.handleStats)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// Stats reports the router's own cost.
func (rt *Router) Stats() Stats {
	state := rt.fleet.State()
	ids := make([]string, 0, len(state.Replicas))
	for _, r := range state.Replicas {
		ids = append(ids, r.ID)
	}
	return Stats{
		Policy:         rt.policy.Name(),
		Replicas:       ids,
		Requests:       rt.requests.Load(),
		RouterOverhead: rt.overhead.Summary(),
	}
}

func (rt *Router) handleStats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rt.Stats())
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

	choice, err := rt.policy.Choose(policy.Request{Header: req.Header, Body: body}, rt.fleet.State())
	if err != nil {
		rt.drop(w, &row, http.StatusServiceUnavailable, err)
		return
	}
	row.Replica = choice.Replica.ID
	row.DecisionReason = string(choice.Reason)

	upstream, err := http.NewRequestWithContext(req.Context(), http.MethodPost,
		choice.Replica.BaseURL+req.URL.Path, bytes.NewReader(body))
	if err != nil {
		rt.drop(w, &row, http.StatusInternalServerError, fmt.Errorf("build upstream request: %w", err))
		return
	}
	upstream.URL.RawQuery = req.URL.RawQuery
	copyHeader(upstream.Header, req.Header)
	upstream.Header.Set(RequestHeader, row.RequestID)

	// Router overhead ends here: everything after this point is the fleet's
	// latency, not the router's.
	row.RouterOverheadNs = time.Since(accepted).Nanoseconds()
	rt.overhead.Observe(time.Duration(row.RouterOverheadNs))

	resp, err := rt.client.Do(upstream)
	if err != nil {
		rt.drop(w, &row, http.StatusBadGateway, fmt.Errorf("dispatch to %s: %w", choice.Replica.ID, err))
		return
	}
	defer resp.Body.Close()

	row.UpstreamStatus = resp.StatusCode
	rt.log.Debug("routed",
		"request_id", row.RequestID,
		"replica", choice.Replica.ID,
		"reason", choice.Reason,
		"stream", row.Stream,
		"overhead_us", float64(row.RouterOverheadNs)/1000,
	)

	copyHeader(w.Header(), resp.Header)
	w.Header().Set(ReplicaHeader, choice.Replica.ID)
	w.Header().Set(DecisionHeader, string(choice.Reason))
	w.Header().Set(RequestHeader, row.RequestID)
	w.WriteHeader(resp.StatusCode)

	written, firstByte, copyErr := streamBody(w, resp.Body)
	row.ResponseBytes = written
	if !firstByte.IsZero() {
		row.TTFTNs = firstByte.Sub(accepted).Nanoseconds()
	}

	switch {
	case copyErr != nil:
		// The replica accepted the request and the exchange then broke, so this
		// is a failure rather than a request the router could not place.
		row.Outcome = record.OutcomeFailed
		row.Error = copyErr.Error()
		rt.log.Warn("stream broke mid-response", "request_id", row.RequestID, "replica", choice.Replica.ID, "err", copyErr)
	case resp.StatusCode >= http.StatusBadRequest:
		row.Outcome = record.OutcomeFailed
		row.Error = fmt.Sprintf("upstream status %d", resp.StatusCode)
	default:
		row.Outcome = record.OutcomeSuccess
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

// streamBody copies the replica's response to the client, flushing every chunk
// so that a token reaches the client when the replica emits it rather than when
// the response ends. It reports the bytes written and when the first one left.
func streamBody(w http.ResponseWriter, body io.Reader) (written int64, firstByte time.Time, err error) {
	flusher := http.NewResponseController(w)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if firstByte.IsZero() {
				firstByte = time.Now()
			}
			wrote, writeErr := w.Write(buf[:n])
			written += int64(wrote)
			if writeErr != nil {
				return written, firstByte, writeErr
			}
			if flushErr := flusher.Flush(); flushErr != nil {
				return written, firstByte, flushErr
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, firstByte, nil
			}
			return written, firstByte, readErr
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
