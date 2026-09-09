package policy

import (
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/yuchia329/kvroute/internal/fleet"
)

// SessionAffinityName is the configuration name of the consistent-hash session
// affinity policy.
const SessionAffinityName = "session_affinity"

// virtualNodesPerReplica is how many points on the ring each replica occupies.
//
// One point per replica would leave six replicas splitting the hash space at six
// arbitrary cuts, and the widest arc can easily be several times the narrowest —
// an imbalance belonging to the ring rather than to the workload, which would
// land in the skew axis's column and be read as a result. A hundred and twenty
// eight points each smooths that to within the bound
// TestSessionsSpreadAcrossTheWholeFleet checks, at the cost of a 768-entry
// sorted slice built once per fleet topology.
const virtualNodesPerReplica = 128

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
// It is deliberately blind to load. That blindness is not a shortcoming to be
// patched: it is the mechanism §0 predicts prefix affinity will beat, because a
// ring that lands hot sessions together has no escape hatch and keeps feeding
// them to the same replica. Fixing it here would delete the comparison.
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
	ring atomic.Pointer[ring]
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

	replica := p.ringFor(state).lookup(req.Session.ID)
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

// ringFor returns the ring for this fleet, building it only when the replica set
// has changed since the last request.
//
// The set is what is compared, not the candidates: a replica's inflight changes
// on every request and has no business rebuilding a ring that does not depend on
// it. Two requests arriving during a rebuild may each build one and one of them
// wins; both rings are the same ring, because it is derived from the replica set
// and nothing else.
func (p *SessionAffinity) ringFor(state fleet.State) *ring {
	key := topologyOf(state.Replicas)
	if current := p.ring.Load(); current != nil && current.topology == key {
		return current
	}
	built := newRing(state.Replicas)
	p.ring.Store(built)
	return built
}

// topologyOf names the replica set a ring was built for. Sorted, so that a fleet
// listing its replicas in a different order is the same fleet rather than a
// reason to rehash every session in it.
func topologyOf(replicas []fleet.Candidate) string {
	ids := make([]string, 0, len(replicas))
	for _, c := range replicas {
		ids = append(ids, c.ID)
	}
	slices.Sort(ids)
	return strings.Join(ids, "\x00")
}

// ring is the hash ring: virtual node positions in ascending order, each mapping
// to the replica that owns it.
//
// Consistent hashing rather than hash-modulo-count, because the two differ
// exactly where it matters. Modulo remaps almost every session when the replica
// count changes — five in six of them when one replica of six leaves — and every
// remapped session arrives at a replica that has never seen its history and must
// prefill the whole conversation again. A ring moves only the sessions that
// lived on the replica that left. That difference is what §7's chaos recovery
// measures, so the policy has to actually have the property rather than
// approximate it.
//
// It carries replicas rather than candidates: a candidate holds load as well as
// identity, and load read off a structure rebuilt only on topology change would
// be frozen at whichever request first built it. Keeping identity alone here
// makes that mistake unrepresentable rather than merely fixed.
type ring struct {
	topology  string
	positions []uint64
	owners    []fleet.Replica
}

func newRing(replicas []fleet.Candidate) *ring {
	type node struct {
		position uint64
		owner    fleet.Replica
	}
	nodes := make([]node, 0, len(replicas)*virtualNodesPerReplica)
	for _, r := range replicas {
		for v := range virtualNodesPerReplica {
			nodes = append(nodes, node{position: hash(fmt.Sprintf("%s#%d", r.ID, v)), owner: r.Replica})
		}
	}
	// Ties are broken by replica id so that the ring is a function of the
	// replica set alone: two routers over one fleet have to send a session to
	// the same replica, and a collision resolved by iteration order would make
	// that a per-process accident.
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].position != nodes[j].position {
			return nodes[i].position < nodes[j].position
		}
		return nodes[i].owner.ID < nodes[j].owner.ID
	})

	r := &ring{
		topology:  topologyOf(replicas),
		positions: make([]uint64, len(nodes)),
		owners:    make([]fleet.Replica, len(nodes)),
	}
	for i, n := range nodes {
		r.positions[i], r.owners[i] = n.position, n.owner
	}
	return r
}

// lookup walks clockwise from the session's position to the first virtual node,
// wrapping at the end of the ring.
func (r *ring) lookup(sessionID string) fleet.Replica {
	i, _ := slices.BinarySearch(r.positions, hash(sessionID))
	return r.owners[i%len(r.owners)]
}

// hash places a key on the ring: FNV-1a, then avalanched.
//
// FNV-1a because the ring wants a fast, stable function and nothing here is
// adversarial — a session id is a key a client supplies to be routed by, not one
// it is authenticated on. Stability across processes and across runs is the
// property that matters, so this must not be swapped for anything seeded per
// process: maphash would move every session on every restart.
//
// The avalanche is not optional. FNV-1a diffuses short, near-identical keys
// poorly, and every session id this fleet will ever see is one of those —
// "user-0", "user-1", "user-2" out of the generator, and a derived id is a hex
// string. Measured over 600 such ids on a six-replica ring, raw FNV-1a put 10
// sessions on one replica and 200 on another, a twenty-fold imbalance belonging
// to the hash rather than to the workload. That would have landed in the skew
// axis's column and been read as the load imbalance §5 predicts, which is the
// one result this policy exists to establish honestly.
//
// The finalizer is splitmix64's, which is a bijection, so it spreads the bits
// without introducing a collision FNV did not already have.
func hash(key string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	return avalanche(h.Sum64())
}

func avalanche(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}
