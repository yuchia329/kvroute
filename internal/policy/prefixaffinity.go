package policy

import (
	"sync/atomic"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/prefix"
)

// PrefixAffinityName is the configuration name of the prefix-affinity policy.
const PrefixAffinityName = "prefix_affinity"

// DecisionBudget is what one routing decision may cost.
//
// idea.md §4.1 reports the router's accept-to-dispatch overhead as a result in
// its own right and claims a sub-millisecond figure for it. A routing decision
// is only part of that cost — reading the body and building the upstream
// request are the rest — so the decision itself is held to a tenth of it. The
// figure is here rather than in the router because this is the policy the budget
// is actually about: the other three make their choice out of a handful of
// integers, and only this one walks an index on the request path.
const DecisionBudget = 100 * time.Microsecond

// PrefixAffinity routes a request to the replica already believed to hold the
// longest leading run of its prompt.
//
// This is policy 4, the one the project exists to measure. Where session
// affinity knows only that a conversation went somewhere before, this knows what
// each replica is believed to hold, which is what lets it find cache locality no
// session id describes: conversations that branch from a common ancestor under a
// new session id, and shared context blocks that lead a prompt.
//
// It has no spill rule yet, and that absence is deliberate rather than
// unfinished. Declining affinity under KV or load pressure is a separate change
// with its own thresholds and its own decision reasons, and landing the two
// together would leave the comparison unable to say which of them moved the
// result.
type PrefixAffinity struct {
	index *prefix.Index
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

// NewPrefixAffinity builds the prefix-affinity policy over a calibrated index.
func NewPrefixAffinity(index *prefix.Index) *PrefixAffinity {
	return &PrefixAffinity{index: index, cold: NewLeastOutstanding()}
}

func (p *PrefixAffinity) Name() string { return PrefixAffinityName }

// Choose sends the request to the replica with the best prefix match, and to
// the least loaded replica when nothing matches.
//
// The chosen replica is then admitted to the index for this prompt's blocks.
// Recording the belief at the decision rather than after the dispatch is what
// keeps a burst of one conversation's turns together: the turns of a session
// arrive close enough to overlap, and a belief recorded once the first response
// came back would leave every turn behind it looking at an index that had not
// heard of the conversation. The cost is that a request the router then fails to
// dispatch leaves a belief behind it, which the TTL clears and which points at a
// replica the fleet had a moment ago.
func (p *PrefixAffinity) Choose(req Request, state fleet.State) (Choice, error) {
	if len(state.Replicas) == 0 {
		return Choice{}, ErrNoReplica
	}

	chain := prefix.Blocks(req.Body)
	choice, err := p.choose(chain, state)
	if err != nil {
		return Choice{}, err
	}
	p.index.Admit(chain, choice.Replica.ID)
	return choice, nil
}

// choose picks the replica, leaving the index untouched.
func (p *PrefixAffinity) choose(chain prefix.Chain, state fleet.State) (Choice, error) {
	tied, bytes := p.bestHolders(chain, state)
	if len(tied) > 0 {
		// Among replicas believed to hold the same leading run, load is the only
		// thing left to choose on, and choosing the idle one costs no cache
		// locality: they are all believed to hold the same bytes.
		//
		// This is not the spill rule. Spill declines the best match and pays a
		// prefill to escape a loaded replica; this sacrifices nothing, because
		// there is no better match to decline.
		chosen := leastLoadedOf(tied, &p.next)
		return Choice{
			Replica: chosen.Replica,
			Reason:  ReasonPrefixAffinity,
			// Reported even though the match, not the load, selected the group:
			// how balanced a cache-aware policy leaves the fleet is the
			// comparison, and it has to be a figure the rows can show.
			Inflight:         chosen.Inflight,
			PrefixMatchBytes: bytes,
		}, nil
	}

	// Nothing anybody holds. There is no cache to preserve, so the only signal
	// left is load, and the reason says the request was cold rather than that
	// affinity was considered and declined — those become different decisions
	// once the spill rule exists, and a record that collapsed them now could not
	// be read against one that does not.
	choice, err := p.cold.Choose(Request{}, state)
	if err != nil {
		return Choice{}, err
	}
	choice.Reason = ReasonCold
	return choice, nil
}

// bestHolders returns every replica tied for the longest leading run of this
// prompt, and how many bytes that run is.
//
// Tied rather than the single best, because ties are the common case here
// rather than the exception, and breaking them the wrong way is a
// load-balancing failure with no symptom in the cache figures. Every request of
// this workload shares its opening bytes — the chat envelope, and a shared
// system prompt on a configurable fraction of sessions — so a great many
// requests match several replicas equally. Taking the index's own deterministic
// order there sends all of them to whichever replica sorts first: measured over
// the multi-turn workload, that put 45% of requests on one replica of five.
// idea.md §5 strikes shared system prompts from the list of places this policy
// should win for exactly that reason, and says concentrating them would be
// worse than scattering them.
//
// A replica the fleet no longer has is skipped, so a shorter run on a replica
// that is still there beats a longer one on a replica that is gone.
func (p *PrefixAffinity) bestHolders(chain prefix.Chain, state fleet.State) ([]fleet.Candidate, int) {
	var tied []fleet.Candidate
	blocks, bytes := 0, 0
	for _, match := range p.index.Match(chain) {
		// Match is ordered longest first, so the first shorter run ends the
		// tied group.
		if len(tied) > 0 && match.Blocks != blocks {
			break
		}
		candidate, present := state.Candidate(match.Replica)
		if !present {
			continue
		}
		blocks, bytes = match.Blocks, match.Bytes
		tied = append(tied, candidate)
	}
	return tied, bytes
}
