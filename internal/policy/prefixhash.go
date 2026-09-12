package policy

import (
	"fmt"
	"sync/atomic"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/prefix"
)

// PrefixHashName is the configuration name of the stateless prefix-hash policy.
const PrefixHashName = "prefix_hash"

// HashPoint is the grid point the stateless prefix hash routes at: how much of a
// prompt the hash reads, and what the hash is worth against load.
//
// Both are judgements rather than measurements, which puts them in the same
// class as the spill thresholds and not in ADR-0006's: a value here is a grid
// point a cell records, never a fact about the fleet. Neither is defaulted. The
// window especially — OpenAI's documented router hashes "the initial tokens" and
// no number has ever been published for it, so any default here would be a guess
// wearing a citation.
type HashPoint struct {
	// LeadingBlocks is how many of the prompt's leading prefix blocks the hash
	// covers, in the prefix index's own blocks (prefix.BlockBytes each).
	//
	// It is a window and not a prefix length, and the two ends of it are real
	// constraints. Too short and every conversation carrying the shared system
	// prompt hashes to one key, which concentrates exactly the traffic idea.md
	// §5 says should be scattered. Too long and the window reaches into the part
	// of the prompt that grows, so turn 2 of a conversation hashes somewhere
	// other than turn 1 and the policy loses the locality it exists for.
	LeadingBlocks int `json:"leading_blocks"`
	// HashWeight is what one step down the hash's ranking of the replicas is
	// worth, in inflight requests.
	//
	// The two terms are added in one unit because the decision is a comparison
	// between them: the replica the hash ranks first is kept while it carries
	// fewer than this many more requests than the replica ranked second, and so
	// on down the ring. A weight of zero is least-outstanding with a hash that
	// decides nothing, and a weight above any imbalance the fleet can show is the
	// hash with no load term. Those are the ends of the axis rather than
	// degenerate settings, and both are worth running: they are what says whether
	// the balance in between is doing anything.
	HashWeight float64 `json:"hash_weight"`
}

// Stated reports whether a window was set. Its zero value is not a policy: a
// hash over no blocks is no hash at all.
func (h HashPoint) Stated() bool { return h.LeadingBlocks > 0 }

// Validate refuses a point that cannot mean what it says.
func (h HashPoint) Validate() error {
	if h.LeadingBlocks < 0 {
		return fmt.Errorf("policy: the hash window is a count of leading prefix blocks and cannot be negative, got %d", h.LeadingBlocks)
	}
	if h.HashWeight < 0 {
		return fmt.Errorf("policy: a hash weight of %v is negative, so the replicas the hash ranked last would be preferred: use 0 to route on load alone", h.HashWeight)
	}
	return nil
}

// WindowBytes is how much prompt the window covers. The grid point is written in
// blocks and the policy reads a prompt in bytes, and a window nobody can convert
// is a window nobody can check against a prompt.
func (h HashPoint) WindowBytes() int { return h.LeadingBlocks * prefix.BlockBytes }

// String renders the grid point, with the window in blocks and in the bytes it
// covers.
func (h HashPoint) String() string {
	if !h.Stated() {
		return "off"
	}
	return fmt.Sprintf("%d blocks (%dB) weight %.1f", h.LeadingBlocks, h.WindowBytes(), h.HashWeight)
}

// HashTuned is implemented by the policy carrying a hash grid point, so the
// router can publish it and the harness can refuse a sweep whose cells would be
// labelled with a point the router is not running.
//
// Separate from Tuned rather than folded into it. The two policies' knobs are
// different quantities — a pressure at which a match is declined, against the
// window and weighting of a hash — and one interface returning both would hand
// every caller a struct of which half is always zero, which is how a cell ends
// up labelled with a threshold nothing applied.
type HashTuned interface {
	HashTunables() HashPoint
}

// PrefixHash routes on a hash of the prompt's leading blocks combined with
// replica load, holding no index and no belief about what any replica caches.
//
// It is the control that isolates what the prefix index buys. Policy 4 tracks
// believed residency in a trie and decays it; this tracks nothing, and the pair
// therefore asks what tracking belief is worth over a stateless prefix hash that
// costs a ring walk. With exact residency above it the project has a ladder of how much
// residency knowledge a router has: none, believed, exact.
//
// It is deliberately outside idea.md §5's five and nothing downstream depends on
// it.
//
// It is not a reproduction of OpenAI's router. The mechanism is inferred from
// their public documentation — they route by "a hash of the initial tokens" plus
// machine load, with prompt_cache_key folded into that hash as a disambiguator,
// and their docs are explicit that such keys "influence routing; they do not pin
// requests to a machine or guarantee a cache read hit" — and not from their
// implementation, which is not published. How many tokens "initial" is, how load
// is weighed against the hash, and how the hash is placed on the machines are
// all choices made here. Wherever this comparison is reported it is reported as
// that: a stateless prefix hash with a load term, of the kind a frontier lab has
// documented running, not theirs.
type PrefixHash struct {
	point HashPoint
	// ring places the hash on the fleet, and is session affinity's own. The two
	// policies differ in what they hash — a session id against a prompt's
	// leading blocks — and must not differ in how they place it: a replica
	// leaving has to move the same share of the space under both, or the
	// recovery curves would be comparing two placements rather than two keys.
	ring ringCache
	// next rotates between replicas that tie, so that an idle fleet — where
	// every candidate sits at zero inflight and the ranking is all that
	// separates them — does not funnel the prompts it cannot hash onto one
	// replica.
	next atomic.Uint64
}

// NewPrefixHash builds the stateless prefix-hash policy at a grid point. The
// point is validated by ByName, which is how a router gets one.
func NewPrefixHash(point HashPoint) *PrefixHash { return &PrefixHash{point: point} }

func (p *PrefixHash) Name() string { return PrefixHashName }

// HashTunables reports the grid point this policy is running.
func (p *PrefixHash) HashTunables() HashPoint { return p.point }

// Choose ranks the replicas by where the prompt's leading blocks hash onto the
// ring, and takes the first one that load does not argue against.
//
// Nothing is recorded. That is the policy: the router learns nothing from having
// routed this request, so the same prompt arriving on a router that has served a
// million requests and on one just started is placed identically. It is why this
// policy has no TTL, no node cap and no calibration — there is nothing held to
// bound or to expire.
func (p *PrefixHash) Choose(req Request, state fleet.State) (Choice, error) {
	if len(state.Replicas) == 0 {
		return Choice{}, ErrNoReplica
	}

	key, hashed := p.key(req.Body)
	if !hashed {
		// A prompt with fewer leading blocks than the window is not hashed on
		// what it has. Hashing a short prompt over the blocks it happens to fill
		// would give a conversation a different key on every turn until it grew
		// past the window, which is the opposite of what the window is for, and
		// it would do so silently. The request goes on load under a reason of its
		// own, so a run whose prompts never filled the window reads as that
		// rather than as a hash that preferred nothing.
		chosen := leastLoadedOf(state.Replicas, &p.next)
		return Choice{Replica: chosen.Replica, Reason: ReasonPromptUnhashed, Inflight: chosen.Inflight}, nil
	}

	ranked := p.ring.For(state).preferenceAt(key)
	chosen := fewestBy(state.Replicas, func(c fleet.Candidate) float64 {
		return float64(c.Inflight) + p.point.HashWeight*float64(rankOf(ranked, c.ID))
	}, &p.next)

	reason := ReasonHashDeflected
	if rankOf(ranked, chosen.ID) == 0 {
		reason = ReasonPrefixHash
	}
	return Choice{
		Replica: chosen.Replica,
		Reason:  reason,
		// Reported for the reason session affinity reports it: how balanced a
		// policy leaves the fleet is what it is compared on, and it has to be a
		// figure the rows can show rather than a claim about this policy's code.
		Inflight: chosen.Inflight,
	}, nil
}

// key is where this prompt's leading blocks hash to on the ring, and whether
// there were enough of them to have a key at all.
//
// The chunking and the hashing are the prefix index's, called rather than
// copied. That is what makes this policy and policy 4 a comparison: they read a
// prompt identically and differ only in what they do with what they read. A
// chained hash also makes the window exactly what it claims to be — block N's
// value covers every byte before it as well as its own, so one value is the
// whole window and not the last block of it.
//
// The ring's own avalanche is applied on top, because that is how a key reaches
// a position on this ring; FNV's raw output diffuses poorly and a ring fed it
// would be lumpy for a reason belonging to the hash rather than to the workload.
func (p *PrefixHash) key(body []byte) (uint64, bool) {
	chain := prefix.Blocks(body)
	if len(chain) < p.point.LeadingBlocks {
		return 0, false
	}
	return avalanche(chain[p.point.LeadingBlocks-1]), true
}

// rankOf is how far down the hash's ranking a replica sits, counting from zero.
// A replica the ranking does not name sorts last, which cannot happen: the ring
// is built from the same snapshot the candidates come from.
func rankOf(ranked []fleet.Replica, id string) int {
	for i, r := range ranked {
		if r.ID == id {
			return i
		}
	}
	return len(ranked)
}
