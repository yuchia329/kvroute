package policy_test

import (
	"fmt"
	"testing"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
)

// newFleet builds a fleet of n replicas, and dispatches to it to set up load.
func newFleet(t *testing.T, n int) *fleet.Fleet {
	t.Helper()
	replicas := make([]fleet.Replica, 0, n)
	for i := range n {
		replicas = append(replicas, fleet.Replica{
			ID:      fmt.Sprintf("replica-%d", i),
			BaseURL: fmt.Sprintf("http://127.0.0.1:%d", 8000+i),
		})
	}
	f, err := fleet.New(replicas)
	if err != nil {
		t.Fatalf("new fleet: %v", err)
	}
	return f
}

// load dispatches requests to a replica and leaves them in flight, so the fleet
// carries the load a policy is then asked to weigh.
func load(t *testing.T, f *fleet.Fleet, id string, requests int) {
	t.Helper()
	for range requests {
		if _, err := f.Dispatch(id); err != nil {
			t.Fatalf("dispatch to %s: %v", id, err)
		}
	}
}

func choose(t *testing.T, p policy.Policy, state fleet.State) policy.Choice {
	t.Helper()
	choice, err := p.Choose(policy.Request{}, state)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	return choice
}

func TestLeastOutstandingPicksTheReplicaWithTheFewestInflight(t *testing.T) {
	f := newFleet(t, 3)
	load(t, f, "replica-0", 4)
	load(t, f, "replica-1", 1)
	load(t, f, "replica-2", 7)

	p := policy.NewLeastOutstanding()
	choice := choose(t, p, f.State())

	if choice.Replica.ID != "replica-1" {
		t.Errorf("chose %s, want replica-1: it holds 1 request against 4 and 7", choice.Replica.ID)
	}
	if choice.Reason != policy.ReasonLeastOutstanding {
		t.Errorf("reason = %q, want %q", choice.Reason, policy.ReasonLeastOutstanding)
	}
	if choice.Inflight != 1 {
		t.Errorf("decision recorded inflight = %d, want 1: the row has to say what the policy weighed", choice.Inflight)
	}
}

// TestLeastOutstandingSpreadsTiesRatherThanFillingOneReplica. Every replica of an
// idle fleet is tied at zero, which is the state the whole low-concurrency end of
// the sweep runs in. A tie-break that always took the first replica would send
// every request of an idle fleet to one card, and the concurrency-1 cell would
// measure one GPU rather than the fleet.
func TestLeastOutstandingSpreadsTiesRatherThanFillingOneReplica(t *testing.T) {
	f := newFleet(t, 6)
	p := policy.NewLeastOutstanding()

	seen := map[string]int{}
	for range 6 {
		// Nothing is dispatched, so every candidate stays tied at zero and only
		// the tie-break decides.
		seen[choose(t, p, f.State()).Replica.ID]++
	}

	if len(seen) != 6 {
		t.Errorf("six requests against an idle fleet of six landed on %d replicas (%v), want all six", len(seen), seen)
	}
}

func TestLeastOutstandingWithNoReplicasCannotChoose(t *testing.T) {
	p := policy.NewLeastOutstanding()

	_, err := p.Choose(policy.Request{}, fleet.State{})
	if err == nil {
		t.Fatalf("choosing from an empty fleet returned no error")
	}
	if err != policy.ErrNoReplica {
		t.Errorf("err = %v, want %v", err, policy.ErrNoReplica)
	}
}

// TestExactCountsPreventTheHerdAStaleViewWouldCause is the herding argument
// (idea.md §4.5) as an executable comparison, at the level where the decision is
// made.
//
// A scraped inflight is up to one polling window old, so every request arriving
// inside that window weighs the same figures and picks the same replica. The
// stale run below is that: one snapshot, reused for the whole window. The exact
// run is what the router does instead, and the two are the same policy over the
// same arrivals, so the only difference between them is where the count came
// from.
//
// The router-level test that the wiring actually produces the exact case is
// TestArrivalsInsideOnePollingWindowDoNotStampedeOneReplica.
func TestExactCountsPreventTheHerdAStaleViewWouldCause(t *testing.T) {
	const (
		replicas = 6
		// Twelve arrivals in one window: at the ~1 s scrape interval §4.5 warns
		// about, a fleet serving even a modest rate sees far more than this.
		arrivals = 12
		// Five replicas are busy and one is idle. That imbalance is what makes a
		// stale figure dangerous: with every replica equally loaded a stale view
		// and an exact one agree.
		busy = 3
	)

	windowStart := func() (*fleet.Fleet, fleet.State) {
		f := newFleet(t, replicas)
		for i := range replicas - 1 {
			load(t, f, fmt.Sprintf("replica-%d", i), busy)
		}
		return f, f.State()
	}

	// Stale: the count comes from a scrape, so every arrival in the window is
	// weighed against the fleet as it was when the window opened.
	stale := map[string]int{}
	_, scraped := windowStart()
	staleP := policy.NewLeastOutstanding()
	for range arrivals {
		stale[choose(t, staleP, scraped).Replica.ID]++
	}

	// Exact: the count is the router's own, so an arrival sees the ones before it.
	exact := map[string]int{}
	f, _ := windowStart()
	exactP := policy.NewLeastOutstanding()
	for range arrivals {
		chosen := choose(t, exactP, f.State()).Replica.ID
		exact[chosen]++
		if _, err := f.Dispatch(chosen); err != nil {
			t.Fatalf("dispatch to %s: %v", chosen, err)
		}
	}

	if len(stale) != 1 || stale["replica-5"] != arrivals {
		t.Fatalf("the stale view spread its arrivals %v: the test is not demonstrating the herd it exists to compare against", stale)
	}
	if len(exact) != replicas {
		t.Errorf("exact counts put %d arrivals on %d of %d replicas (%v), want every replica used", arrivals, len(exact), replicas, exact)
	}
	worst := 0
	for _, n := range exact {
		worst = max(worst, n)
	}
	// The idle replica legitimately takes the first few, up to the point where it
	// is no longer the least loaded. What it must not do is take the window.
	if worst > busy+2 {
		t.Errorf("exact counts still piled %d of %d arrivals onto one replica (%v)", worst, arrivals, exact)
	}
}

func TestByNameResolvesEveryPolicyTheComparisonReports(t *testing.T) {
	_, residencyIndex := exactFleet(t, 2)
	options := policy.Options{PrefixIndex: prefixIndex(t), ResidencyIndex: residencyIndex, Tokenizer: tokenizer()}
	for _, name := range policy.Order {
		p, err := policy.ByName(name, options)
		if err != nil {
			t.Errorf("policy %q is compared but cannot be run: %v", name, err)
			continue
		}
		if p.Name() != name {
			t.Errorf("policy %q reports its name as %q, so its cells would be labelled wrong", name, p.Name())
		}
	}
}
