package fleet_test

import (
	"sync"
	"testing"

	"github.com/yuchia329/kvroute/internal/fleet"
)

func newFleet(t *testing.T, ids ...string) *fleet.Fleet {
	t.Helper()
	replicas := make([]fleet.Replica, 0, len(ids))
	for i, id := range ids {
		replicas = append(replicas, fleet.Replica{ID: id, BaseURL: "http://127.0.0.1:800" + string(rune('0'+i))})
	}
	f, err := fleet.New(replicas)
	if err != nil {
		t.Fatalf("new fleet: %v", err)
	}
	return f
}

// inflight reads the snapshot a policy would be given, keyed by replica id.
func inflight(t *testing.T, f *fleet.Fleet) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, c := range f.State().Replicas {
		out[c.ID] = c.Inflight
	}
	return out
}

// TestInflightIsCountedLocallyPerReplica: the router is the sole ingress, so it
// knows exactly what it dispatched. Nothing here scrapes anything.
func TestInflightIsCountedLocallyPerReplica(t *testing.T) {
	f := newFleet(t, "replica-0", "replica-1")

	if got := inflight(t, f); got["replica-0"] != 0 || got["replica-1"] != 0 {
		t.Fatalf("a fleet that has dispatched nothing reports %v, want zeros", got)
	}

	done0, err := f.Dispatch("replica-0")
	if err != nil {
		t.Fatalf("dispatch to replica-0: %v", err)
	}
	if _, err := f.Dispatch("replica-0"); err != nil {
		t.Fatalf("dispatch to replica-0: %v", err)
	}
	got := inflight(t, f)
	if got["replica-0"] != 2 {
		t.Errorf("replica-0 inflight = %d, want 2", got["replica-0"])
	}
	if got["replica-1"] != 0 {
		t.Errorf("replica-1 inflight = %d, want 0: nothing was dispatched to it", got["replica-1"])
	}

	done0()
	if got := inflight(t, f); got["replica-0"] != 1 {
		t.Errorf("replica-0 inflight = %d after one completion, want 1", got["replica-0"])
	}
}

// TestCompletingTwiceCannotDriveInflightBelowWhatIsInFlight. The completion
// function is idempotent because a double release would under-count a replica
// that really is busy, and an under-count is what makes a policy pile more work
// onto it.
func TestCompletingTwiceCannotDriveInflightBelowWhatIsInFlight(t *testing.T) {
	f := newFleet(t, "replica-0")

	done, err := f.Dispatch("replica-0")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, err := f.Dispatch("replica-0"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	done()
	done()
	done()

	if got := inflight(t, f)["replica-0"]; got != 1 {
		t.Errorf("replica-0 inflight = %d, want 1: one request is still in flight", got)
	}
}

// TestInflightDoesNotDriftOverManyConcurrentRequests is the property the load
// term rests on: a count that creeps upward makes an idle replica look busy for
// the rest of the run, and no amount of correct policy code recovers from it.
func TestInflightDoesNotDriftOverManyConcurrentRequests(t *testing.T) {
	f := newFleet(t, "replica-0", "replica-1", "replica-2")
	ids := []string{"replica-0", "replica-1", "replica-2"}

	const requests = 2000
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			done, err := f.Dispatch(ids[i%len(ids)])
			if err != nil {
				t.Errorf("dispatch: %v", err)
				return
			}
			// Every path a request can take ends in exactly one completion, so
			// this is what the router's deferred release does.
			done()
		}()
	}
	wg.Wait()

	for id, n := range inflight(t, f) {
		if n != 0 {
			t.Errorf("%s inflight = %d after %d completed requests, want 0", id, n, requests)
		}
	}
}

func TestDispatchToAnUnknownReplicaIsAnError(t *testing.T) {
	f := newFleet(t, "replica-0")

	done, err := f.Dispatch("replica-9")
	if err == nil {
		t.Fatalf("dispatch to an unknown replica returned no error")
	}
	if done != nil {
		t.Errorf("a failed dispatch returned a completion function, which the caller would defer")
	}
}

// TestStateIsASnapshot: a policy weighs one consistent view of the fleet, and a
// dispatch after it was taken does not reach back into it.
func TestStateIsASnapshot(t *testing.T) {
	f := newFleet(t, "replica-0")
	before := f.State()

	if _, err := f.Dispatch("replica-0"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if got := before.Replicas[0].Inflight; got != 0 {
		t.Errorf("the snapshot changed under the caller: inflight = %d, want 0", got)
	}
	if got := f.State().Replicas[0].Inflight; got != 1 {
		t.Errorf("a fresh snapshot reports inflight = %d, want 1", got)
	}
}
