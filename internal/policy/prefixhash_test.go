package policy_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/session"
)

// testWindow is how many leading blocks the tests hash over.
//
// Four rather than the sixteen a run states, because the conversations these
// tests render are short: a window no prompt fills would send every request down
// the unhashed path and prove nothing about the hash.
const testWindow = 4

// hashPoint is the grid point the tests route at, with the weight named at each
// call because the weight is the policy.
func hashPoint(weight float64) policy.HashPoint {
	return policy.HashPoint{LeadingBlocks: testWindow, HashWeight: weight}
}

// openingOf renders a chat body that opens with one conversation's own text and
// then diverges, so that two bodies built from one opening share their leading
// blocks and nothing after them.
//
// The opening is padded past the hash window deliberately: the window is a
// length in bytes, and a helper that left it half-filled would let a test pass
// on a policy that hashed the whole prompt.
func openingOf(opening, rest string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `{"model":"llama","messages":[{"role":"user","content":%q}]}`,
		strings.Repeat(opening+" ", 40)+rest)
	return []byte(b.String())
}

// homesOfPrompts maps each opening to the replica the policy sends it to.
func homesOfPrompts(t *testing.T, p policy.Policy, state fleet.State, openings []string) map[string]string {
	t.Helper()
	home := make(map[string]string, len(openings))
	for _, opening := range openings {
		choice, err := p.Choose(policy.Request{Body: openingOf(opening, "")}, state)
		if err != nil {
			t.Fatalf("choose for %s: %v", opening, err)
		}
		home[opening] = choice.Replica.ID
	}
	return home
}

func openings(n int) []string {
	names := make([]string, 0, n)
	for i := range n {
		names = append(names, fmt.Sprintf("topic-%d", i))
	}
	return names
}

// The mechanism: every turn of a conversation resends the same opening, so
// hashing that opening puts every turn on one replica without the router
// remembering anything at all.
func TestEveryTurnOfAConversationHashesToTheSameReplica(t *testing.T) {
	p := policy.NewPrefixHash(hashPoint(4))
	state := sixReplicas()

	first, err := p.Choose(policy.Request{Body: conversation("alpha", 0)}, state)
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if first.Reason != policy.ReasonPrefixHash {
		t.Errorf("the first turn was reason %q, want %q", first.Reason, policy.ReasonPrefixHash)
	}
	for turn := 1; turn < 5; turn++ {
		got, err := p.Choose(policy.Request{Body: conversation("alpha", turn)}, state)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		if got.Replica.ID != first.Replica.ID {
			t.Fatalf("turn %d went to %s, but turn 0 went to %s: the hash is not stable across a growing prompt",
				turn, got.Replica.ID, first.Replica.ID)
		}
	}
}

// What the hash buys over a session id, and it buys it with no session id at
// all: two conversations that share an opening and diverge after it are one key,
// so the shared prefix is computed once.
func TestTwoPromptsSharingAnOpeningLandTogetherWithNoSessionId(t *testing.T) {
	p := policy.NewPrefixHash(hashPoint(4))
	state := sixReplicas()

	first, err := p.Choose(policy.Request{Body: openingOf("shared", "one tail")}, state)
	if err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	second, err := p.Choose(policy.Request{Body: openingOf("shared", "a different tail entirely")}, state)
	if err != nil {
		t.Fatalf("second prompt: %v", err)
	}
	if first.Replica.ID != second.Replica.ID {
		t.Errorf("two prompts sharing their leading blocks went to %s and %s, so the hash is reading past its window",
			first.Replica.ID, second.Replica.ID)
	}
}

// The chunking is the prefix index's, reused rather than reimplemented, so the
// two policies differ in what they remember and not in how they read a prompt.
// A prompt that changes only past the window's last whole block is the same key.
func TestTheWindowIsThePrefixIndexsOwnBlocks(t *testing.T) {
	p := policy.NewPrefixHash(hashPoint(testWindow))
	state := sixReplicas()

	body := openingOf("boundary", "")
	inside := prefix.BlockBytes * testWindow
	if len(body) <= inside {
		t.Fatalf("the test body is %d bytes, which does not reach the %d-byte window", len(body), inside)
	}

	home, err := p.Choose(policy.Request{Body: body}, state)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	// Every byte from the window's end onwards is rewritten. Nothing inside it
	// moves, so the key must not.
	changed := append(append([]byte{}, body[:inside]...), []byte(strings.Repeat("z", len(body)-inside))...)
	after, err := p.Choose(policy.Request{Body: changed}, state)
	if err != nil {
		t.Fatalf("choose after the window: %v", err)
	}
	if after.Replica.ID != home.Replica.ID {
		t.Errorf("rewriting every byte past block %d moved the request from %s to %s",
			testWindow, home.Replica.ID, after.Replica.ID)
	}
}

// The load term is half the policy: a hashed replica buried under work loses the
// request to a quieter sibling, and the decision says which of the two decided
// it.
func TestTheLoadTermDeflectsARequestOffItsHashedReplica(t *testing.T) {
	state := sixReplicas()
	body := openingOf("busy", "")

	home, err := policy.NewPrefixHash(hashPoint(4)).Choose(policy.Request{Body: body}, state)
	if err != nil {
		t.Fatalf("choose on an idle fleet: %v", err)
	}

	// The hashed replica is now carrying more than the weight is worth, and
	// every other replica is idle.
	loaded := sixReplicas()
	for i := range loaded.Replicas {
		if loaded.Replicas[i].ID == home.Replica.ID {
			loaded.Replicas[i].Inflight = 5
		}
	}
	deflected, err := policy.NewPrefixHash(hashPoint(4)).Choose(policy.Request{Body: body}, loaded)
	if err != nil {
		t.Fatalf("choose on a loaded fleet: %v", err)
	}
	if deflected.Replica.ID == home.Replica.ID {
		t.Fatalf("the request stayed on %s under 5 inflight against idle siblings, at a weight of 4", home.Replica.ID)
	}
	if deflected.Reason != policy.ReasonHashDeflected {
		t.Errorf("a request the load term moved was reason %q, want %q", deflected.Reason, policy.ReasonHashDeflected)
	}
	if deflected.Inflight != 0 {
		t.Errorf("the decision records %d inflight on the replica it chose, which was idle", deflected.Inflight)
	}
}

// The bottom of the weight axis, and the control it gives the sweep: a weight of
// zero is policy 2 with a hash that decides nothing.
func TestAWeightOfZeroRoutesOnLoadAlone(t *testing.T) {
	p := policy.NewPrefixHash(hashPoint(0))
	state := sixReplicas()
	for i := range state.Replicas {
		state.Replicas[i].Inflight = 10
	}
	state.Replicas[4].Inflight = 1

	for _, opening := range openings(20) {
		choice, err := p.Choose(policy.Request{Body: openingOf(opening, "")}, state)
		if err != nil {
			t.Fatalf("choose: %v", err)
		}
		if choice.Replica.ID != "replica-4" {
			t.Fatalf("%s went to %s at a weight of zero, where only load decides", opening, choice.Replica.ID)
		}
	}
}

// The top of the same axis: a weight above any imbalance the fleet can show is
// the stateless hash with no load term, which is the other control.
func TestAWeightAboveTheFleetsImbalanceIgnoresLoad(t *testing.T) {
	state := sixReplicas()
	body := openingOf("stubborn", "")

	home, err := policy.NewPrefixHash(hashPoint(1000)).Choose(policy.Request{Body: body}, state)
	if err != nil {
		t.Fatalf("choose on an idle fleet: %v", err)
	}
	loaded := sixReplicas()
	for i := range loaded.Replicas {
		if loaded.Replicas[i].ID == home.Replica.ID {
			loaded.Replicas[i].Inflight = 200
		}
	}
	stayed, err := policy.NewPrefixHash(hashPoint(1000)).Choose(policy.Request{Body: body}, loaded)
	if err != nil {
		t.Fatalf("choose on a loaded fleet: %v", err)
	}
	if stayed.Replica.ID != home.Replica.ID {
		t.Errorf("a weight of 1000 gave up %s at 200 inflight, so the load term is not weighted", home.Replica.ID)
	}
}

// A prompt shorter than the window has no leading blocks to hash, and saying so
// is the point: a run whose prompts never filled the window would otherwise read
// as a hash that found nothing to prefer.
func TestAPromptShorterThanTheWindowIsRoutedOnLoadAndSaysSo(t *testing.T) {
	p := policy.NewPrefixHash(hashPoint(64))
	state := sixReplicas()

	seen := map[string]int{}
	for range 12 {
		choice, err := p.Choose(policy.Request{Body: []byte(`{"messages":[]}`)}, state)
		if err != nil {
			t.Fatalf("choose: %v", err)
		}
		if choice.Reason != policy.ReasonPromptUnhashed {
			t.Fatalf("a prompt below the window was reason %q, want %q", choice.Reason, policy.ReasonPromptUnhashed)
		}
		seen[choice.Replica.ID]++
	}
	if len(seen) < 2 {
		t.Errorf("every prompt below the window landed on one replica: %v", seen)
	}
}

// The claim this policy is the control for: it holds no index and no belief, so
// a router that has served a million requests decides exactly as one just
// started.
func TestThePolicyRemembersNothingBetweenRequests(t *testing.T) {
	state := sixReplicas()
	warmed := policy.NewPrefixHash(hashPoint(4))
	for _, opening := range openings(500) {
		if _, err := warmed.Choose(policy.Request{Body: openingOf(opening, "")}, state); err != nil {
			t.Fatalf("warming: %v", err)
		}
	}

	fresh := policy.NewPrefixHash(hashPoint(4))
	for _, opening := range openings(50) {
		body := openingOf(opening, "")
		was, err := warmed.Choose(policy.Request{Body: body}, state)
		if err != nil {
			t.Fatalf("warmed: %v", err)
		}
		is, err := fresh.Choose(policy.Request{Body: body}, state)
		if err != nil {
			t.Fatalf("fresh: %v", err)
		}
		if was.Replica.ID != is.Replica.ID {
			t.Fatalf("%s went to %s on a warmed router and %s on a fresh one, so something is being remembered",
				opening, was.Replica.ID, is.Replica.ID)
		}
	}
}

// A lumpy ring would concentrate prompts on one replica for a reason belonging
// to the hash rather than to the workload, which would land in the skew axis's
// column and be read as a result.
func TestPromptsSpreadAcrossTheWholeFleet(t *testing.T) {
	p := policy.NewPrefixHash(hashPoint(1000))
	state := sixReplicas()

	const prompts = 600
	counts := map[string]int{}
	for _, replica := range homesOfPrompts(t, p, state, openings(prompts)) {
		counts[replica]++
	}
	if len(counts) != len(state.Replicas) {
		t.Fatalf("%d prompts landed on %d of %d replicas: %v", prompts, len(counts), len(state.Replicas), counts)
	}
	even := prompts / len(state.Replicas)
	for replica, n := range counts {
		if n < even/2 || n > even*2 {
			t.Errorf("%s holds %d of %d prompts against an even share of %d: %v", replica, n, prompts, even, counts)
		}
	}
}

// The ring's property, which this policy has for the reason session affinity has
// it: a replica leaving must move only the prompts that hashed to it, or the
// recovery curves would be reading hash-modulo-count.
func TestARemovedReplicaMovesOnlyItsOwnPrompts(t *testing.T) {
	p := policy.NewPrefixHash(hashPoint(1000))
	names := openings(600)

	before := homesOfPrompts(t, p, sixReplicas(), names)
	after := homesOfPrompts(t, p, stateOf("replica-0", "replica-1", "replica-2", "replica-3", "replica-4"), names)

	moved := 0
	for _, opening := range names {
		if before[opening] == after[opening] {
			continue
		}
		moved++
		if before[opening] != "replica-5" {
			t.Errorf("%s moved from %s to %s, but neither replica left the fleet", opening, before[opening], after[opening])
		}
	}
	if moved == 0 {
		t.Fatal("no prompt moved when a replica left, so nothing hashed to it")
	}
	if share := float64(moved) / float64(len(names)); share > 0.34 {
		t.Errorf("%.0f%% of prompts rehashed when one replica of six left, which is not a minimal redistribution", share*100)
	}
}

// How balanced this policy leaves the fleet is one of the figures it is compared
// on, so the load it weighed has to be a figure the rows can show.
func TestTheHashDecisionRecordsTheLoadItWeighed(t *testing.T) {
	p := policy.NewPrefixHash(hashPoint(1000))
	state := sixReplicas()
	for i := range state.Replicas {
		state.Replicas[i].Inflight = 3 + i
	}

	choice, err := p.Choose(policy.Request{Body: openingOf("recorded", "")}, state)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	chosen, present := state.Candidate(choice.Replica.ID)
	if !present {
		t.Fatalf("the policy chose %s, which is not in the fleet it was given", choice.Replica.ID)
	}
	if choice.Inflight != chosen.Inflight {
		t.Errorf("the decision records %d inflight on %s, which was carrying %d", choice.Inflight, choice.Replica.ID, chosen.Inflight)
	}
	// It consults no index, so it predicts no prefix match. A figure here would
	// be a belief this policy is defined by not holding.
	if choice.PrefixMatchBytes != 0 || choice.PrefixMatchTokens != 0 {
		t.Errorf("the decision claims a prefix match of %d bytes and %d tokens, and this policy holds no index to claim one from",
			choice.PrefixMatchBytes, choice.PrefixMatchTokens)
	}
}

func TestPrefixHashWithNoReplicasCannotChoose(t *testing.T) {
	p := policy.NewPrefixHash(hashPoint(4))
	if _, err := p.Choose(policy.Request{Body: openingOf("nowhere", "")}, fleet.State{}); err == nil {
		t.Error("an empty fleet produced a choice, so a request would be dispatched to nothing")
	}
}

// The router routes concurrently, and this policy is shared between every
// request in flight.
func TestPrefixHashIsSafeUnderConcurrentUse(t *testing.T) {
	p := policy.NewPrefixHash(hashPoint(4))
	state := sixReplicas()

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				body := openingOf(fmt.Sprintf("worker-%d-%d", worker, i), "")
				if _, err := p.Choose(policy.Request{Body: body}, state); err != nil {
					t.Errorf("choose: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// The walk of the ring is on the request path, so it is held to the same budget
// the index lookup is.
func TestTheRingWalkStaysInsideTheRouterOverheadBudget(t *testing.T) {
	p := policy.NewPrefixHash(policy.HashPoint{LeadingBlocks: 16, HashWeight: 4})
	state := sixReplicas()
	bodies := make([][]byte, 0, 200)
	for _, opening := range openings(200) {
		bodies = append(bodies, openingOf(opening, strings.Repeat("tail ", 200)))
	}

	started := time.Now()
	for _, body := range bodies {
		if _, err := p.Choose(policy.Request{Body: body}, state); err != nil {
			t.Fatalf("choose: %v", err)
		}
	}
	mean := time.Since(started) / time.Duration(len(bodies))
	if mean > policy.DecisionBudget {
		t.Errorf("a stateless hash decision averaged %v, past the %v budget", mean, policy.DecisionBudget)
	}
	t.Logf("stateless prefix-hash decision: %v mean", mean)
}

// The window is a judgement and not a measurement, so it is stated rather than
// defaulted: any number here would be OpenAI's "initial tokens" given a value
// nobody has published.
func TestByNameNeedsAStatedWindowForPrefixHash(t *testing.T) {
	if _, err := policy.ByName(policy.PrefixHashName, policy.Options{}); err == nil {
		t.Error("the stateless hash was built with no window, so its cells would state a number nothing set")
	}
	if _, err := policy.ByName(policy.PrefixHashName, policy.Options{HashPoint: policy.HashPoint{LeadingBlocks: -1}}); err == nil {
		t.Error("a negative window was accepted")
	}
	if _, err := policy.ByName(policy.PrefixHashName, policy.Options{HashPoint: policy.HashPoint{LeadingBlocks: 16, HashWeight: -1}}); err == nil {
		t.Error("a negative weight was accepted, which would prefer the replicas the hash ranked last")
	}
	p, err := policy.ByName(policy.PrefixHashName, policy.Options{HashPoint: policy.HashPoint{LeadingBlocks: 16, HashWeight: 4}})
	if err != nil {
		t.Fatalf("ByName: %v", err)
	}
	if p.Name() != policy.PrefixHashName {
		t.Errorf("the policy reports its name as %q, so its cells would be labelled wrong", p.Name())
	}
	tuned, ok := p.(policy.HashTuned)
	if !ok {
		t.Fatal("the stateless hash does not report its grid point, so a sweep could label cells with a point that never ran")
	}
	if got := tuned.HashTunables(); got.LeadingBlocks != 16 || got.HashWeight != 4 {
		t.Errorf("it reports the point %v, and it is running 16 blocks at a weight of 4", got)
	}
}

// A policy with no hash point must not be handed one, for the reason a policy
// with no spill rule must not be handed thresholds: a flag nothing reads is a
// run labelled with a configuration it did not have.
func TestOnlyTheStatelessHashCarriesAHashPoint(t *testing.T) {
	for _, name := range []string{policy.RoundRobinName, policy.LeastOutstandingName, policy.SessionAffinityName} {
		p, err := policy.ByName(name, policy.Options{})
		if err != nil {
			t.Fatalf("ByName(%s): %v", name, err)
		}
		if _, tuned := p.(policy.HashTuned); tuned {
			t.Errorf("%s reports a hash point, and it does not route on one", name)
		}
	}
}

// It is deliberately outside idea.md §5's five, and it is the bottom rung of the
// ladder #24 tops: none, then believed, then exact. The table reads in that
// order.
func TestTheLadderReadsFromNoResidencyKnowledgeToExact(t *testing.T) {
	ladder := []string{policy.PrefixHashName, policy.PrefixAffinityName, policy.ExactResidencyName}
	at := func(name string) int {
		for i, n := range policy.Order {
			if n == name {
				return i
			}
		}
		t.Fatalf("%s is not in the comparison order, so its cells would sort after the policies that are", name)
		return 0
	}
	for i := 1; i < len(ladder); i++ {
		if at(ladder[i-1]) >= at(ladder[i]) {
			t.Errorf("%s sorts after %s, and the ladder runs none -> believed -> exact", ladder[i-1], ladder[i])
		}
	}
}

// forSessionBody is a request that carries both a session and a body, which the
// router resolves for every policy alike. This one must ignore the session.
func forSessionBody(id string, body []byte) policy.Request {
	return policy.Request{Session: session.Session{ID: id}, Body: body}
}

// No per-session state, which is what makes it the control for what the index
// buys: the same prompt under two session ids is one key.
func TestTheSessionIdChangesNothing(t *testing.T) {
	p := policy.NewPrefixHash(hashPoint(4))
	state := sixReplicas()
	body := openingOf("indifferent", "")

	first, err := p.Choose(forSessionBody("chat-1", body), state)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	second, err := p.Choose(forSessionBody("chat-2", body), state)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	if first.Replica.ID != second.Replica.ID {
		t.Errorf("one prompt under two session ids went to %s and %s, so the policy is reading the session",
			first.Replica.ID, second.Replica.ID)
	}
}
