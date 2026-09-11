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
// The spill rule is what stops that from being unconditional: a match on a
// replica that is out of cache room or buried under work is a match that costs
// more to take than to forfeit. Spill is configured rather than assumed, and its
// zero value declines nothing — which is exactly the policy measured before the
// rule existed, and the baseline this one is read against.
type PrefixAffinity struct {
	index *prefix.Index
	// rule is the decision made once the index has said what each replica is
	// believed to hold. It is shared with exact residency, so that the two
	// policies differ only in how they know.
	rule *affinityRule
}

// NewPrefixAffinity builds the prefix-affinity policy over a calibrated index,
// declining matches at the given pressure. A zero Spill declines nothing.
func NewPrefixAffinity(index *prefix.Index, spill Spill) *PrefixAffinity {
	return &PrefixAffinity{index: index, rule: newAffinityRule(spill)}
}

func (p *PrefixAffinity) Name() string { return PrefixAffinityName }

// IndexStats reports the index this policy routes on, so the router can publish
// how full it is and under what bounds.
//
// It is a method on the one policy that has an index rather than a member of the
// Policy interface, because three of the four have nothing to report and an
// interface method they all had to implement would make the absence look like a
// zero. The router asks for it by type assertion; see router.IndexReporter.
func (p *PrefixAffinity) IndexStats() prefix.Stats { return p.index.Stats() }

// Tunables reports the thresholds this policy is running, so the router can
// publish them and the harness can refuse to label cells with a grid point the
// router is not actually at.
func (p *PrefixAffinity) Tunables() Spill { return p.rule.spill }

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
	matches := p.index.Match(chain)
	held := make([]holding, len(matches))
	for i, m := range matches {
		held[i] = holding{replica: m.Replica, blocks: m.Blocks, size: m.Bytes}
	}
	v, err := p.rule.decide(held, state)
	if err != nil {
		return Choice{}, err
	}
	v.choice.PrefixMatchBytes, v.choice.DeclinedMatchBytes = v.matched, v.declined
	return v.choice, nil
}
