package policy_test

import (
	"fmt"
	"testing"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/session"
)

// forSession is one request of a conversation, as the policy sees it after the
// router has resolved its identity.
func forSession(id string) policy.Request {
	return policy.Request{Session: session.Session{ID: id}}
}

// homes maps each session to the replica the policy sends it to, which is the
// unit every claim about consistent hashing below is made in.
func homes(t *testing.T, p policy.Policy, state fleet.State, sessions []string) map[string]string {
	t.Helper()
	home := make(map[string]string, len(sessions))
	for _, id := range sessions {
		choice, err := p.Choose(forSession(id), state)
		if err != nil {
			t.Fatalf("choose for session %s: %v", id, err)
		}
		home[id] = choice.Replica.ID
	}
	return home
}

func sessionIDs(n int) []string {
	ids := make([]string, 0, n)
	for i := range n {
		ids = append(ids, fmt.Sprintf("chat-%d", i))
	}
	return ids
}

// stateOf builds the fleet state a policy sees from a set of replica ids, which
// is how a replica leaving the fleet is expressed to a policy: the candidate is
// simply no longer in the snapshot.
func stateOf(ids ...string) fleet.State {
	candidates := make([]fleet.Candidate, 0, len(ids))
	for i, id := range ids {
		candidates = append(candidates, fleet.Candidate{
			Replica: fleet.Replica{ID: id, BaseURL: fmt.Sprintf("http://127.0.0.1:%d", 8000+i)},
		})
	}
	return fleet.State{Replicas: candidates}
}

func sixReplicas() fleet.State {
	return stateOf("replica-0", "replica-1", "replica-2", "replica-3", "replica-4", "replica-5")
}

// The policy exists to keep a conversation on one replica, so that turn N finds
// turns 1..N-1 already in that replica's KV cache.
func TestEveryTurnOfASessionGoesToTheSameReplica(t *testing.T) {
	p := policy.NewSessionAffinity()
	state := sixReplicas()

	first, err := p.Choose(forSession("chat-7"), state)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	if first.Reason != policy.ReasonSessionAffinity {
		t.Errorf("reason = %q, want %q", first.Reason, policy.ReasonSessionAffinity)
	}
	for turn := range 20 {
		later, err := p.Choose(forSession("chat-7"), state)
		if err != nil {
			t.Fatalf("choose: %v", err)
		}
		if later.Replica.ID != first.Replica.ID {
			t.Fatalf("turn %d went to %s, but the session's first turn went to %s", turn, later.Replica.ID, first.Replica.ID)
		}
	}
}

// Pinning every session to one replica would also satisfy the test above. The
// hash has to spread the sessions, or the comparison is between a policy and one
// GPU.
func TestSessionsSpreadAcrossTheWholeFleet(t *testing.T) {
	p := policy.NewSessionAffinity()
	state := sixReplicas()

	const sessions = 600
	counts := map[string]int{}
	for _, replica := range homes(t, p, state, sessionIDs(sessions)) {
		counts[replica]++
	}

	if len(counts) != len(state.Replicas) {
		t.Fatalf("%d sessions landed on %d of %d replicas: %v", sessions, len(counts), len(state.Replicas), counts)
	}
	// A hash ring is not a balancer and an exactly even split is not what it
	// promises, so the bound is loose. What it rules out is a ring so lumpy that
	// the load imbalance §5 predicts would be the ring's rather than the skew's.
	even := sessions / len(state.Replicas)
	for replica, n := range counts {
		if n < even/2 || n > even*2 {
			t.Errorf("%s holds %d of %d sessions against an even share of %d: %v", replica, n, sessions, even, counts)
		}
	}
}

// The criterion consistent hashing exists for. Modulo hashing would move roughly
// five sessions in six when one replica of six leaves; a ring moves only the
// sessions that lived on the replica that left.
//
// This is the mechanism behind idea.md §5's prediction that a hash ring wrecks
// its own cache on topology change while a prefix index degrades gracefully —
// the fraction that rehashes here is exactly what that claim is about, so it is
// measured rather than assumed.
func TestARemovedReplicaMovesOnlyItsOwnSessions(t *testing.T) {
	p := policy.NewSessionAffinity()
	sessions := sessionIDs(600)

	before := homes(t, p, sixReplicas(), sessions)
	after := homes(t, p, stateOf("replica-0", "replica-1", "replica-2", "replica-3", "replica-4"), sessions)

	moved, strandedElsewhere := 0, 0
	for _, id := range sessions {
		if before[id] == after[id] {
			continue
		}
		moved++
		if before[id] != "replica-5" {
			strandedElsewhere++
			t.Errorf("session %s moved from %s to %s, but neither replica left the fleet", id, before[id], after[id])
		}
	}

	if strandedElsewhere > 0 {
		t.Fatalf("%d of %d sessions were rehashed off replicas that were still there", strandedElsewhere, len(sessions))
	}
	if moved == 0 {
		t.Fatal("no session moved when a replica left, so the sessions homed on it were never routed there")
	}
	// Everything on the departed replica has to move, and nothing else may. That
	// is a sixth of the sessions, and the test asserts the shape rather than the
	// exact count so a differently-seeded ring does not fail it.
	if share := float64(moved) / float64(len(sessions)); share > 0.34 {
		t.Errorf("%.0f%% of sessions rehashed when one replica of six left, which is not a minimal redistribution", share*100)
	}
}

// The other direction of the same property: a replica joining takes sessions
// from the fleet, and does not shuffle the sessions it did not take.
func TestAnAddedReplicaTakesSessionsWithoutDisturbingTheRest(t *testing.T) {
	p := policy.NewSessionAffinity()
	sessions := sessionIDs(600)

	before := homes(t, p, sixReplicas(), sessions)
	after := homes(t, p, stateOf("replica-0", "replica-1", "replica-2", "replica-3", "replica-4", "replica-5", "replica-6"), sessions)

	took := 0
	for _, id := range sessions {
		switch {
		case before[id] == after[id]:
		case after[id] == "replica-6":
			took++
		default:
			t.Errorf("session %s moved from %s to %s, though the only change was replica-6 joining", id, before[id], after[id])
		}
	}
	if took == 0 {
		t.Error("a seventh replica joined the fleet and took no sessions")
	}
}

// The ring is derived from the replica set, so the fleet it is built from has to
// be the only thing that decides it: two routers started against one fleet must
// send a session to the same replica, or the policy is a per-process accident.
func TestTwoRoutersAgreeOnWhereASessionLives(t *testing.T) {
	state := sixReplicas()
	one, other := policy.NewSessionAffinity(), policy.NewSessionAffinity()

	for _, id := range sessionIDs(200) {
		a, b := homes(t, one, state, []string{id})[id], homes(t, other, state, []string{id})[id]
		if a != b {
			t.Fatalf("session %s lives on %s in one router and %s in another", id, a, b)
		}
	}
}

// A request the router could not identify a session for has nothing to hash. It
// is spread rather than pinned, and it says so: routing every unidentified
// request to whichever replica the empty string lands on would look exactly like
// a hot session, and the record has to be able to tell those apart.
func TestUnidentifiedRequestsAreSpreadAndSayWhyRatherThanPinned(t *testing.T) {
	p := policy.NewSessionAffinity()
	state := sixReplicas()

	seen := map[string]int{}
	for range len(state.Replicas) {
		choice, err := p.Choose(policy.Request{}, state)
		if err != nil {
			t.Fatalf("choose: %v", err)
		}
		if choice.Reason != policy.ReasonSessionUnidentified {
			t.Errorf("reason = %q, want %q: an unidentified request was not routed by session affinity", choice.Reason, policy.ReasonSessionUnidentified)
		}
		seen[choice.Replica.ID]++
	}
	if len(seen) != len(state.Replicas) {
		t.Errorf("%d unidentified requests landed on %d of %d replicas (%v), want all six", len(state.Replicas), len(seen), len(state.Replicas), seen)
	}
}

// A derived identity is an identity: the fallback path is the derived-key policy
// idea.md §4.2 wants measured, so it must route by session affinity like any
// other.
func TestADerivedIdentityRoutesBySessionAffinityLikeASuppliedOne(t *testing.T) {
	p := policy.NewSessionAffinity()
	state := sixReplicas()

	supplied, err := p.Choose(policy.Request{Session: session.Session{ID: "chat-3"}}, state)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	derived, err := p.Choose(policy.Request{Session: session.Session{ID: "chat-3", Derived: true}}, state)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	if derived.Reason != policy.ReasonSessionAffinity {
		t.Errorf("a derived identity was routed as %q, not by session affinity", derived.Reason)
	}
	if derived.Replica.ID != supplied.Replica.ID {
		t.Errorf("one session id routed to %s when derived and %s when supplied", derived.Replica.ID, supplied.Replica.ID)
	}
}

// The policy ignores load by design — that blindness is the whole reason §5 says
// prefix affinity should be able to beat it under skew — but the row still has
// to record the load it ignored, or the table cannot show the imbalance the
// argument rests on.
func TestTheChoiceRecordsTheLoadTheHashIgnored(t *testing.T) {
	f := newFleet(t, 3)
	load(t, f, "replica-0", 5)
	load(t, f, "replica-1", 5)
	load(t, f, "replica-2", 5)

	choice, err := policy.NewSessionAffinity().Choose(forSession("chat-1"), f.State())
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	if choice.Inflight != 5 {
		t.Errorf("decision recorded inflight = %d, want 5", choice.Inflight)
	}
}

func TestSessionAffinityWithNoReplicasCannotChoose(t *testing.T) {
	if _, err := policy.NewSessionAffinity().Choose(forSession("chat-1"), fleet.State{}); err != policy.ErrNoReplica {
		t.Errorf("err = %v, want %v", err, policy.ErrNoReplica)
	}
}
