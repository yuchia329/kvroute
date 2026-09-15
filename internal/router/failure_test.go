package router_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/fakereplica/fakereplicatest"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/router"
)

// headersThenSilence answers a chat request the way vLLM does before its engine
// has produced a token: the status line and headers are on the wire, and the
// body has not begun. It announces that moment and then holds the request until
// its connection goes.
//
// It is the shape the reroute boundary has to be drawn against. vLLM's server
// writes its headers the moment a response starts, so the router has a 200 in
// hand long before the first token — and a replica that dies in between has
// emitted nothing the client could have seen.
func headersThenSilence(arrived chan<- struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		arrived <- struct{}{}
		<-r.Context().Done()
	})
}

// counting wraps a replica handler and counts the chat requests that reached it.
type counting struct {
	http.Handler
	chats atomic.Int64
}

func (c *counting) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == router.ChatCompletionsPath {
		c.chats.Add(1)
	}
	c.Handler.ServeHTTP(w, r)
}

// sendAsync sends one request from its own goroutine, for the tests that have
// to do something to the fleet while it is in flight.
func sendAsync(url, body string) <-chan response {
	result := make(chan response, 1)
	go func() {
		got, err := send(url, body)
		got.err = err
		result <- got
	}()
	return result
}

func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	panic("unreachable")
}

// The criterion the reroute exists for. A replica killed after it accepted a
// request but before it produced a token has emitted nothing, so the request can
// go to another replica and the client can never tell: the bytes it receives are
// exactly the bytes it would have received from that replica directly.
func TestARequestWhoseReplicaDiesBeforeItsFirstTokenIsReroutedTransparently(t *testing.T) {
	arrived := make(chan struct{}, 1)
	doomed := fakereplicatest.Start(t, headersThenSilence(arrived))
	_, healthy := startFake(t, fakereplica.Config{ID: "replica-1", OutputTokens: 4})
	// Round-robin takes the fleet in order, so the first request goes to the
	// replica that is about to die.
	rt := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+doomed.URL(), "replica-1="+healthy)

	result := sendAsync(rt.url, streamingRequest)
	await(t, arrived, "the request to reach the replica that is about to die")
	doomed.Kill()
	got := await(t, result, "the rerouted request to come back")
	if got.err != nil {
		t.Fatalf("the client saw its request fail: %v", got.err)
	}

	direct := post(t, healthy, streamingRequest)
	if got.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", got.status)
	}
	if !bytes.Equal(got.body, direct.body) {
		t.Errorf("the rerouted stream differs from asking the healthy replica directly, so the reroute was not transparent\nrouted: %q\ndirect: %q", got.body, direct.body)
	}
	if got.replica != "replica-1" {
		t.Errorf("replica header = %q, want replica-1: the header names the replica that served the request", got.replica)
	}
	if got.reroutes != "1" {
		t.Errorf("reroutes header = %q, want 1", got.reroutes)
	}

	row := rt.rows.wait(t, 1)[0]
	if row.Outcome != record.OutcomeSuccess {
		t.Errorf("outcome = %q, want success: the client received a complete response", row.Outcome)
	}
	if row.Replica != "replica-1" || row.Reroutes != 1 || row.ReroutedFrom != "replica-0" {
		t.Errorf("row says replica=%s reroutes=%d rerouted_from=%q, want replica-1, one reroute, from replica-0",
			row.Replica, row.Reroutes, row.ReroutedFrom)
	}
	// Passive tracking: the request that found the replica dead is the evidence,
	// and it is acted on at once rather than waiting for a health check.
	if !replicaStats(t, rt.url, "replica-0").Ejected {
		t.Error("the replica a request found dead is still in rotation")
	}
}

// A replica that is not listening at all — dead before the request arrived, and
// not yet ejected — is the same case one step earlier, and is rerouted the same
// way.
func TestARequestToAReplicaThatIsNotListeningIsRerouted(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	_, healthy := startFake(t, fakereplica.Config{ID: "replica-1", OutputTokens: 4})
	rt := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+deadURL, "replica-1="+healthy)

	got := post(t, rt.url, streamingRequest)

	if got.status != http.StatusOK || got.replica != "replica-1" || got.reroutes != "1" {
		t.Fatalf("status=%d replica=%q reroutes=%q, want a 200 from replica-1 after one reroute", got.status, got.replica, got.reroutes)
	}
	row := rt.rows.wait(t, 1)[0]
	if row.Outcome != record.OutcomeSuccess || row.ReroutedFrom != "replica-0" {
		t.Errorf("row says outcome=%s rerouted_from=%q, want a success rerouted from replica-0", row.Outcome, row.ReroutedFrom)
	}
}

// With nowhere left to go the request is dropped, and the router says so rather
// than looping over a fleet that cannot answer.
func TestARequestEveryReplicaFailsIsDropped(t *testing.T) {
	var dead []string
	for range 2 {
		srv := httptest.NewServer(http.NotFoundHandler())
		dead = append(dead, srv.URL)
		srv.Close()
	}
	rt := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+dead[0], "replica-1="+dead[1])

	got := post(t, rt.url, streamingRequest)

	if got.status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502: every replica the router tried failed to answer", got.status)
	}
	row := rt.rows.wait(t, 1)[0]
	if row.Outcome != record.OutcomeDropped {
		t.Errorf("outcome = %q, want dropped", row.Outcome)
	}
	for _, id := range []string{"replica-0", "replica-1"} {
		if !strings.Contains(row.Error, id) {
			t.Errorf("error %q does not name %s, which was tried", row.Error, id)
		}
	}
	rt.drains(t)
}

// An error a replica answers with is not a replica that died. It is passed to the
// client as the replica sent it and counted as failed, exactly as before there
// was a reroute. Rerouting it would hide an overloaded fleet behind another replica's answer, and
// ejecting on it would take a busy replica out of rotation because it was busy —
// changing the fleet under the load a cell was measuring.
func TestAnErrorAReplicaAnswersWithIsNotRerouted(t *testing.T) {
	sick, sickURL := startFake(t, fakereplica.Config{ID: "replica-0"})
	sick.SetFailure(&fakereplica.Failure{Status: http.StatusServiceUnavailable, Message: "engine is out of KV blocks"})
	sibling := &counting{Handler: fakereplica.New(fakereplica.Config{ID: "replica-1", Now: pinnedClock()}).Handler()}
	siblingSrv := httptest.NewServer(sibling)
	t.Cleanup(siblingSrv.Close)
	rt := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+sickURL, "replica-1="+siblingSrv.URL)

	got := post(t, rt.url, blockingRequest)

	if got.status != http.StatusServiceUnavailable || !bytes.Contains(got.body, []byte("out of KV blocks")) {
		t.Errorf("status=%d body=%s, want the replica's own 503 passed through", got.status, got.body)
	}
	if n := sibling.chats.Load(); n != 0 {
		t.Errorf("the sibling received %d requests: an error the replica answered with was rerouted", n)
	}
	row := rt.rows.wait(t, 1)[0]
	if row.Outcome != record.OutcomeFailed || row.Reroutes != 0 {
		t.Errorf("row says outcome=%s reroutes=%d, want failed with no reroute", row.Outcome, row.Reroutes)
	}
	if replicaStats(t, rt.url, "replica-0").Ejected {
		t.Error("a replica that answered with an error was ejected")
	}
}

// Criterion 4. A replica lost after the first token has emitted something the
// client has already seen, so no other replica's answer can be spliced onto it:
// the request is dropped and never rerouted. And the client is told by a broken
// connection, not by a stream that ends as though it had finished — a cut-off
// answer that reads as a complete one is the worst thing this path could return.
func TestARequestWhoseReplicaDiesMidStreamIsDroppedNotRerouted(t *testing.T) {
	doomed := fakereplicatest.Start(t, fakereplica.New(fakereplica.Config{
		ID: "replica-0", TTFT: 5 * time.Millisecond, InterToken: time.Second, OutputTokens: 10, Now: pinnedClock(),
	}).Handler())
	sibling := &counting{Handler: fakereplica.New(fakereplica.Config{ID: "replica-1", Now: pinnedClock()}).Handler()}
	siblingSrv := httptest.NewServer(sibling)
	t.Cleanup(siblingSrv.Close)
	rt := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+doomed.URL(), "replica-1="+siblingSrv.URL)

	resp, err := http.Post(rt.url+router.ChatCompletionsPath, "application/json", strings.NewReader(streamingRequest))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	stream := bufio.NewReader(resp.Body)
	// The first chunk, so the stream has begun and the client has seen it.
	if _, err := stream.ReadString('\n'); err != nil {
		t.Fatalf("read the first chunk: %v", err)
	}
	doomed.Kill()

	if _, err := io.ReadAll(stream); err == nil {
		t.Error("the stream ended cleanly after its replica died, so the client cannot tell a cut-off answer from a finished one")
	}
	row := rt.rows.wait(t, 1)[0]
	if row.Outcome != record.OutcomeDropped {
		t.Errorf("outcome = %q, want %q: the client never received a complete response", row.Outcome, record.OutcomeDropped)
	}
	if row.Replica != "replica-0" || row.Reroutes != 0 {
		t.Errorf("row says replica=%s reroutes=%d, want replica-0 and no reroute", row.Replica, row.Reroutes)
	}
	if n := sibling.chats.Load(); n != 0 {
		t.Errorf("the sibling received %d requests: a request was rerouted after its stream had begun", n)
	}
	if !replicaStats(t, rt.url, "replica-0").Ejected {
		t.Error("the replica that broke a stream mid-response is still in rotation")
	}
	rt.drains(t)
}

// Criterion 6: a replica that comes back is readmitted by the router on its own
// — no restart of the router, no operator — and is given work again. The health
// checks are what notice, so they run here as cmd/router runs them.
func TestAReplicaThatComesBackIsReadmittedWithoutARestart(t *testing.T) {
	phoenix := fakereplicatest.Start(t, fakereplica.New(fakereplica.Config{ID: "replica-0", OutputTokens: 2, Now: pinnedClock()}).Handler())
	_, steady := startFake(t, fakereplica.Config{ID: "replica-1", OutputTokens: 2})
	rt := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+phoenix.URL(), "replica-1="+steady)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go fleet.CheckHealth(ctx, fleet.HealthConfig{Fleet: rt.fleet, Interval: 10 * time.Millisecond, Timeout: time.Second})

	phoenix.Kill()
	eventually(t, "the dead replica to be ejected", func() bool { return replicaStats(t, rt.url, "replica-0").Ejected })
	for range 3 {
		if got := post(t, rt.url, streamingRequest); got.replica != "replica-1" {
			t.Errorf("a request went to %q while replica-0 was dead, want replica-1", got.replica)
		}
	}

	if err := phoenix.Revive(); err != nil {
		t.Fatalf("revive: %v", err)
	}
	eventually(t, "the revived replica to be readmitted", func() bool { return !replicaStats(t, rt.url, "replica-0").Ejected })
	served := map[string]bool{}
	for range 2 {
		served[post(t, rt.url, streamingRequest).replica] = true
	}
	if !served["replica-0"] {
		t.Error("the readmitted replica was not given work again")
	}
	if got := replicaStats(t, rt.url, "replica-0").Ejections; got != 1 {
		t.Errorf("ejections = %d after one death, want 1", got)
	}
}

// replicaStats is one replica's entry in the router's published stats.
func replicaStats(t *testing.T, url, id string) router.ReplicaStats {
	t.Helper()
	for _, r := range statsOf(t, url).Replicas {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("the router's stats do not list %s", id)
	return router.ReplicaStats{}
}
