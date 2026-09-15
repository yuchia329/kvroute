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

// ringCache holds the ring a policy routes on, rebuilding it only when the
// replica set changes.
//
// Shared by the two policies that place a key on a ring — session affinity,
// which places a session id, and the stateless prefix hash, which places a
// prompt's leading blocks. They differ in what they hash and must not differ in
// how they place it: a replica leaving has to move the same share of the space
// under both, because that difference is what §7's recovery curves are read for.
type ringCache struct {
	current atomic.Pointer[ring]
}

// For returns the ring for this fleet, building it only when the replica set has
// changed since the last request.
//
// The set is what is compared, not the candidates: a replica's inflight changes
// on every request and has no business rebuilding a ring that does not depend on
// it. Two requests arriving during a rebuild may each build one and one of them
// wins; both rings are the same ring, because it is derived from the replica set
// and nothing else.
func (c *ringCache) For(state fleet.State) *ring {
	key := topologyOf(state.Replicas)
	if current := c.current.Load(); current != nil && current.topology == key {
		return current
	}
	built := newRing(state.Replicas)
	c.current.Store(built)
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
// exactly where it matters. Modulo remaps almost every key when the replica
// count changes — five in six of them when one replica of six leaves — and every
// remapped key arrives at a replica that has never seen its history and must
// prefill the whole conversation again. A ring moves only the keys that lived on
// the replica that left. That difference is what §7's chaos recovery measures,
// so the policies have to actually have the property rather than approximate it.
//
// It carries replicas rather than candidates: a candidate holds load as well as
// identity, and load read off a structure rebuilt only on topology change would
// be frozen at whichever request first built it. Keeping identity alone here
// makes that mistake unrepresentable rather than merely fixed.
type ring struct {
	topology  string
	positions []uint64
	owners    []fleet.Replica
	// members is how many distinct replicas the owners name, so that a walk
	// looking for all of them knows when it has them without counting first.
	members int
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
		members:   len(replicas),
	}
	for i, n := range nodes {
		r.positions[i], r.owners[i] = n.position, n.owner
	}
	return r
}

// lookup walks clockwise from the session's position to the first virtual node,
// wrapping at the end of the ring.
func (r *ring) lookup(sessionID string) fleet.Replica {
	return r.owners[r.from(hash(sessionID))%len(r.owners)]
}

// preferenceAt is every replica on the ring in the order a key at this position
// meets them, walking clockwise and naming each replica once.
//
// An order rather than a single owner, because the policy that asks for it
// weighs the hash against load and therefore has to know what second place is.
// Walking gives the same answer a single lookup would for first place, so the
// two ways of reading the ring cannot disagree about where a key belongs.
//
// The position is already on the ring — the caller hashed it — so a key is
// placed by exactly the function a session id is, and neither can drift from the
// other.
func (r *ring) preferenceAt(position uint64) []fleet.Replica {
	order := make([]fleet.Replica, 0, r.members)
	start := r.from(position)
	for step := 0; step < len(r.owners) && len(order) < r.members; step++ {
		owner := r.owners[(start+step)%len(r.owners)]
		// Linear over a handful of replicas, which is cheaper than the map a
		// set would cost on a path held to DecisionBudget.
		if !slices.ContainsFunc(order, func(seen fleet.Replica) bool { return seen.ID == owner.ID }) {
			order = append(order, owner)
		}
	}
	return order
}

// from is the index of the first virtual node at or clockwise of a position.
func (r *ring) from(position uint64) int {
	i, _ := slices.BinarySearch(r.positions, position)
	return i
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
