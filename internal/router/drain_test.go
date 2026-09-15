package router_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/router"
)

// admin asks the router to drain or restore a replica, and returns the status
// it answered and the replica as it stands afterwards.
func admin(t *testing.T, base, id, action string) (int, router.ReplicaStats) {
	t.Helper()
	resp, err := http.Post(base+"/router/replicas/"+id+"/"+action, "", nil)
	if err != nil {
		t.Fatalf("%s %s: %v", action, id, err)
	}
	defer resp.Body.Close()
	var got router.ReplicaStats
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("%s %s: decode: %v", action, id, err)
		}
	}
	return resp.StatusCode, got
}

func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Criterion 2 at the router: a replica drained through the router's own surface
// finishes every request it is serving, is given no new one, and comes back
// only when the operator restores it. Zero drops is the drain's whole promise,
// so every request of the test is checked, not only the ones that were moved.
func TestADrainedReplicaFinishesWhatItHasAndTakesNothingNew(t *testing.T) {
	slow := func(id string) *counting {
		return &counting{Handler: fakereplica.New(fakereplica.Config{
			ID: id, TTFT: 5 * time.Millisecond, InterToken: 30 * time.Millisecond, OutputTokens: 6, Now: pinnedClock(),
		}).Handler()}
	}
	drained, sibling := slow("replica-0"), slow("replica-1")
	drainedSrv, siblingSrv := httptest.NewServer(drained), httptest.NewServer(sibling)
	t.Cleanup(drainedSrv.Close)
	t.Cleanup(siblingSrv.Close)
	rt := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+drainedSrv.URL, "replica-1="+siblingSrv.URL)

	// One request in flight on each replica when the drain begins.
	inFlight := []<-chan response{sendAsync(rt.url, streamingRequest), sendAsync(rt.url, streamingRequest)}
	eventually(t, "a request in flight on replica-0", func() bool { return replicaStats(t, rt.url, "replica-0").Inflight == 1 })

	status, got := admin(t, rt.url, "replica-0", "drain")
	if status != http.StatusOK || !got.Draining {
		t.Fatalf("drain answered %d with draining=%v", status, got.Draining)
	}

	// Everything after the drain goes to the sibling.
	before := drained.chats.Load()
	for range 4 {
		if got := post(t, rt.url, streamingRequest); got.replica != "replica-1" {
			t.Errorf("a request after the drain went to %q, want replica-1", got.replica)
		}
	}
	if n := drained.chats.Load() - before; n != 0 {
		t.Errorf("the draining replica was given %d new requests", n)
	}

	// What it was already serving finishes, whole.
	for _, ch := range inFlight {
		got := await(t, ch, "a request that was in flight when the drain began")
		if got.err != nil || got.status != http.StatusOK || !bytes.HasSuffix(got.body, []byte("data: [DONE]\n\n")) {
			t.Errorf("an in-flight request did not finish whole: status=%d err=%v", got.status, got.err)
		}
	}
	for i, row := range rt.rows.wait(t, 6) {
		if row.Outcome != record.OutcomeSuccess {
			t.Errorf("row %d on %s is %s: a drain must drop nothing", i, row.Replica, row.Outcome)
		}
	}
	eventually(t, "the drained replica to hold nothing", func() bool { return replicaStats(t, rt.url, "replica-0").Inflight == 0 })

	status, got = admin(t, rt.url, "replica-0", "restore")
	if status != http.StatusOK || got.Draining {
		t.Fatalf("restore answered %d with draining=%v", status, got.Draining)
	}
	served := map[string]bool{}
	for range 2 {
		served[post(t, rt.url, streamingRequest).replica] = true
	}
	if !served["replica-0"] {
		t.Error("the restored replica was not given work again")
	}
}

// An operator naming a replica the router does not front has mistyped it, and a
// success would leave them waiting on a drain that is not happening.
func TestDrainingAReplicaTheRouterDoesNotFrontIsNotFound(t *testing.T) {
	_, url := startFake(t, fakereplica.Config{ID: "replica-0"})
	rt := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+url)

	for _, action := range []string{"drain", "restore"} {
		if status, _ := admin(t, rt.url, "replica-9", action); status != http.StatusNotFound {
			t.Errorf("%s of a replica the router does not front answered %d, want 404", action, status)
		}
	}
}
