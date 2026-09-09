package router_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/prefix"
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

// routed is a running router and the two things a test needs to look at behind
// it: the rows it wrote, and the fleet whose load it is accounting for.
type routed struct {
	url   string
	rows  *rowSink
	fleet *fleet.Fleet
}

// startRouterWith runs a router under a given policy in front of the given
// replica specs.
func startRouterWith(t *testing.T, chosen policy.Policy, specs ...string) routed {
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
		Policy:  chosen,
		Records: record.NewWriter[record.Request](rows),
	})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)
	return routed{url: srv.URL, rows: rows, fleet: f}
}

// startRouter runs a round-robin router in front of the given replica specs,
// returning its base URL and the sink its per-request rows are written to.
func startRouter(t *testing.T, specs ...string) (string, *rowSink) {
	t.Helper()
	r := startRouterWith(t, policy.NewRoundRobin(), specs...)
	return r.url, r.rows
}

// inflight totals what the router believes is in flight across the fleet.
func (r routed) inflight(t *testing.T) int {
	t.Helper()
	total := 0
	for _, c := range r.fleet.State().Replicas {
		total += c.Inflight
	}
	return total
}

// drains waits for the router's inflight count to come back to zero.
//
// Polled rather than read once: the client is answered from inside the handler,
// so a test that read the count the instant its request returned would be racing
// the release rather than checking it.
func (r routed) drains(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := r.inflight(t); got == 0 {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("inflight stayed at %d: a request that ended was never counted out, so this replica looks busy for the rest of the run", got)
		}
		time.Sleep(time.Millisecond)
	}
}

type response struct {
	status      int
	contentType string
	body        []byte
	replica     string
	// err is set when the request never produced a response at all, for the
	// callers that send from their own goroutine and have to report it back on
	// the test's.
	err error
}

func post(t *testing.T, baseURL, body string) response {
	t.Helper()
	got, err := send(baseURL, body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return got
}

// send is post without the test: the tests that build a window of concurrent
// arrivals send from their own goroutines, where failing the test directly is not
// allowed. The error comes back with the response so the caller can fail on the
// test's own goroutine.
func send(baseURL, body string) (response, error) {
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		return response{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		return response{}, fmt.Errorf("read body: %w", err)
	}
	return response{
		status:      resp.StatusCode,
		contentType: resp.Header.Get("Content-Type"),
		body:        got,
		replica:     resp.Header.Get(router.ReplicaHeader),
	}, nil
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

// TestClientHangingUpIsNotCountedAsAReplicaFailure: dropped, failed and
// cancelled are three different things, and a client's own disconnect must not
// land in the column that says the fleet errored.
func TestClientHangingUpIsNotCountedAsAReplicaFailure(t *testing.T) {
	_, replicaURL := startFake(t, fakereplica.Config{
		ID: "replica-0", TTFT: 5 * time.Millisecond, InterToken: time.Second, OutputTokens: 10,
	})
	routerURL, rows := startRouter(t, "replica-0="+replicaURL)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, routerURL+"/v1/chat/completions", strings.NewReader(streamingRequest))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	// Take the first chunk, then walk away mid-stream.
	if _, err := resp.Body.Read(make([]byte, 1)); err != nil {
		t.Fatalf("read first byte: %v", err)
	}
	cancel()
	resp.Body.Close()

	got := rows.wait(t, 1)
	if got[0].Outcome != record.OutcomeCancelled {
		t.Errorf("outcome = %q, want %q: no replica errored here", got[0].Outcome, record.OutcomeCancelled)
	}
	if got[0].TTFTNs <= 0 {
		t.Errorf("ttft_ns = %d: the client did receive a first byte", got[0].TTFTNs)
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

// pollingWindow is the shortest interval §4.5 considers scraping load on. It is
// here as the bound the stampede test has to stay inside: a window of arrivals
// that took longer than one polling interval to arrive would not be
// demonstrating anything about arrivals inside one.
const pollingWindow = 250 * time.Millisecond

// startGate runs a replica that announces every arrival as it lands, and — when
// hold is set — keeps the request until the test releases it.
//
// It replaces sleeping with waiting. A stampede test built on delays asserts that
// requests overlapped; this one knows they did, because the next request is not
// sent until the previous one is provably sitting on a replica.
func startGate(t *testing.T, id string, arrived chan<- string, release <-chan struct{}, hold bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		arrived <- id
		if hold {
			select {
			case <-release:
			case <-req.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestArrivalsInsideOnePollingWindowDoNotStampedeOneReplica is the acceptance
// criterion for counting inflight locally, at the level where the counting is
// wired up.
//
// One replica holds the first request it is given for the whole window; the other
// five answer immediately. So from the second arrival onwards the fleet is
// genuinely uneven, and there is exactly one wrong answer: the replica that is
// already busy. Requests are sent one at a time, each only after the previous is
// provably somewhere, so every decision but the first is made with a request
// visibly in flight.
//
// This is what a scraped count could not do. Between two scrapes the busy
// replica still reads as idle, so it stays the most attractive candidate for
// every arrival in the window — and the window is 250 ms to 1 s wide, which at
// any serving rate is a great many arrivals. It is also why the tie-break is not
// enough on its own: with no counts to tell them apart the policy would rotate
// through all six and hand the held replica a second request.
//
// The same comparison against a stale view, at the policy level, is
// TestExactCountsPreventTheHerdAStaleViewWouldCause.
func TestArrivalsInsideOnePollingWindowDoNotStampedeOneReplica(t *testing.T) {
	const (
		replicas = 6
		// More arrivals than replicas, so a policy rotating blindly would come
		// back around to the held replica.
		arrivals = 12
		// The first request lands on the first replica: every candidate is tied at
		// zero, and the tie-break takes them in fleet order.
		held = "replica-0"
	)

	arrived := make(chan string, arrivals)
	release := make(chan struct{})
	specs := make([]string, 0, replicas)
	for i := range replicas {
		id := fmt.Sprintf("replica-%d", i)
		specs = append(specs, id+"="+startGate(t, id, arrived, release, id == held))
	}

	rt := startRouterWith(t, policy.NewLeastOutstanding(), specs...)

	done := make(chan response, arrivals)
	seen := map[string]int{}
	start := time.Now()
	for i := range arrivals {
		go func() {
			got, err := send(rt.url, blockingRequest)
			got.err = err
			done <- got
		}()

		var landed string
		select {
		case landed = <-arrived:
			seen[landed]++
		case <-time.After(5 * time.Second):
			t.Fatalf("request %d never reached a replica; arrivals so far: %v", i, seen)
		}
		if landed == held {
			// It stays in flight for the rest of the window, which is the whole
			// point: the fleet is uneven from here on.
			continue
		}
		// Every other request is waited out, so the only thing the next decision
		// can see in flight is the held one.
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("request %d to %s never came back", i, landed)
		}
	}
	window := time.Since(start)
	close(release)
	select {
	case got := <-done:
		if got.err != nil {
			t.Errorf("the held request failed: %v", got.err)
		} else if got.status != http.StatusOK {
			t.Errorf("the held request returned %d, want 200", got.status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held request never came back after release")
	}

	if got := seen[held]; got != 1 {
		t.Errorf("%s took %d of %d arrivals, want 1: it was busy from the first one onward, and a request in flight is what a scraped count would not have shown",
			held, got, arrivals)
	}
	if len(seen) != replicas {
		t.Errorf("%d arrivals landed on %d of %d replicas (%v), want every replica used", arrivals, len(seen), replicas, seen)
	}
	if window > pollingWindow {
		t.Errorf("the %d arrivals took %v, longer than the %v polling window they are meant to fit inside, so this run demonstrated nothing",
			arrivals, window, pollingWindow)
	}
	rt.drains(t)
}

// TestInflightReturnsToZeroOnEveryOutcome. A request that ends without being
// counted out leaves its replica looking permanently busier than it is, and every
// later decision is made against that. The four outcomes are four separate
// return paths through the handler, so each one is checked.
func TestInflightReturnsToZeroOnEveryOutcome(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		_, replicaURL := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 2})
		rt := startRouterWith(t, policy.NewLeastOutstanding(), "replica-0="+replicaURL)

		if got := post(t, rt.url, streamingRequest); got.status != http.StatusOK {
			t.Fatalf("status = %d, want 200", got.status)
		}
		rt.drains(t)
	})

	t.Run("replica errored", func(t *testing.T) {
		replica, replicaURL := startFake(t, fakereplica.Config{ID: "replica-0"})
		replica.SetFailure(&fakereplica.Failure{Status: http.StatusServiceUnavailable, Message: "no KV blocks"})
		rt := startRouterWith(t, policy.NewLeastOutstanding(), "replica-0="+replicaURL)

		if got := post(t, rt.url, blockingRequest); got.status != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", got.status)
		}
		rt.drains(t)
	})

	t.Run("replica unreachable", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		deadURL := dead.URL
		dead.Close()
		rt := startRouterWith(t, policy.NewLeastOutstanding(), "replica-0="+deadURL)

		if got := post(t, rt.url, blockingRequest); got.status != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", got.status)
		}
		rt.drains(t)
	})

	t.Run("client disconnected mid-stream", func(t *testing.T) {
		_, replicaURL := startFake(t, fakereplica.Config{
			ID: "replica-0", TTFT: time.Millisecond, InterToken: time.Second, OutputTokens: 10,
		})
		rt := startRouterWith(t, policy.NewLeastOutstanding(), "replica-0="+replicaURL)

		ctx, cancel := context.WithCancel(context.Background())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rt.url+"/v1/chat/completions", strings.NewReader(streamingRequest))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		if _, err := resp.Body.Read(make([]byte, 1)); err != nil {
			t.Fatalf("read first byte: %v", err)
		}
		cancel()
		resp.Body.Close()
		rt.drains(t)
	})

	t.Run("client timed out waiting", func(t *testing.T) {
		// The router deliberately sets no timeout of its own — a streaming
		// response is long-lived by design — so a timeout is always the client's,
		// and it reaches the handler as a cancelled context mid-response.
		_, replicaURL := startFake(t, fakereplica.Config{
			ID: "replica-0", TTFT: time.Millisecond, InterToken: time.Second, OutputTokens: 10,
		})
		rt := startRouterWith(t, policy.NewLeastOutstanding(), "replica-0="+replicaURL)

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rt.url+"/v1/chat/completions", strings.NewReader(streamingRequest))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		rt.drains(t)
	})
}

// TestInflightDoesNotDriftUpwardOverManyRequests: the counters are the load
// signal every load-aware policy reads, and a slow leak in them is invisible
// until the fleet looks uniformly busy and the policy has nothing left to
// choose by.
func TestInflightDoesNotDriftUpwardOverManyRequests(t *testing.T) {
	_, healthy := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 2})
	sick, sickURL := startFake(t, fakereplica.Config{ID: "replica-1"})
	sick.SetFailure(&fakereplica.Failure{Status: http.StatusInternalServerError, Message: "engine died"})
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	rt := startRouterWith(t, policy.NewLeastOutstanding(),
		"replica-0="+healthy, "replica-1="+sickURL, "replica-2="+deadURL)

	const requests = 30
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Whatever came back, the request ended, which is all this test is
			// about: the count has to come down for every one of them.
			_, _ = send(rt.url, streamingRequest)
		}()
	}
	wg.Wait()
	rt.drains(t)
}

// TestLoadIsCountedWithoutScrapingAnyReplica. Inflight is the router's own
// count, and the herding argument only holds if nothing quietly turns it back
// into a scraped figure. Nothing in the routing path may touch a replica's
// /metrics.
func TestLoadIsCountedWithoutScrapingAnyReplica(t *testing.T) {
	var scrapes atomic.Int64
	replica := fakereplica.New(fakereplica.Config{ID: "replica-0", OutputTokens: 2, Now: pinnedClock()})
	handler := replica.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != router.ChatCompletionsPath {
			scrapes.Add(1)
		}
		handler.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)

	rt := startRouterWith(t, policy.NewLeastOutstanding(), "replica-0="+srv.URL)
	for range 5 {
		if got := post(t, rt.url, streamingRequest); got.status != http.StatusOK {
			t.Fatalf("status = %d, want 200", got.status)
		}
	}

	if got := scrapes.Load(); got != 0 {
		t.Errorf("the router made %d requests to the replica outside the routing path: inflight must never come from a scrape", got)
	}
	rows := rt.rows.wait(t, 5)
	for i, row := range rows {
		if row.DecisionReason != string(policy.ReasonLeastOutstanding) {
			t.Errorf("row %d decision_reason = %q, want %q", i, row.DecisionReason, policy.ReasonLeastOutstanding)
		}
	}
}

// TestStatsReportInflightPerReplica: the count is the load signal, so it has to
// be visible while a run is happening rather than only inferable afterwards.
func TestStatsReportInflightPerReplica(t *testing.T) {
	arrived := make(chan string, 1)
	release := make(chan struct{})
	gateURL := startGate(t, "replica-0", arrived, release, true)
	_, idleURL := startFake(t, fakereplica.Config{ID: "replica-1", OutputTokens: 2})

	rt := startRouterWith(t, policy.NewLeastOutstanding(), "replica-0="+gateURL, "replica-1="+idleURL)

	go func() { _, _ = send(rt.url, blockingRequest) }()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the gated replica")
	}

	resp, err := http.Get(rt.url + "/router/stats")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	defer resp.Body.Close()
	var stats router.Stats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	close(release)

	byID := map[string]int{}
	for _, r := range stats.Replicas {
		byID[r.ID] = r.Inflight
	}
	if byID["replica-0"] != 1 {
		t.Errorf("replica-0 inflight = %d, want 1: a request is sitting on it", byID["replica-0"])
	}
	if byID["replica-1"] != 0 {
		t.Errorf("replica-1 inflight = %d, want 0: nothing was dispatched to it", byID["replica-1"])
	}
	rt.drains(t)
}

// TestAConcurrentBurstIsSpreadRatherThanPiledOntoOneReplica covers what the
// serialised test above cannot: arrivals that are not merely inside one polling
// window but genuinely simultaneous.
//
// Two mechanisms separate a burst, and they cover each other. Where the decisions
// happen to be ordered, the counts separate them: each pick lifts that replica out
// of the minimum. Where they are genuinely simultaneous and read one snapshot, the
// tie-break rotation separates them, because its counter is atomic and two
// choosers take different turns of it. Removing either one alone still spreads
// this burst; removing both puts all twelve requests on one replica, which is the
// stampede. So the test asserts that the burst was spread rather than that it was
// split evenly, and the serialised test above is the one that isolates the counts.
//
// The bound is a third of the burst against the whole of it, which is where a
// stale count would have put it.
func TestAConcurrentBurstIsSpreadRatherThanPiledOntoOneReplica(t *testing.T) {
	const (
		replicas = 6
		arrivals = 12
	)

	arrived := make(chan string, arrivals)
	release := make(chan struct{})
	specs := make([]string, 0, replicas)
	for i := range replicas {
		id := fmt.Sprintf("replica-%d", i)
		// Every replica holds its requests, so nothing completes and drains the
		// count before the rest of the burst has been placed.
		specs = append(specs, id+"="+startGate(t, id, arrived, release, true))
	}

	rt := startRouterWith(t, policy.NewLeastOutstanding(), specs...)

	// Released together rather than sent in a loop: a loop would stagger them by
	// however long a round of dispatch takes, which is the case the serialised test
	// already covers.
	start := make(chan struct{})
	done := make(chan response, arrivals)
	for range arrivals {
		go func() {
			<-start
			got, err := send(rt.url, blockingRequest)
			got.err = err
			done <- got
		}()
	}
	close(start)

	seen := map[string]int{}
	for range arrivals {
		select {
		case id := <-arrived:
			seen[id]++
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d arrivals landed: %v", len(seen), arrivals, seen)
		}
	}
	close(release)
	for range arrivals {
		if got := <-done; got.err != nil {
			t.Errorf("a request in the burst failed: %v", got.err)
		}
	}

	worst := 0
	for _, n := range seen {
		worst = max(worst, n)
	}
	if worst > arrivals/3 {
		t.Errorf("%d of a %d-request burst went to one replica (%v): a burst must be spread, not piled",
			arrivals, worst, seen)
	}
	if len(seen) < replicas/2 {
		t.Errorf("a %d-request burst used only %d of %d replicas (%v)", arrivals, len(seen), replicas, seen)
	}
	rt.drains(t)
}

// The router's prediction has to reach both the row and the client, because two
// different readers need it. The row is where belief divergence is computed
// from, and the header is how the harness puts the figure on its own row without
// reimplementing the index.
func TestThePrefixMatchReachesBothTheRowAndTheResponse(t *testing.T) {
	_, base := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 2})
	index, err := prefix.New(prefix.Config{NodeCap: 1024, TTL: time.Hour})
	if err != nil {
		t.Fatalf("prefix.New: %v", err)
	}
	rt := startRouterWith(t, policy.NewPrefixAffinity(index, policy.Spill{}), "replica-0="+base)

	// A prompt long enough to fill several blocks, sent twice: the first turn
	// teaches the index, the second is the one with something to match.
	body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":%q}],"stream":true}`,
		strings.Repeat("a long shared opening ", 40))

	var matches []string
	for range 2 {
		resp, err := http.Post(rt.url+router.ChatCompletionsPath, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		matches = append(matches, resp.Header.Get(router.PrefixMatchHeader))
	}

	if matches[0] != "0" {
		t.Errorf("the first turn reported a match of %q, want 0", matches[0])
	}
	if matches[1] == "" || matches[1] == "0" {
		t.Errorf("the second turn of the same prompt reported a match of %q", matches[1])
	}

	rows := rt.rows.wait(t, 2)
	if rows[0].PrefixMatchBytes != 0 {
		t.Errorf("row 0 prefix_match_bytes = %d, want 0", rows[0].PrefixMatchBytes)
	}
	if rows[1].PrefixMatchBytes <= 0 {
		t.Errorf("row 1 prefix_match_bytes = %d, want the match the decision was made on", rows[1].PrefixMatchBytes)
	}
	if got := strconv.Itoa(rows[1].PrefixMatchBytes); got != matches[1] {
		t.Errorf("the row says %s and the header says %s, so the two readers disagree", got, matches[1])
	}
	if rows[1].DecisionReason != string(policy.ReasonPrefixAffinity) {
		t.Errorf("row 1 decision = %q, want %q", rows[1].DecisionReason, policy.ReasonPrefixAffinity)
	}
}

// The policies that consult no index report no match, rather than a zero that
// could be read as an index that found nothing.
func TestAPolicyWithNoIndexReportsNoPrefixMatch(t *testing.T) {
	_, base := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 2})
	rt := startRouterWith(t, policy.NewSessionAffinity(), "replica-0="+base)

	resp, err := http.Post(rt.url+router.ChatCompletionsPath, "application/json", strings.NewReader(streamingRequest))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	rows := rt.rows.wait(t, 1)
	if rows[0].PrefixMatchBytes != 0 {
		t.Errorf("a policy with no index recorded a %dB match", rows[0].PrefixMatchBytes)
	}
}
