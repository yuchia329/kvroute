package router_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/router"
)

const streamingRequest = `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello"}],"stream":true}`
const blockingRequest = `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello"}],"stream":false}`

// pinnedClock makes the fake's responses byte-deterministic, so that a stream
// taken directly and a stream taken through the router can be compared byte for
// byte.
func pinnedClock() func() time.Time {
	return func() time.Time { return time.Unix(1757000000, 0) }
}

// startFake runs one fake replica and returns it with its base URL.
func startFake(t *testing.T, cfg fakereplica.Config) (*fakereplica.Replica, string) {
	t.Helper()
	if cfg.Now == nil {
		cfg.Now = pinnedClock()
	}
	replica := fakereplica.New(cfg)
	srv := httptest.NewServer(replica.Handler())
	t.Cleanup(srv.Close)
	return replica, srv.URL
}

// rowSink collects the per-request rows the router writes. The router writes a
// row only after the client has already received the last byte, so a test that
// wants a row has to wait for it rather than assume it has landed.
type rowSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *rowSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *rowSink) parse(t *testing.T) []record.Request {
	t.Helper()
	s.mu.Lock()
	contents := s.buf.String()
	s.mu.Unlock()

	var out []record.Request
	for line := range strings.SplitSeq(strings.TrimSpace(contents), "\n") {
		if line == "" {
			continue
		}
		var r record.Request
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("row %q is not valid JSON: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

// wait returns the rows once want of them have been written, failing if they do
// not arrive.
func (s *rowSink) wait(t *testing.T, want int) []record.Request {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := s.parse(t)
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("wrote %d rows, want %d", len(got), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// startRouter runs a router in front of the given replica specs, returning its
// base URL and the sink its per-request rows are written to.
func startRouter(t *testing.T, specs ...string) (string, *rowSink) {
	t.Helper()
	replicas, err := fleet.ParseSpecs(specs)
	if err != nil {
		t.Fatalf("parse replica specs: %v", err)
	}
	f, err := fleet.New(replicas)
	if err != nil {
		t.Fatalf("new fleet: %v", err)
	}
	rows := &rowSink{}
	r, err := router.New(router.Config{
		Fleet:   f,
		Policy:  policy.NewRoundRobin(),
		Records: record.NewWriter(rows),
	})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)
	return srv.URL, rows
}

type response struct {
	status      int
	contentType string
	body        []byte
	replica     string
}

func post(t *testing.T, baseURL, body string) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response{
		status:      resp.StatusCode,
		contentType: resp.Header.Get("Content-Type"),
		body:        got,
		replica:     resp.Header.Get(router.ReplicaHeader),
	}
}

// TestStreamingBytesAreIdenticalToHittingTheReplicaDirectly is the criterion the
// whole project rests on: break SSE fidelity and TTFT stops being measurable.
//
// It also asserts request fidelity for free, because the fake derives its
// completion id from a hash of the request body — a router that altered the
// body upstream would produce a different id.
func TestStreamingBytesAreIdenticalToHittingTheReplicaDirectly(t *testing.T) {
	_, replicaURL := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 4})
	routerURL, _ := startRouter(t, "replica-0="+replicaURL)

	direct := post(t, replicaURL, streamingRequest)
	routed := post(t, routerURL, streamingRequest)

	if direct.status != routed.status {
		t.Fatalf("status: direct %d, routed %d", direct.status, routed.status)
	}
	if direct.contentType != routed.contentType {
		t.Errorf("content type: direct %q, routed %q", direct.contentType, routed.contentType)
	}
	if !bytes.Equal(direct.body, routed.body) {
		t.Errorf("SSE bytes differ\ndirect: %q\nrouted: %q", direct.body, routed.body)
	}
	if !bytes.HasSuffix(routed.body, []byte("data: [DONE]\n\n")) {
		t.Errorf("routed stream does not terminate with [DONE]: %q", routed.body)
	}
	if routed.replica != "replica-0" {
		t.Errorf("chosen replica header = %q, want replica-0", routed.replica)
	}
}

func TestNonStreamingBytesAreIdenticalToHittingTheReplicaDirectly(t *testing.T) {
	_, replicaURL := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 4})
	routerURL, rows := startRouter(t, "replica-0="+replicaURL)

	direct := post(t, replicaURL, blockingRequest)
	routed := post(t, routerURL, blockingRequest)

	if routed.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", routed.status)
	}
	if !bytes.Equal(direct.body, routed.body) {
		t.Errorf("body differs\ndirect: %s\nrouted: %s", direct.body, routed.body)
	}
	got := rows.wait(t, 1)
	if got[0].Stream {
		t.Errorf("row marked as streaming, want non-streaming")
	}
}

// TestUpstreamErrorSurfacesFaithfully: a replica's own error must reach the
// client as the replica sent it, not be masked as a router failure.
func TestUpstreamErrorSurfacesFaithfully(t *testing.T) {
	replica, replicaURL := startFake(t, fakereplica.Config{ID: "replica-0"})
	replica.SetFailure(&fakereplica.Failure{
		Status:  http.StatusServiceUnavailable,
		Type:    "ServiceUnavailableError",
		Message: "engine is out of KV blocks",
	})
	routerURL, rows := startRouter(t, "replica-0="+replicaURL)

	direct := post(t, replicaURL, blockingRequest)
	routed := post(t, routerURL, blockingRequest)

	if routed.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", routed.status)
	}
	if !bytes.Equal(direct.body, routed.body) {
		t.Errorf("error body differs\ndirect: %s\nrouted: %s", direct.body, routed.body)
	}
	if !bytes.Contains(routed.body, []byte("engine is out of KV blocks")) {
		t.Errorf("upstream error message was lost: %s", routed.body)
	}

	got := rows.wait(t, 1)
	if got[0].Outcome != record.OutcomeFailed {
		t.Errorf("outcome = %q, want %q: the replica accepted and then errored", got[0].Outcome, record.OutcomeFailed)
	}
	if got[0].UpstreamStatus != http.StatusServiceUnavailable {
		t.Errorf("upstream_status = %d, want 503", got[0].UpstreamStatus)
	}
}

// TestUnreachableReplicaIsDropped separates the router's own failure from the
// replica's: a request the router could never place is dropped, not failed.
func TestUnreachableReplicaIsDropped(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	routerURL, rows := startRouter(t, "replica-0="+deadURL)
	routed := post(t, routerURL, blockingRequest)

	if routed.status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", routed.status)
	}
	got := rows.wait(t, 1)
	if got[0].Outcome != record.OutcomeDropped {
		t.Errorf("outcome = %q, want %q", got[0].Outcome, record.OutcomeDropped)
	}
	if got[0].Error == "" {
		t.Errorf("dropped row carries no error text")
	}
}

// TestOneRowPerRequestCarriesTimingsAndDecision is the record contract the
// harness reads later.
func TestOneRowPerRequestCarriesTimingsAndDecision(t *testing.T) {
	_, replicaURL := startFake(t, fakereplica.Config{ID: "replica-0", TTFT: 5 * time.Millisecond, OutputTokens: 3})
	routerURL, rows := startRouter(t, "replica-0="+replicaURL)

	const requests = 3
	for range requests {
		if got := post(t, routerURL, streamingRequest); got.status != http.StatusOK {
			t.Fatalf("status = %d, want 200", got.status)
		}
	}

	got := rows.wait(t, requests)
	seen := map[string]bool{}
	for i, row := range got {
		if row.RequestID == "" {
			t.Errorf("row %d has no request id", i)
		}
		if seen[row.RequestID] {
			t.Errorf("row %d reuses request id %q", i, row.RequestID)
		}
		seen[row.RequestID] = true

		if row.Replica != "replica-0" {
			t.Errorf("row %d replica = %q, want replica-0", i, row.Replica)
		}
		if row.DecisionReason != string(policy.ReasonRoundRobin) {
			t.Errorf("row %d decision_reason = %q, want %q", i, row.DecisionReason, policy.ReasonRoundRobin)
		}
		if row.Policy != policy.RoundRobinName {
			t.Errorf("row %d policy = %q, want %q", i, row.Policy, policy.RoundRobinName)
		}
		if !row.Stream {
			t.Errorf("row %d not marked as streaming", i)
		}
		if row.Outcome != record.OutcomeSuccess {
			t.Errorf("row %d outcome = %q, want success", i, row.Outcome)
		}
		if row.RouterOverheadNs <= 0 {
			t.Errorf("row %d router_overhead_ns = %d, want positive", i, row.RouterOverheadNs)
		}
		if row.TTFTNs <= 0 {
			t.Errorf("row %d ttft_ns = %d, want positive", i, row.TTFTNs)
		}
		if row.TotalNs < row.TTFTNs {
			t.Errorf("row %d total_ns %d < ttft_ns %d", i, row.TotalNs, row.TTFTNs)
		}
		if row.RouterOverheadNs >= row.TTFTNs {
			t.Errorf("row %d overhead %dns is not inside TTFT %dns", i, row.RouterOverheadNs, row.TTFTNs)
		}
		if row.ResponseBytes <= 0 {
			t.Errorf("row %d response_bytes = %d, want positive", i, row.ResponseBytes)
		}
	}
}

// TestRouterOverheadIsReportedAsPercentiles: the router's own cost is a
// reported result, not something hidden inside TTFT.
func TestRouterOverheadIsReportedAsPercentiles(t *testing.T) {
	_, replicaURL := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 2})
	routerURL, _ := startRouter(t, "replica-0="+replicaURL)

	const requests = 5
	for range requests {
		post(t, routerURL, streamingRequest)
	}

	resp, err := http.Get(routerURL + "/router/stats")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d, want 200", resp.StatusCode)
	}
	var stats router.Stats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.Policy != policy.RoundRobinName {
		t.Errorf("stats policy = %q, want %q", stats.Policy, policy.RoundRobinName)
	}
	if stats.RouterOverhead.Observed != requests {
		t.Errorf("observed = %d, want %d", stats.RouterOverhead.Observed, requests)
	}
	if stats.RouterOverhead.P50Us <= 0 || stats.RouterOverhead.P99Us <= 0 {
		t.Errorf("overhead percentiles not populated: p50=%v p99=%v", stats.RouterOverhead.P50Us, stats.RouterOverhead.P99Us)
	}
	if stats.RouterOverhead.P99Us < stats.RouterOverhead.P50Us {
		t.Errorf("p99 %v < p50 %v", stats.RouterOverhead.P99Us, stats.RouterOverhead.P50Us)
	}
}

// TestStreamIsNotBuffered: the first token must reach the client long before
// the stream ends, or TTFT measures the whole response.
func TestStreamIsNotBuffered(t *testing.T) {
	const (
		ttft       = 10 * time.Millisecond
		interToken = 40 * time.Millisecond
		tokens     = 5
	)
	_, replicaURL := startFake(t, fakereplica.Config{
		ID: "replica-0", TTFT: ttft, InterToken: interToken, OutputTokens: tokens,
	})
	routerURL, _ := startRouter(t, "replica-0="+replicaURL)

	start := time.Now()
	req, err := http.NewRequest(http.MethodPost, routerURL+"/v1/chat/completions", strings.NewReader(streamingRequest))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 1)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("read first byte: %v", err)
	}
	firstByte := time.Since(start)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("drain: %v", err)
	}
	total := time.Since(start)

	wholeStream := ttft + (tokens-1)*interToken
	if total < wholeStream {
		t.Fatalf("stream finished in %v, faster than the fake can produce it (%v)", total, wholeStream)
	}
	if firstByte > total/2 {
		t.Errorf("first byte took %v of a %v stream: the router is buffering", firstByte, total)
	}
}
