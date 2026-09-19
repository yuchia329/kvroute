package policy

import (
	"sync/atomic"

	"github.com/yuchia329/kvroute/internal/fleet"
)

// SessionAffinityName is the configuration name of the consistent-hash session
// affinity policy.
const SessionAffinityName = "session_affinity"

// SessionAffinity routes every turn of a conversation to the same replica, by
// hashing the session onto a ring of the replicas.
//
// This is the baseline that matters (idea.md §5). Sticky sessions capture most
// multi-turn cache locality with no prefix tracking at all, every load balancer
// ships them, and vLLM's own production-stack ships session-ID routing today
// while its prefix-aware routing is still marked WIP. A prefix index that only
// beat round-robin would be a radix tree built to beat a straw man.
//
// Because the session id arrives as a header, this policy gets a perfect oracle
// here — stronger than it would be in production, which is what makes separating
// from it worth something.
//
// It is deliberately blind to load, which makes it the first baseline: the
// blindness is the mechanism §0 predicts prefix affinity will beat, because a
// ring that lands hot sessions together has no escape hatch and keeps feeding
// them to the same replica. Fixing it here would delete that comparison and
// change what every cell already recorded under this name measured. The escape
// hatch is a policy of its own instead — BoundedSessionAffinity, the second and
// harder baseline (ADR-0016) — and the two are compared side by side.
//
// Note for anyone writing this up: this is not "what OpenAI shipped". OpenAI
// routes on a hash of the initial tokens *plus machine load*, with
// prompt_cache_key folded into that hash as a disambiguator, and their docs say
// such keys "influence routing; they do not pin requests to a machine". That is
// nearer prefix affinity with a load term than it is to this.
type SessionAffinity struct {
	// ring is rebuilt when the replica set changes and reused when it does not,
	// because hashing 768 virtual nodes on every request would put the ring's
	// construction inside the router overhead figure §4.1 reports.
	ring ringCache
	// next spreads the requests no session could be identified for. They have
	// nothing to hash, so they are rotated rather than all landing on whichever
	// replica the empty string maps to.
	next atomic.Uint64
}

// NewSessionAffinity builds the consistent-hash session affinity policy.
func NewSessionAffinity() *SessionAffinity { return &SessionAffinity{} }

func (p *SessionAffinity) Name() string { return SessionAffinityName }

// Choose sends the request to the replica its session hashes to.
func (p *SessionAffinity) Choose(req Request, state fleet.State) (Choice, error) {
	if len(state.Replicas) == 0 {
		return Choice{}, ErrNoReplica
	}
	if !req.Session.Known() {
		// Nothing to hash. Rotating is what a load balancer with no session key
		// does, and the reason says so rather than letting these requests read as
		// session affinity decisions: a pile of unidentified requests on one
		// replica looks exactly like one hot session, and the decision mix is a
		// reported result.
		chosen := state.Replicas[(p.next.Add(1)-1)%uint64(len(state.Replicas))]
		return Choice{Replica: chosen.Replica, Reason: ReasonSessionUnidentified, Inflight: chosen.Inflight}, nil
	}

	replica := p.ring.For(state).lookup(req.Session.ID)
	// The load is reported even though the hash did not weigh it. How badly this
	// policy leaves the fleet imbalanced under skew is the comparison §5 is
	// about, and it has to be a figure the rows can show rather than a claim
	// about this policy's code.
	//
	// Read off the snapshot rather than off the ring. The ring is rebuilt only
	// when the replica set changes, so anything it carried besides identity
	// would be frozen at whichever request first built it — an idle fleet — and
	// every row would report zero however loaded the fleet became. That is the
	// one figure this policy contributes to §5's imbalance claim, so it failing
	// silently is the whole cost of the mistake.
	chosen, present := state.Candidate(replica.ID)
	if !present {
		// Unreachable: the ring is rebuilt whenever the replica set changes, so
		// it can only name a replica of this snapshot. Handled rather than
		// asserted, because the alternative to a defined answer here is a
		// dropped request on a fleet that is fine.
		return Choice{Replica: replica, Reason: ReasonSessionAffinity}, nil
	}
	return Choice{Replica: chosen.Replica, Reason: ReasonSessionAffinity, Inflight: chosen.Inflight}, nil
}
