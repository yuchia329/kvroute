package policy

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/residency"
)

// ExactResidencyName is the configuration name of the exact-residency policy.
const ExactResidencyName = "exact_residency"

// Tokenizer turns a chat request into the token ids an engine computes for it.
type Tokenizer interface {
	Tokenize(ctx context.Context, replica fleet.Replica, body []byte) ([]uint32, error)
}

// ExactResidency routes a request to the replica whose engine reports holding
// the longest leading run of its prompt.
//
// This is policy 5 (idea.md §5): prefix affinity with the router's belief
// replaced by the engines' own account of their caches, taken from the KV cache
// events they publish. It makes policy 4's decision — the same tie-break by
// load, the same spill rule — so that the comparison between the two measures
// what exact knowledge is worth and nothing else.
//
// It differs in what it knows and in what knowing costs. What it knows is what
// the engines have reported storing and not yet reported removing, with nothing
// to bound or expire, and nothing about a prefill still in flight. What it costs
// is a tokenization per request before routing: the engines name their blocks
// by tokens, and the router has no tokenizer of its own.
type ExactResidency struct {
	index     *residency.Index
	tokenizer Tokenizer
	// rule is the decision made once the index has said what each replica
	// holds, shared with prefix affinity.
	rule *affinityRule
	// ask rotates which replica tokenizes each prompt. Every prompt is
	// tokenized twice on the fleet — once to route it and once to serve it —
	// and the first is this policy's cost. Spread in turn, it lands on every
	// API server alike instead of on whichever one sorts first.
	ask atomic.Uint64
}

// NewExactResidency builds the exact-residency policy over an index the
// engines' events feed, declining matches at the given pressure. A zero Spill
// declines nothing.
func NewExactResidency(index *residency.Index, tokenizer Tokenizer, spill Spill) *ExactResidency {
	return &ExactResidency{index: index, tokenizer: tokenizer, rule: newAffinityRule(spill)}
}

func (p *ExactResidency) Name() string { return ExactResidencyName }

// Tunables reports the spill thresholds this policy is running, which are
// prefix affinity's.
func (p *ExactResidency) Tunables() Spill { return p.rule.spill }

// Choose sends the request to the replica whose engine holds the longest
// leading run of its prompt, and to the least loaded replica when none holds
// any.
//
// Nothing is recorded about the decision. The index learns that a replica holds
// this prompt when that replica's engine says so, after it has computed the
// blocks, rather than when the router sends it there — which is exactly what
// makes it exact, and also why it cannot see a prefill that is still queued.
func (p *ExactResidency) Choose(req Request, state fleet.State) (Choice, error) {
	if len(state.Replicas) == 0 {
		return Choice{}, ErrNoReplica
	}

	ctx := req.Context
	if ctx == nil {
		ctx = context.Background()
	}
	asked := state.Replicas[(p.ask.Add(1)-1)%uint64(len(state.Replicas))].Replica
	started := time.Now()
	tokens, err := p.tokenizer.Tokenize(ctx, asked, req.Body)
	took := time.Since(started)
	if err != nil {
		// Without tokens there is nothing to match, so the request is placed on
		// load under a reason of its own: cold would say no engine held the
		// prompt, and here nobody could look.
		v, err := p.rule.placeOnLoad(state, ReasonPromptUntokenized, nil, 0, fleet.Candidate{})
		if err != nil {
			return Choice{}, err
		}
		v.choice.Tokenize = took
		return v.choice, nil
	}

	matches := p.index.Match(tokens)
	held := make([]holding, len(matches))
	for i, m := range matches {
		held[i] = holding{replica: m.Replica, blocks: m.Blocks, size: m.Tokens}
	}
	v, err := p.rule.decide(held, state)
	if err != nil {
		return Choice{}, err
	}
	v.choice.PrefixMatchTokens, v.choice.DeclinedMatchTokens = v.matched, v.declined
	v.choice.Tokenize = took
	return v.choice, nil
}
