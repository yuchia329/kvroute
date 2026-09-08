package policy

import (
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
	for _, match := range p.index.Match(chain) {
		candidate, present := state.Candidate(match.Replica)
		if !present {
			// A replica the index believes in but the fleet no longer has. The
			// next best match is a better answer than a drop, and than falling
			// straight to load: a shorter match is still a match.
			continue
		}
		return Choice{
			Replica: candidate.Replica,
			Reason:  ReasonPrefixAffinity,
			// Reported even though it was not weighed, for the same reason
			// session affinity reports it: how badly a cache-aware policy leaves
			// the fleet imbalanced is the comparison, and it has to be a figure
			// the rows can show.
			Inflight:         candidate.Inflight,
			PrefixMatchBytes: match.Bytes,
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
