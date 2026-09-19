package policy

import (
	"fmt"
	"math"
	"strconv"
	"sync/atomic"

	"github.com/yuchia329/kvroute/internal/fleet"
)

// BoundedSessionAffinityName is the configuration name of the load-bounded
// consistent-hash session affinity policy.
const BoundedSessionAffinityName = "bounded_session_affinity"

// InflightBound is how far above the fleet's mean inflight a replica may be and
// still keep the session the ring placed on it, as a fraction of that mean: 0.25
// holds every replica to a quarter above it.
//
// It is the ε of consistent hashing with bounded loads, and the same quantity
// Envoy and HAProxy take as hash_balance_factor (theirs is 100 × (1 + ε)).
//
// A judgement rather than a measurement, which puts it in the class of the spill
// thresholds and the hash window and not in ADR-0006's: a value here is a grid
// point a cell records, never a fact about the fleet. So it is not defaulted.
// The repo runs it at one value, CacheRoute's 0.25, so that a reader comparing
// the two tables is comparing one policy — and that is a choice the run states,
// not one the code makes for it.
type InflightBound float64

// Stated reports whether a bound was set. Its zero value is not a policy: a
// bound of nothing above the mean is not the load-blind ring with a bound left
// off, it is a ring that deflects every session on a busy fleet, and nobody who
// left the flag unset asked for that.
func (b InflightBound) Stated() bool { return b > 0 }

// Validate refuses a bound that cannot mean what it says.
func (b InflightBound) Validate() error {
	if b < 0 || math.IsNaN(float64(b)) || math.IsInf(float64(b), 0) {
		return fmt.Errorf("policy: an inflight bound of %v is not a fraction above the fleet's mean inflight: a negative one would hold every replica below the mean, which no fleet can satisfy", float64(b))
	}
	return nil
}

// String renders the bound as the fraction it is, exactly: it is printed when
// two bounds disagree, and a rounded rendering would print a router at 0.254 and
// a label of 0.25 as the same number in the refusal that says they differ.
func (b InflightBound) String() string {
	if !b.Stated() {
		return "off"
	}
	return strconv.FormatFloat(float64(b), 'g', -1, 64) + " above mean inflight"
}

// capacity is the most requests a replica may hold, counting the one being
// placed, on a fleet of this many replicas holding this many between them.
//
// ⌈(1 + ε) × (total + 1) / replicas⌉: the rule as Mirrokni, Thorup and
// Zadimoghaddam state it, and as HAProxy implements it. The request being placed
// is counted into the mean, which is what makes the bound satisfiable — with it,
// the capacities sum to more than the fleet holds, so some replica always has
// room — and the ceiling is what keeps a nearly idle fleet from having a
// capacity of zero.
//
// The product is exact at 0.25, the bound this repo runs. At a bound binary
// cannot represent, the ceiling of a product that should be whole can land one
// higher; that is one request of slack on one replica, and it is the price of
// not carrying the bound as a ratio of integers for a sweep nobody is running.
func (b InflightBound) capacity(total, replicas int) int {
	capacity := math.Ceil((1 + float64(b)) * float64(total+1) / float64(replicas))
	if capacity >= math.MaxInt32 {
		// A bound so loose nothing is ever over it, rather than a conversion
		// that overflows into a capacity below zero.
		return math.MaxInt32
	}
	return int(capacity)
}

// BoundTuned is implemented by the policy carrying an inflight bound, so the
// router can publish it and the harness can refuse a sweep whose cells would be
// labelled with a bound the router is not running.
//
// Separate from Tuned and HashTuned for the reason those two are separate from
// each other: the knobs are different quantities on different policies, and one
// interface returning all of them would hand every caller a struct of which most
// is always zero.
type BoundTuned interface {
	BoundTunables() InflightBound
}

// BoundedSessionAffinity is session affinity with a load bound: the same ring,
// walked clockwise past any replica holding more than the bound allows, taking
// the first replica within it.
//
// It is the second baseline, and the harder one. Session affinity is blind to
// load on purpose, and every margin published against it is therefore a margin
// against a policy with no escape hatch — while Envoy and HAProxy turn this
// bound on with one setting. Until it is on the grid, a reader cannot tell
// whether a KV index beats a load-bounded hash or only a load-blind one
// (ADR-0016).
//
// It differs from session affinity in the bound alone. The ring is the same
// ring, the session identity is the same supplied identity, and an unidentified
// request is rotated the same way, so a difference between the two policies'
// cells is what the bound did and not a second hashing scheme.
//
// It holds no state about where a session went. A session the bound moved is
// walked from its own position again on its next turn, and returns to the
// replica the ring placed it on as soon as that replica is back within the
// bound. That is the algorithm as the prior art ships it, and it is kept: a
// variant that remembered deflections would be a session table, and would be
// compared against nothing anybody runs.
type BoundedSessionAffinity struct {
	bound InflightBound
	// ring is session affinity's own, for the reason the stateless prefix hash
	// shares it: the policies must not differ in how they place a key.
	ring ringCache
	// next spreads the requests no session could be identified for, as session
	// affinity's does.
	next atomic.Uint64
}

// NewBoundedSessionAffinity builds the load-bounded session affinity policy at a
// bound. The bound is validated by ByName, which is how a router gets one.
func NewBoundedSessionAffinity(bound InflightBound) *BoundedSessionAffinity {
	return &BoundedSessionAffinity{bound: bound}
}

func (p *BoundedSessionAffinity) Name() string { return BoundedSessionAffinityName }

// BoundTunables reports the bound this policy is running.
func (p *BoundedSessionAffinity) BoundTunables() InflightBound { return p.bound }

// Choose sends the request to the first replica clockwise of its session's
// position that has room under the bound.
func (p *BoundedSessionAffinity) Choose(req Request, state fleet.State) (Choice, error) {
	if len(state.Replicas) == 0 {
		return Choice{}, ErrNoReplica
	}
	if !req.Session.Known() {
		// Nothing to hash, so rotated and reported as such, exactly as the
		// load-blind policy does it. The bound is not consulted: it is a rule
		// about where a session may stay, and this request has no session.
		chosen := state.Replicas[(p.next.Add(1)-1)%uint64(len(state.Replicas))]
		return Choice{Replica: chosen.Replica, Reason: ReasonSessionUnidentified, Inflight: chosen.Inflight}, nil
	}

	total := 0
	for _, c := range state.Replicas {
		total += c.Inflight
	}
	capacity := p.bound.capacity(total, len(state.Replicas))

	order := p.ring.For(state).preferenceAt(hash(req.Session.ID))
	for rank, replica := range order {
		// Load is read off the snapshot and never off the ring, which carries
		// identity alone: see ring.
		candidate, present := state.Candidate(replica.ID)
		if !present || candidate.Inflight >= capacity {
			continue
		}
		reason := ReasonBoundDeflected
		if rank == 0 {
			reason = ReasonBoundedSessionAffinity
		}
		return Choice{Replica: candidate.Replica, Reason: reason, Inflight: candidate.Inflight}, nil
	}

	// Every replica is over the bound, so the ring's first choice is taken: the
	// session's cache is there, and no replica is a better place to be over the
	// bound than that one.
	//
	// Unreachable for any bound ByName accepts. The capacity is at least the
	// mean with this request counted, and every replica holding that much would
	// have the fleet holding more than it holds. Handled rather than asserted,
	// for the reason session affinity handles its own unreachable branch: the
	// alternative to a defined answer here is a dropped request on a fleet that
	// is fine.
	first := order[0]
	if candidate, present := state.Candidate(first.ID); present {
		return Choice{Replica: candidate.Replica, Reason: ReasonBoundedSessionAffinity, Inflight: candidate.Inflight}, nil
	}
	return Choice{Replica: first, Reason: ReasonBoundedSessionAffinity}, nil
}
