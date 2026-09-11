package policy

import (
	"sync/atomic"

	"github.com/yuchia329/kvroute/internal/fleet"
)

// holding is one replica's leading run of a prompt, as the index that found it
// counts one: in blocks, and in that index's own unit of size — bytes of prompt
// for the prefix index, the engine's tokens for exact residency.
type holding struct {
	replica string
	blocks  int
	size    int
}

// affinityRule is the decision both cache-aware policies make once they know
// what each replica holds: take the longest leading run, separate its equal
// holders by load, decline it under the spill rule's pressure, and place the
// request on load when nobody holds anything.
//
// One rule for both, because policies 4 and 5 are compared to find out what
// exact knowledge of the caches is worth, and any other difference between them
// would land in that figure too. Two tie-breaks or two spill rules would be a
// difference between the policies that is not the mechanism.
type affinityRule struct {
	// spill is the pressure at which a match is declined. Zero declines nothing.
	spill Spill
	// cold routes the requests no replica holds anything for. It is a whole
	// least-outstanding policy rather than a reimplementation of one, so that a
	// cold request is placed exactly as policy 2 would place it — including its
	// tie rotation, without which an idle fleet would funnel every cold request
	// into the first replica and this policy's cold path would differ from the
	// baseline it is compared against for a reason that is not the mechanism.
	cold *LeastOutstanding
	// next rotates between replicas that tie on both match length and load, so
	// that an idle fleet — where every candidate sits at zero inflight — spreads
	// those requests instead of funnelling them onto one replica.
	next atomic.Uint64
}

func newAffinityRule(spill Spill) *affinityRule {
	return &affinityRule{spill: spill, cold: NewLeastOutstanding()}
}

// verdict is a decision with its match sizes in the unit of the index it was
// made from. Each policy files them under the Choice fields for that unit, so
// that a figure in bytes can never be read as one in tokens.
type verdict struct {
	choice Choice
	// matched is the run the chosen replica holds, and declined the run a spill
	// gave up.
	matched, declined int
}

// decide picks the replica for a prompt of which these replicas hold these
// runs.
func (r *affinityRule) decide(held []holding, state fleet.State) (verdict, error) {
	tied, size := bestHolders(held, state)
	if len(tied) == 0 {
		// Nothing anybody holds. There is no cache to preserve, so the only
		// signal left is load, and the reason says the request was cold rather
		// than that affinity was considered and declined. The two are different
		// decisions: a grid point producing nothing but cold decisions has an
		// index that is not finding anything, which is not a threshold set too
		// low, and no goodput figure beside it would tell them apart.
		return r.placeOnLoad(state, ReasonCold, held, 0, fleet.Candidate{})
	}

	// Among replicas holding the same leading run, load is the only thing left
	// to choose on, and choosing the idle one costs no cache locality: they all
	// hold the same prompt.
	//
	// This is not the spill rule. Spill declines the best match and pays a
	// prefill to escape a loaded replica; this sacrifices nothing, because there
	// is no better match to decline.
	best := leastLoadedOf(tied, &r.next)

	// A spill has to relieve the pressure it fired on, or it is not a spill.
	// Where it can find no relief the match is kept: declining it would pay a
	// prefill, forfeit the locality and record a decision in the column the grid
	// is read from, all for nothing.
	if reason, declined := r.spill.Declines(best, state); declined {
		if elsewhere := r.spill.targets(state, best, reason); len(elsewhere.Replicas) > 0 {
			// The match is real and is being given up, so the row records what
			// it was worth as well as where the request went instead.
			return r.placeOnLoad(elsewhere, reason, held, size, best)
		}
	}

	return verdict{
		choice: Choice{
			Replica: best.Replica,
			Reason:  ReasonPrefixAffinity,
			// Reported even though the match, not the load, selected the group:
			// how balanced a cache-aware policy leaves the fleet is the
			// comparison, and it has to be a figure the rows can show.
			Inflight: best.Inflight,
			KV:       best.KV,
		},
		matched: size,
	}, nil
}

// placeOnLoad puts a request on the least loaded replica and labels it with the
// reason it ended up there rather than on a match.
//
// Cold requests and spilled ones share this path deliberately. A cold request
// has to be placed exactly as policy 2 would place it — including its tie
// rotation, without which an idle fleet would funnel every cold request into
// the first replica — and a spilled one has to be placed the same way for the
// grid to be reading the threshold rather than a second tie-break.
//
// The match reported is the target's own, never the declined replica's. This
// figure is the router's prediction of the prefix cache hit the engine will
// record for the replica that actually serves the request, and a spilled row
// carrying the forfeited match would predict a hit on a replica the request
// never reached — which is the exact quantity the belief-divergence measurement
// is drawn from. What was given up is recorded separately.
func (r *affinityRule) placeOnLoad(state fleet.State, reason Reason, held []holding, declined int, turnedDown fleet.Candidate) (verdict, error) {
	choice, err := r.cold.Choose(Request{}, state)
	if err != nil {
		return verdict{}, err
	}
	choice.Reason = reason
	// Only a spill turned a replica down; a cold request is passed the zero
	// candidate, whose unread KV keeps "declined nothing" apart from "declined a
	// replica whose cache nobody had scraped".
	choice.DeclinedKV, choice.DeclinedInflight = turnedDown.KV, turnedDown.Inflight
	if target, present := state.Candidate(choice.Replica.ID); present {
		choice.KV = target.KV
	}
	return verdict{choice: choice, matched: heldBy(held, choice.Replica.ID), declined: declined}, nil
}

// heldBy is one replica's own run of this prompt, or zero if it holds none of
// it.
func heldBy(held []holding, replica string) int {
	for _, h := range held {
		if h.replica == replica {
			return h.size
		}
	}
	return 0
}

// bestHolders returns every replica tied for the longest leading run of this
// prompt, and how long that run is.
//
// Tied rather than the single best, because ties are the common case here
// rather than the exception, and breaking them the wrong way is a
// load-balancing failure with no symptom in the cache figures. Every request of
// this workload shares its opening — the chat envelope, and a shared system
// prompt on a configurable fraction of sessions — so a great many requests
// match several replicas equally. Taking the index's own deterministic order
// there sends all of them to whichever replica sorts first: measured over the
// multi-turn workload, that put 45% of requests on one replica of five.
// idea.md §5 strikes shared system prompts from the list of places prefix
// affinity should win for exactly that reason, and says concentrating them would
// be worse than scattering them.
//
// A replica the fleet no longer has is skipped, so a shorter run on a replica
// that is still there beats a longer one on a replica that is gone.
func bestHolders(held []holding, state fleet.State) ([]fleet.Candidate, int) {
	var tied []fleet.Candidate
	blocks, size := 0, 0
	for _, h := range held {
		// Both indexes order their matches longest first, so the first shorter
		// run ends the tied group.
		if len(tied) > 0 && h.blocks != blocks {
			break
		}
		candidate, present := state.Candidate(h.replica)
		if !present {
			continue
		}
		blocks, size = h.blocks, h.size
		tied = append(tied, candidate)
	}
	return tied, size
}
