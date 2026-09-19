package policy_test

import (
	"math/rand/v2"
	"testing"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
)

// cacheRouteBound is the one bound this repo measures at: CacheRoute's setting
// for the same policy (#36). The tests run at it so that what they pin down is
// the policy the grid runs rather than a neighbour of it.
const cacheRouteBound = policy.InflightBound(0.25)

// carrying is the six-replica fleet with some of its replicas holding requests,
// which is how load reaches a policy: as the inflight on the snapshot's
// candidates.
func carrying(inflight map[string]int) fleet.State {
	state := sixReplicas()
	for i, c := range state.Replicas {
		state.Replicas[i].Inflight = inflight[c.ID]
	}
	return state
}

// without is the fleet with one replica gone, which is the only way the policy
// seam can be asked what the ring's next choice for a session is: a ring that
// loses a replica hands its sessions to the next replica clockwise.
func without(state fleet.State, id string) fleet.State {
	rest := fleet.State{}
	for _, c := range state.Replicas {
		if c.ID != id {
			rest.Replicas = append(rest.Replicas, c)
		}
	}
	return rest
}

// ringHome is where the load-blind policy puts a session, which is the ring's
// first choice by construction: the two policies share the ring, and that is the
// claim the tests below lean on rather than reaching into it.
func ringHome(t *testing.T, state fleet.State, id string) string {
	t.Helper()
	return homes(t, policy.NewSessionAffinity(), state, []string{id})[id]
}

// The bounded policy is session affinity until the bound has something to say.
// A difference between the two on a fleet where nothing is over the bound would
// be a second hashing scheme, and every margin between them would be the ring's
// rather than the bound's.
func TestASessionStaysWhereTheRingPutsItWhileThatReplicaIsWithinTheBound(t *testing.T) {
	p := policy.NewBoundedSessionAffinity(cacheRouteBound)
	state := sixReplicas()

	for _, id := range sessionIDs(200) {
		choice, err := p.Choose(forSession(id), state)
		if err != nil {
			t.Fatalf("choose: %v", err)
		}
		if want := ringHome(t, state, id); choice.Replica.ID != want {
			t.Fatalf("session %s went to %s on an idle fleet, but the ring places it on %s", id, choice.Replica.ID, want)
		}
		if choice.Reason != policy.ReasonBoundedSessionAffinity {
			t.Fatalf("reason = %q, want %q: nothing was over the bound, so nothing was moved", choice.Reason, policy.ReasonBoundedSessionAffinity)
		}
	}
}

// The bound is relative to the fleet's mean, not an absolute count. A fleet
// that is busy everywhere has no replica to prefer, and a session moved off its
// cache there would pay a prefill for nothing.
func TestAnEvenlyBusyFleetMovesNothing(t *testing.T) {
	p := policy.NewBoundedSessionAffinity(cacheRouteBound)
	state := carrying(map[string]int{
		"replica-0": 40, "replica-1": 40, "replica-2": 40,
		"replica-3": 40, "replica-4": 40, "replica-5": 40,
	})

	for _, id := range sessionIDs(100) {
		choice, err := p.Choose(forSession(id), state)
		if err != nil {
			t.Fatalf("choose: %v", err)
		}
		if want := ringHome(t, state, id); choice.Replica.ID != want || choice.Reason != policy.ReasonBoundedSessionAffinity {
			t.Fatalf("session %s went to %s as %q on an evenly loaded fleet, want %s as %q",
				id, choice.Replica.ID, choice.Reason, want, policy.ReasonBoundedSessionAffinity)
		}
	}
}

// The mechanism the policy exists for: a session whose replica is over the bound
// goes to the next replica clockwise, and says the bound moved it.
func TestASessionMovesToTheNextReplicaClockwiseOnceItsOwnIsOverTheBound(t *testing.T) {
	p := policy.NewBoundedSessionAffinity(cacheRouteBound)
	idle := sixReplicas()
	home := ringHome(t, idle, "chat-7")

	// Twelve requests on one replica of six: the mean with this request counted
	// is 13/6, the bound ⌈1.25 × 13/6⌉ = 3, and the home holds four times that.
	state := carrying(map[string]int{home: 12})
	next := ringHome(t, without(idle, home), "chat-7")

	choice, err := p.Choose(forSession("chat-7"), state)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	if choice.Replica.ID != next {
		t.Errorf("the session went to %s, want %s: the next replica clockwise of %s, which is where the ring sends it when %s is gone", choice.Replica.ID, next, home, home)
	}
	if choice.Reason != policy.ReasonBoundDeflected {
		t.Errorf("reason = %q, want %q: the bound moved this turn off the replica the ring placed it on", choice.Reason, policy.ReasonBoundDeflected)
	}
	if choice.Inflight != 0 {
		t.Errorf("decision recorded inflight = %d, want the 0 the chosen replica held, not the %d on the replica turned down", choice.Inflight, 12)
	}
}

// The walk continues past every replica over the bound, in the ring's order and
// not in load's. Taking the least loaded replica instead would be
// least-outstanding with a ring-placed first choice, which is a different baseline
// and not the one Envoy and HAProxy ship.
func TestTheWalkPassesEveryReplicaOverTheBoundAndStopsAtTheFirstWithinIt(t *testing.T) {
	p := policy.NewBoundedSessionAffinity(cacheRouteBound)
	idle := sixReplicas()
	first := ringHome(t, idle, "chat-7")
	second := ringHome(t, without(idle, first), "chat-7")
	third := ringHome(t, without(without(idle, first), second), "chat-7")

	// The third replica carries something and the rest carry nothing, so a
	// policy that fell through to load would go anywhere but there.
	state := carrying(map[string]int{first: 20, second: 20, third: 1})

	choice, err := p.Choose(forSession("chat-7"), state)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	if choice.Replica.ID != third {
		t.Errorf("the session went to %s, want %s: the first replica in the ring's order that is within the bound", choice.Replica.ID, third)
	}
	if choice.Reason != policy.ReasonBoundDeflected {
		t.Errorf("reason = %q, want %q", choice.Reason, policy.ReasonBoundDeflected)
	}
	if choice.Inflight != 1 {
		t.Errorf("decision recorded inflight = %d, want the 1 the chosen replica held", choice.Inflight)
	}
}

// A replica exactly at the bound is full: the bound is a capacity that counts
// this request, as the consistent-hashing-with-bounded-loads rule states it, and
// not a load the replica may exceed by one.
func TestTheBoundCountsTheRequestBeingPlaced(t *testing.T) {
	p := policy.NewBoundedSessionAffinity(cacheRouteBound)
	idle := sixReplicas()
	home := ringHome(t, idle, "chat-7")

	// 29 in flight and this request is 30: a mean of 5, a bound of ⌈6.25⌉ = 7.
	// A home holding 6 has room for a seventh; one holding 7 does not.
	others := map[string]int{}
	for _, c := range idle.Replicas {
		if c.ID != home {
			others[c.ID] = 0
		}
	}
	fill := func(homeInflight int) fleet.State {
		inflight := map[string]int{home: homeInflight}
		rest := 29 - homeInflight
		for id := range others {
			share := min(rest, 5)
			inflight[id] = share
			rest -= share
		}
		if rest != 0 {
			t.Fatalf("the fixture holds %d requests it could not place", rest)
		}
		return carrying(inflight)
	}

	room, err := p.Choose(forSession("chat-7"), fill(6))
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	if room.Replica.ID != home || room.Reason != policy.ReasonBoundedSessionAffinity {
		t.Errorf("a home holding 6 under a bound of 7 sent the session to %s as %q, want it kept on %s", room.Replica.ID, room.Reason, home)
	}

	full, err := p.Choose(forSession("chat-7"), fill(7))
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	if full.Replica.ID == home || full.Reason != policy.ReasonBoundDeflected {
		t.Errorf("a home holding 7 under a bound of 7 kept the session (%s, %q), want it moved", full.Replica.ID, full.Reason)
	}
}

// No request is dropped for want of a replica under the bound, whatever the
// fleet looks like, and none is sent outside the fleet.
//
// Stated over arbitrary fleets rather than over one built to have every replica
// over the bound, because no such fleet exists: the bound is at least the mean
// with this request counted, and every replica cannot be above the mean. The
// fallback to the ring's first choice is kept in the policy regardless — a
// defined answer costs nothing, and the alternative is a dropped request on a
// fleet that is fine — and this is the behaviour it guarantees at the seam.
func TestEveryRequestIsPlacedHoweverTheFleetIsLoaded(t *testing.T) {
	p := policy.NewBoundedSessionAffinity(cacheRouteBound)
	rng := rand.New(rand.NewPCG(46, 36))

	for trial := range 2000 {
		inflight := map[string]int{}
		for _, c := range sixReplicas().Replicas {
			inflight[c.ID] = rng.IntN(200)
		}
		state := carrying(inflight)
		id := sessionIDs(trial + 1)[trial]

		choice, err := p.Choose(forSession(id), state)
		if err != nil {
			t.Fatalf("session %s was dropped on a fleet carrying %v: %v", id, inflight, err)
		}
		chosen, present := state.Candidate(choice.Replica.ID)
		if !present {
			t.Fatalf("session %s was sent to %s, which is not in the fleet", id, choice.Replica.ID)
		}
		home := ringHome(t, state, id)
		switch choice.Reason {
		case policy.ReasonBoundedSessionAffinity:
			if chosen.ID != home {
				t.Fatalf("session %s was reported kept where the ring put it, but went to %s and the ring places it on %s", id, chosen.ID, home)
			}
		case policy.ReasonBoundDeflected:
			if chosen.ID == home {
				t.Fatalf("session %s was reported moved by the bound, but went to the ring's first choice %s", id, home)
			}
		default:
			t.Fatalf("session %s was routed as %q, which is not a decision this policy makes for an identified session", id, choice.Reason)
		}
	}
}

// What the bound is for, measured at the seam: replayed over a skewed arrival
// stream with every request left in flight, no replica ends up holding more than
// the bound allows, where the load-blind ring piles the hot sessions together.
func TestTheBoundHoldsEveryReplicaNearTheMean(t *testing.T) {
	f := newFleet(t, 6)
	p := policy.NewBoundedSessionAffinity(cacheRouteBound)

	// Two hot sessions and a long tail, so the load-blind ring would put at
	// least a third of the traffic on one replica.
	const requests = 600
	sessions := sessionIDs(40)
	for i := range requests {
		id := sessions[i%len(sessions)]
		if i%3 != 0 {
			id = sessions[i%2]
		}
		choice, err := p.Choose(forSession(id), f.State())
		if err != nil {
			t.Fatalf("choose: %v", err)
		}
		load(t, f, choice.Replica.ID, 1)
	}

	// ⌈1.25 × 600/6⌉ is the capacity the last request was placed under, and no
	// earlier request was placed under a looser one.
	const capacity = 125
	for _, c := range f.State().Replicas {
		if c.Inflight > capacity {
			t.Errorf("%s holds %d of %d requests against a bound of %d", c.ID, c.Inflight, requests, capacity)
		}
	}
}

// The branch no accepted bound can reach, reached the only way the seam allows:
// the constructor does not validate — ByName does — so a bound below zero builds
// a policy whose capacity is under the mean, and an evenly loaded fleet then has
// every replica over it. The session goes where the ring puts it rather than
// nowhere.
func TestEveryReplicaOverTheBoundFallsBackToTheRingsFirstChoice(t *testing.T) {
	p := policy.NewBoundedSessionAffinity(-0.5)
	state := carrying(map[string]int{
		"replica-0": 40, "replica-1": 40, "replica-2": 40,
		"replica-3": 40, "replica-4": 40, "replica-5": 40,
	})

	for _, id := range sessionIDs(50) {
		choice, err := p.Choose(forSession(id), state)
		if err != nil {
			t.Fatalf("session %s was dropped for want of a replica under the bound: %v", id, err)
		}
		if want := ringHome(t, state, id); choice.Replica.ID != want {
			t.Fatalf("session %s went to %s with every replica over the bound, want the ring's first choice %s", id, choice.Replica.ID, want)
		}
		if choice.Inflight != 40 {
			t.Fatalf("decision recorded inflight = %d, want the 40 the chosen replica held", choice.Inflight)
		}
	}
}

// An unidentified request has nothing to hash, under this policy as under the
// load-blind one, and is rotated for the same reason.
func TestBoundedUnidentifiedRequestsAreSpreadAndSayWhyRatherThanPinned(t *testing.T) {
	p := policy.NewBoundedSessionAffinity(cacheRouteBound)
	state := sixReplicas()

	seen := map[string]int{}
	for range len(state.Replicas) {
		choice, err := p.Choose(policy.Request{}, state)
		if err != nil {
			t.Fatalf("choose: %v", err)
		}
		if choice.Reason != policy.ReasonSessionUnidentified {
			t.Errorf("reason = %q, want %q: an unidentified request was not placed by the ring", choice.Reason, policy.ReasonSessionUnidentified)
		}
		seen[choice.Replica.ID]++
	}
	if len(seen) != len(state.Replicas) {
		t.Errorf("%d unidentified requests landed on %d of %d replicas (%v), want all six", len(state.Replicas), len(seen), len(state.Replicas), seen)
	}
}

func TestBoundedSessionAffinityWithNoReplicasCannotChoose(t *testing.T) {
	p := policy.NewBoundedSessionAffinity(cacheRouteBound)
	if _, err := p.Choose(forSession("chat-1"), fleet.State{}); err != policy.ErrNoReplica {
		t.Errorf("err = %v, want %v", err, policy.ErrNoReplica)
	}
}

// The bound is a judgement and not a measurement, so nothing may supply one on
// the run's behalf: a cell at a bound nobody stated is a cell labelled with a
// setting nobody chose.
func TestBoundedSessionAffinityIsResolvedByNameAndRefusesAnUnstatedBound(t *testing.T) {
	if _, err := policy.ByName(policy.BoundedSessionAffinityName, policy.Options{}); err == nil {
		t.Error("the policy was built with no bound stated, so something defaulted it")
	}
	if _, err := policy.ByName(policy.BoundedSessionAffinityName, policy.Options{InflightBound: -0.25}); err == nil {
		t.Error("a negative bound was accepted, which holds every replica below the fleet's mean")
	}

	p, err := policy.ByName(policy.BoundedSessionAffinityName, policy.Options{InflightBound: cacheRouteBound})
	if err != nil {
		t.Fatalf("by name: %v", err)
	}
	if p.Name() != policy.BoundedSessionAffinityName {
		t.Errorf("name = %q, want %q", p.Name(), policy.BoundedSessionAffinityName)
	}
	tuned, ok := p.(policy.BoundTuned)
	if !ok {
		t.Fatal("the policy does not report its bound, so the router cannot publish it and the harness cannot check a cell's label against it")
	}
	if got := tuned.BoundTunables(); got != cacheRouteBound {
		t.Errorf("the policy reports a bound of %v, want the %v it was built with", got, cacheRouteBound)
	}
}

// It sits beside the baseline it hardens, so the two read as a pair in every
// table: the ring blind to load, then the same ring with a bound.
func TestBoundedSessionAffinityIsComparedDirectlyAfterSessionAffinity(t *testing.T) {
	for i, name := range policy.Order {
		if name != policy.SessionAffinityName {
			continue
		}
		if i+1 >= len(policy.Order) || policy.Order[i+1] != policy.BoundedSessionAffinityName {
			t.Errorf("the comparison order is %v, want %s directly after %s", policy.Order, policy.BoundedSessionAffinityName, policy.SessionAffinityName)
		}
		return
	}
	t.Errorf("the comparison order %v does not name %s", policy.Order, policy.SessionAffinityName)
}
