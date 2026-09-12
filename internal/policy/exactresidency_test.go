package policy_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/kvevents"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/residency"
)

// engineBlock is the engine's block size, in tokens.
const engineBlock = 16

// tokenBlock returns sixteen tokens that no call with another seed returns.
func tokenBlock(seed uint32) []uint32 {
	out := make([]uint32, engineBlock)
	for i := range out {
		out[i] = seed*1000 + uint32(i)
	}
	return out
}

// prompt is a chat body and the tokens the engine computes for it: three full
// blocks and part of a fourth.
var prompt = struct {
	body   []byte
	tokens []uint32
}{
	body:   []byte(`{"model":"m","messages":[{"role":"user","content":"alpha"}]}`),
	tokens: slices.Concat(tokenBlock(1), tokenBlock(2), tokenBlock(3), tokenBlock(4)[:5]),
}

// engineTokens is the engines' tokenizer, answering from a table and noting
// which replica each request asked.
type engineTokens struct {
	mu      sync.Mutex
	prompts map[string][]uint32
	asked   []string
	err     error
	delay   time.Duration
}

func tokenizer() *engineTokens {
	return &engineTokens{prompts: map[string][]uint32{string(prompt.body): prompt.tokens}}
}

func (e *engineTokens) Tokenize(_ context.Context, replica fleet.Replica, body []byte) ([]uint32, error) {
	e.mu.Lock()
	e.asked = append(e.asked, replica.ID)
	e.mu.Unlock()
	time.Sleep(e.delay)
	if e.err != nil {
		return nil, e.err
	}
	tokens, known := e.prompts[string(body)]
	if !known {
		return nil, fmt.Errorf("no tokens for %s", body)
	}
	return tokens, nil
}

func (e *engineTokens) askedOf() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.asked)
}

// exactFleet is n idle replicas and a residency index over them holding nothing.
func exactFleet(t *testing.T, n int) (fleet.State, *residency.Index) {
	t.Helper()
	state := fleetOf(t, n)
	ids := make([]string, 0, n)
	for _, c := range state.Replicas {
		ids = append(ids, c.ID)
	}
	ix, err := residency.New(engineBlock, ids)
	if err != nil {
		t.Fatalf("residency.New: %v", err)
	}
	return state, ix
}

// holds has a replica's engine report storing these blocks from the start of a
// prompt.
func holds(ix *residency.Index, replica string, blocks ...[]uint32) {
	names := make([]uint64, len(blocks))
	for i, b := range blocks {
		names[i] = uint64(b[0])<<8 | uint64(i)
	}
	ix.Apply(replica, kvevents.Batch{Events: []kvevents.Event{{
		Kind:        kvevents.Stored,
		BlockHashes: names,
		TokenIDs:    slices.Concat(blocks...),
		BlockSize:   engineBlock,
		Medium:      "GPU",
	}}})
}

// The mechanism: a prompt goes to the replica whose engine says it holds the
// longest leading run of it, and the decision carries that run in the engine's
// own tokens — a figure the engine's usage block can hold it to exactly.
func TestAPromptGoesToTheReplicaWhoseEngineHoldsIt(t *testing.T) {
	state, ix := exactFleet(t, 3)
	holds(ix, "replica-1", tokenBlock(1), tokenBlock(2))
	holds(ix, "replica-2", tokenBlock(1))
	p := policy.NewExactResidency(ix, tokenizer(), policy.Spill{})

	got, err := p.Choose(policy.Request{Body: prompt.body}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Replica.ID != "replica-1" {
		t.Errorf("went to %s, want replica-1, which holds two blocks of it", got.Replica.ID)
	}
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q", got.Reason, policy.ReasonPrefixAffinity)
	}
	if got.PrefixMatchTokens != 32 {
		t.Errorf("predicted %d cached tokens, want 32", got.PrefixMatchTokens)
	}
	if got.PrefixMatchBytes != 0 {
		t.Errorf("carried a %dB match: this policy predicts in tokens and has no byte figure to give", got.PrefixMatchBytes)
	}
}

// A prompt no engine holds any of is cold, placed exactly as policy 2 would
// place it.
func TestAPromptNoEngineHoldsIsColdAndGoesToTheLeastLoaded(t *testing.T) {
	state, ix := exactFleet(t, 3)
	state.Replicas[0].Inflight, state.Replicas[1].Inflight, state.Replicas[2].Inflight = 9, 4, 1
	p := policy.NewExactResidency(ix, tokenizer(), policy.Spill{})

	got, err := p.Choose(policy.Request{Body: prompt.body}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Replica.ID != "replica-2" || got.Reason != policy.ReasonCold {
		t.Errorf("went to %s as %q, want the least loaded replica-2 as %q", got.Replica.ID, got.Reason, policy.ReasonCold)
	}
}

// Without tokens there is nothing to match, so the request is placed on load.
// It carries its own reason rather than COLD: cold says no engine held the
// prompt, and here nobody could look. A run whose tokenizer was failing would
// otherwise read as an index that found nothing.
func TestAPromptThatCouldNotBeTokenizedIsPlacedOnLoadUnderItsOwnReason(t *testing.T) {
	state, ix := exactFleet(t, 3)
	holds(ix, "replica-0", tokenBlock(1), tokenBlock(2))
	state.Replicas[0].Inflight, state.Replicas[1].Inflight, state.Replicas[2].Inflight = 3, 0, 5
	engines := tokenizer()
	engines.err = errors.New("the API server did not answer")
	p := policy.NewExactResidency(ix, engines, policy.Spill{})

	got, err := p.Choose(policy.Request{Body: prompt.body}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Replica.ID != "replica-1" {
		t.Errorf("went to %s, want the least loaded replica-1", got.Replica.ID)
	}
	if got.Reason != policy.ReasonPromptUntokenized {
		t.Errorf("reason = %q, want %q", got.Reason, policy.ReasonPromptUntokenized)
	}
}

// The comparison is between two ways of knowing what a replica holds, so both
// policies decline a match under the same pressure: policy 4's spill rule, both
// conditions, and nothing of this policy's own.
func TestTheSpillRuleIsPolicyFoursOwn(t *testing.T) {
	t.Run("load", func(t *testing.T) {
		state, ix := exactFleet(t, 3)
		holds(ix, "replica-0", tokenBlock(1), tokenBlock(2))
		state.Replicas[0].Inflight, state.Replicas[1].Inflight, state.Replicas[2].Inflight = 21, 12, 10
		p := policy.NewExactResidency(ix, tokenizer(), policy.Spill{LoadImbalanceFactor: 2})

		got, _ := p.Choose(policy.Request{Body: prompt.body}, state)
		if got.Replica.ID != "replica-2" || got.Reason != policy.ReasonSpillLoad {
			t.Fatalf("went to %s as %q, want replica-2 as %q", got.Replica.ID, got.Reason, policy.ReasonSpillLoad)
		}
		if got.DeclinedMatchTokens != 32 || got.DeclinedInflight != 21 {
			t.Errorf("declined %d tokens at %d inflight, want 32 at 21", got.DeclinedMatchTokens, got.DeclinedInflight)
		}
		if got.PrefixMatchTokens != 0 {
			t.Errorf("predicted %d cached tokens on a replica that holds none", got.PrefixMatchTokens)
		}
	})
	t.Run("unhonoured", func(t *testing.T) {
		state, ix := exactFleet(t, 2)
		holds(ix, "replica-0", tokenBlock(1))
		state.Replicas[0].HitRate, state.Replicas[1].HitRate = hitting(0.05), hitting(0.90)
		p := policy.NewExactResidency(ix, tokenizer(), policy.Spill{HitRateLowWater: 0.20})

		got, _ := p.Choose(policy.Request{Body: prompt.body}, state)
		if got.Replica.ID != "replica-1" || got.Reason != policy.ReasonSpillHitRate {
			t.Errorf("went to %s as %q, want replica-1 as %q", got.Replica.ID, got.Reason, policy.ReasonSpillHitRate)
		}
	})
}

// Equal holders are separated by load, as policy 4 separates them: the cache
// is a wash between them, so the idle one costs nothing.
func TestReplicasHoldingTheSameRunAreSeparatedByLoad(t *testing.T) {
	state, ix := exactFleet(t, 3)
	for _, id := range []string{"replica-0", "replica-1", "replica-2"} {
		holds(ix, id, tokenBlock(1), tokenBlock(2))
	}
	state.Replicas[0].Inflight, state.Replicas[1].Inflight, state.Replicas[2].Inflight = 40, 40, 0
	p := policy.NewExactResidency(ix, tokenizer(), policy.Spill{})

	got, _ := p.Choose(policy.Request{Body: prompt.body}, state)
	if got.Replica.ID != "replica-2" || got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("went to %s as %q, want the idle replica-2 as %q", got.Replica.ID, got.Reason, policy.ReasonPrefixAffinity)
	}
}

// Every prompt is tokenized twice on the fleet — once to route it, once to serve
// it — and the first of those is this policy's cost. It is spread across the
// replicas in turn, so that no one API server carries the whole fleet's routing
// on top of its own serving.
func TestTokenizingIsSpreadAcrossTheFleetInTurn(t *testing.T) {
	state, ix := exactFleet(t, 3)
	engines := tokenizer()
	p := policy.NewExactResidency(ix, engines, policy.Spill{})

	for range 6 {
		if _, err := p.Choose(policy.Request{Body: prompt.body}, state); err != nil {
			t.Fatalf("Choose: %v", err)
		}
	}
	want := []string{"replica-0", "replica-1", "replica-2", "replica-0", "replica-1", "replica-2"}
	if got := engines.askedOf(); !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v", got, want)
	}
}

// Tokenizing is on the request path and inside the router overhead the project
// reports. The decision carries its share separately, so that the overhead of
// knowing exactly can be told apart from the overhead of deciding.
func TestTheDecisionCarriesHowLongTokenizingTook(t *testing.T) {
	state, ix := exactFleet(t, 2)
	engines := tokenizer()
	engines.delay = 5 * time.Millisecond
	p := policy.NewExactResidency(ix, engines, policy.Spill{})

	got, err := p.Choose(policy.Request{Body: prompt.body}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Tokenize < engines.delay {
		t.Errorf("recorded %v tokenizing, want at least the %v the engine took", got.Tokenize, engines.delay)
	}
}

func TestByNameNeedsAResidencyIndexAndATokenizerForExactResidency(t *testing.T) {
	_, ix := exactFleet(t, 2)
	if _, err := policy.ByName(policy.ExactResidencyName, policy.Options{Tokenizer: tokenizer()}); err == nil {
		t.Error("exact residency was built with no residency index")
	}
	if _, err := policy.ByName(policy.ExactResidencyName, policy.Options{ResidencyIndex: ix}); err == nil {
		t.Error("exact residency was built with no tokenizer, so it could match nothing")
	}
	p, err := policy.ByName(policy.ExactResidencyName, policy.Options{ResidencyIndex: ix, Tokenizer: tokenizer()})
	if err != nil {
		t.Fatalf("ByName: %v", err)
	}
	if p.Name() != policy.ExactResidencyName {
		t.Errorf("policy reports its name as %q", p.Name())
	}
}
