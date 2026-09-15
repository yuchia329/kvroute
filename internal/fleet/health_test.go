package fleet_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
)

// healthEndpoint is a replica's /health that the test turns off and on, and that
// counts how often it was asked.
type healthEndpoint struct {
	down atomic.Bool
	// failNext fails that many upcoming checks even while the endpoint is up,
	// which is a replica that hiccuped rather than one that died.
	failNext atomic.Int64
	checks   atomic.Int64
}

func startHealth(t *testing.T) (*healthEndpoint, string) {
	t.Helper()
	h := &healthEndpoint{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h.checks.Add(1)
		if h.down.Load() || h.failNext.Add(-1) >= 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return h, srv.URL
}

// checking runs the health checks over f until the test ends.
//
// The timeout is stated rather than derived from the short interval: a check to
// a loopback server answers at once and a refused connection fails at once, so
// the only thing a millisecond timeout would add is a check failed by the race
// detector's pauses, which is exactly the flake the single-failure test would
// then trip on.
func checking(t *testing.T, f *fleet.Fleet) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go fleet.CheckHealth(ctx, fleet.HealthConfig{Fleet: f, Interval: 5 * time.Millisecond, Timeout: time.Second})
}

// The active half of health tracking, both directions: a replica that stops
// answering is taken out without anyone having to send it a request, and one
// that starts answering again is put back without anyone restarting the router.
func TestFailedHealthChecksEjectAReplicaAndPassingOnesReadmitIt(t *testing.T) {
	endpoint, url := startHealth(t)
	_, sibling := startHealth(t)
	f, err := fleet.New([]fleet.Replica{{ID: "replica-0", BaseURL: url}, {ID: "replica-1", BaseURL: sibling}})
	if err != nil {
		t.Fatalf("new fleet: %v", err)
	}
	checking(t, f)

	endpoint.down.Store(true)
	waitFor(t, func() bool { return member(t, f, "replica-0").Ejected })
	if member(t, f, "replica-1").Ejected {
		t.Error("one replica failing its checks ejected its sibling too")
	}

	endpoint.down.Store(false)
	waitFor(t, func() bool { return !member(t, f, "replica-0").Ejected })
	if got := member(t, f, "replica-0").Ejections; got != 1 {
		t.Errorf("ejections = %d after one outage, want 1", got)
	}
}

// A replica that is not listening at all is what a killed process looks like
// from outside, and it is the case the health checks exist for.
func TestAReplicaThatIsNotListeningIsEjected(t *testing.T) {
	_, sibling := startHealth(t)
	f, err := fleet.New([]fleet.Replica{{ID: "replica-0", BaseURL: "http://127.0.0.1:1"}, {ID: "replica-1", BaseURL: sibling}})
	if err != nil {
		t.Fatalf("new fleet: %v", err)
	}
	checking(t, f)

	waitFor(t, func() bool { return member(t, f, "replica-0").Ejected })
}

// One failed check is not a dead replica. The checks run beside the measurement
// on the engines being measured, and an engine busy enough to miss one check is
// an engine under load rather than one that has gone — ejecting it would change
// the fleet a cell is measuring because the cell was loading it.
func TestASingleFailedCheckDoesNotEject(t *testing.T) {
	endpoint, url := startHealth(t)
	f, err := fleet.New([]fleet.Replica{{ID: "replica-0", BaseURL: url}})
	if err != nil {
		t.Fatalf("new fleet: %v", err)
	}
	endpoint.failNext.Store(1)
	checking(t, f)

	// Enough checks that the failed one is followed by several passes.
	waitFor(t, func() bool { return endpoint.checks.Load() >= 6 })
	if got := member(t, f, "replica-0").Ejections; got != 0 {
		t.Errorf("one failed check among passes ejected the replica %d times", got)
	}
}

// A replica ejected by a real request failing — the passive half — comes back
// through the same checks as one ejected by the checks themselves. Otherwise a
// replica that dropped one connection would be out for good.
func TestAPassivelyEjectedReplicaIsReadmittedWhenItsChecksPass(t *testing.T) {
	_, url := startHealth(t)
	f, err := fleet.New([]fleet.Replica{{ID: "replica-0", BaseURL: url}})
	if err != nil {
		t.Fatalf("new fleet: %v", err)
	}
	if _, err := f.Eject("replica-0"); err != nil {
		t.Fatalf("eject: %v", err)
	}
	checking(t, f)

	waitFor(t, func() bool { return !member(t, f, "replica-0").Ejected })
}
