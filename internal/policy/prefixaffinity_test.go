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

// prefixIndex is an index sized for a test rather than for the fleet. The
// bounds a real one carries are measurements (prefix.Calibration); here they
// only have to be out of the way.
func prefixIndex(t *testing.T) *prefix.Index {
	t.Helper()
	ix, err := prefix.New(prefix.Config{NodeCap: 4096, TTL: time.Hour})
	if err != nil {
		t.Fatalf("prefix.New: %v", err)
	}
	return ix
}

// conversation renders a turn of a chat request whose bytes grow as the
// conversation does, so that turn N shares a leading run with turn N-1 exactly
// as a real multi-turn prompt does.
func conversation(session string, turn int) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `{"model":"llama","messages":[{"role":"user","content":%q}`, strings.Repeat(session+" opening ", 20))
	for i := range turn {
		fmt.Fprintf(&b, `,{"role":"assistant","content":%q},{"role":"user","content":%q}`,
			strings.Repeat(fmt.Sprintf("reply %d ", i), 12), strings.Repeat(fmt.Sprintf("follow %d ", i), 12))
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func fleetOf(t *testing.T, n int) fleet.State {
	t.Helper()
	state := fleet.State{}
	for i := range n {
		state.Replicas = append(state.Replicas, fleet.Candidate{
			Replica: fleet.Replica{ID: fmt.Sprintf("replica-%d", i), BaseURL: fmt.Sprintf("http://127.0.0.1:800%d", i)},
		})
	}
	return state
}

// The mechanism the whole project is about: once a conversation has been served
// somewhere, its next turn goes back to the replica already holding its prefix.
func TestALaterTurnGoesBackToTheReplicaHoldingItsPrefix(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t))
	state := fleetOf(t, 6)

	first, err := p.Choose(policy.Request{Body: conversation("alpha", 0)}, state)
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if first.Reason != policy.ReasonCold {
		t.Errorf("the first turn of a conversation was reason %q, want %q", first.Reason, policy.ReasonCold)
	}

	for turn := 1; turn < 5; turn++ {
		got, err := p.Choose(policy.Request{Body: conversation("alpha", turn)}, state)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		if got.Replica.ID != first.Replica.ID {
			t.Errorf("turn %d went to %s, want %s — the replica holding the prefix", turn, got.Replica.ID, first.Replica.ID)
		}
		if got.Reason != policy.ReasonPrefixAffinity {
			t.Errorf("turn %d was reason %q, want %q", turn, got.Reason, policy.ReasonPrefixAffinity)
		}
		if got.PrefixMatchBytes <= 0 {
			t.Errorf("turn %d took affinity on a prefix match of %dB", turn, got.PrefixMatchBytes)
		}
	}
}

// The match is a prediction, and the decision has to carry it: without the
// figure on the row there is nothing to plot the engine's actual prefill
// against, which is the measurement this index exists to make possible.
func TestTheDecisionCarriesThePrefixMatchItWasMadeOn(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t))
	state := fleetOf(t, 6)

	cold, _ := p.Choose(policy.Request{Body: conversation("beta", 0)}, state)
	if cold.PrefixMatchBytes != 0 {
		t.Errorf("a conversation nobody had seen carried a %dB match", cold.PrefixMatchBytes)
	}

	warm, _ := p.Choose(policy.Request{Body: conversation("beta", 0)}, state)
	if warm.PrefixMatchBytes == 0 {
		t.Fatal("a repeated prompt carried no match")
	}
	// The match is a length of the prompt, so it cannot exceed it, and it is
	// counted in whole blocks.
	if body := len(conversation("beta", 0)); warm.PrefixMatchBytes > body {
		t.Errorf("match of %dB against a prompt of %dB", warm.PrefixMatchBytes, body)
	}
	if warm.PrefixMatchBytes%prefix.BlockBytes != 0 {
		t.Errorf("match of %dB is not a whole number of %dB blocks", warm.PrefixMatchBytes, prefix.BlockBytes)
	}
}

// A conversation nobody holds is cold, and cold goes to the least loaded
// replica. Routing it by anything else would be inventing a preference: there is
// no cache to preserve, so the only signal left is load.
func TestAColdRequestGoesToTheLeastLoadedReplica(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t))
	state := fleetOf(t, 3)
	state.Replicas[0].Inflight = 9
	state.Replicas[1].Inflight = 4
	state.Replicas[2].Inflight = 1

	got, err := p.Choose(policy.Request{Body: conversation("gamma", 0)}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Replica.ID != "replica-2" {
		t.Errorf("a cold request went to %s at %d inflight, want the least loaded", got.Replica.ID, got.Inflight)
	}
	if got.Reason != policy.ReasonCold {
		t.Errorf("reason = %q, want %q", got.Reason, policy.ReasonCold)
	}
}

// This policy is deliberately blind to load once it has a match — declining
// affinity under pressure is the spill rule, which is a separate piece of work
// with its own thresholds and its own reasons. Without this, the comparison the
// project rests on would be measuring two changes at once.
func TestAffinityIsTakenRegardlessOfLoadUntilSpillExists(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t))
	state := fleetOf(t, 3)

	first, _ := p.Choose(policy.Request{Body: conversation("delta", 0)}, state)

	// The replica holding the prefix is now buried, and every sibling is idle.
	for i := range state.Replicas {
		if state.Replicas[i].ID == first.Replica.ID {
			state.Replicas[i].Inflight = 64
		}
	}
	got, _ := p.Choose(policy.Request{Body: conversation("delta", 1)}, state)
	if got.Replica.ID != first.Replica.ID {
		t.Errorf("affinity was declined under load, which is the spill rule and not this policy: went to %s", got.Replica.ID)
	}
	if got.Inflight != 64 {
		t.Errorf("the decision recorded %d inflight, want the 64 it was actually made against", got.Inflight)
	}
}

// Two conversations that branch from a common ancestor share their opening under
// different session ids. A hash scatters them; the index finds them. idea.md §5
// names this as one of the places policy 4 should separate from policy 3, so it
// is tested rather than assumed.
func TestBranchedConversationsFindTheirCommonAncestor(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t))
	state := fleetOf(t, 6)

	root := conversation("epsilon", 2)
	first, _ := p.Choose(policy.Request{Body: root, Session: session.Session{ID: "epsilon"}}, state)

	// A branch: the same history so far, continuing differently, under a session
	// id that shares nothing with the first.
	branch := append(root[:len(root)-2], []byte(`,{"role":"user","content":"a different continuation entirely"}]}`)...)
	got, err := p.Choose(policy.Request{Body: branch, Session: session.Session{ID: "zeta"}}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Replica.ID != first.Replica.ID {
		t.Errorf("a branch of the conversation went to %s and its ancestor to %s", got.Replica.ID, first.Replica.ID)
	}
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("a branch was reason %q, want %q", got.Reason, policy.ReasonPrefixAffinity)
	}
}

// The index believes what the router dispatched. A replica that has left the
// fleet is not a candidate however good its match, and the request falls to the
// next best rather than being dropped.
func TestAMatchOnAReplicaThatHasLeftFallsToTheNextBest(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t))
	full := fleetOf(t, 3)

	first, _ := p.Choose(policy.Request{Body: conversation("eta", 0)}, full)

	// The fleet loses the replica that took it.
	var remaining fleet.State
	for _, c := range full.Replicas {
		if c.ID != first.Replica.ID {
			remaining.Replicas = append(remaining.Replicas, c)
		}
	}
	got, err := p.Choose(policy.Request{Body: conversation("eta", 1)}, remaining)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Replica.ID == first.Replica.ID {
		t.Errorf("the request went to %s, which is no longer in the fleet", got.Replica.ID)
	}
	if got.Reason != policy.ReasonCold {
		t.Errorf("reason = %q, want %q: no remaining replica held anything", got.Reason, policy.ReasonCold)
	}
}

// An empty fleet is the one case with no answer, and it is a drop rather than a
// panic.
func TestAnEmptyFleetIsADropRatherThanAChoice(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t))
	if _, err := p.Choose(policy.Request{Body: conversation("theta", 0)}, fleet.State{}); err == nil {
		t.Error("a request was routed to an empty fleet")
	}
}

// A prompt too short to fill one block has no chain to match on, and is routed
// on load like any other cold request rather than treated as an error.
func TestAPromptTooShortToChunkIsRoutedOnLoad(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t))
	state := fleetOf(t, 3)
	state.Replicas[1].Inflight = 7

	got, err := p.Choose(policy.Request{Body: []byte(`{"messages":[]}`)}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Reason != policy.ReasonCold {
		t.Errorf("reason = %q, want %q", got.Reason, policy.ReasonCold)
	}
	if got.Replica.ID == "replica-1" {
		t.Error("a cold request went to the busiest replica")
	}
}

// The router is the sole ingress to six replicas and handles requests
// concurrently. Run under -race.
func TestThePolicyIsSafeUnderConcurrentUse(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t))
	state := fleetOf(t, 6)

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for turn := range 40 {
				if _, err := p.Choose(policy.Request{Body: conversation(fmt.Sprintf("s%d", worker), turn%6)}, state); err != nil {
					t.Errorf("Choose: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// The index lookup happens on every request, inside the figure §4.1 reports as
// the router's own cost. A sub-millisecond number there is a claim the project
// makes, so the lookup is measured against it over an index the size the fleet
// calibrates to rather than an empty one.
func TestTheIndexLookupStaysInsideTheRouterOverheadBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	ix, err := prefix.New(prefix.Config{NodeCap: 45_000, TTL: time.Hour})
	if err != nil {
		t.Fatalf("prefix.New: %v", err)
	}
	p := policy.NewPrefixAffinity(ix)
	state := fleetOf(t, 6)

	// Fill the index to its cap, the state the router runs in for all but the
	// opening minutes of a cell.
	for session := range 3000 {
		if _, err := p.Choose(policy.Request{Body: conversation(fmt.Sprintf("fill-%d", session), 3)}, state); err != nil {
			t.Fatalf("filling the index: %v", err)
		}
	}
	if ix.Len() < 10_000 {
		t.Fatalf("the index holds %d nodes, which is not the loaded state this is meant to measure", ix.Len())
	}

	body := conversation("measured", 4)
	const runs = 2000
	started := time.Now()
	for range runs {
		if _, err := p.Choose(policy.Request{Body: body}, state); err != nil {
			t.Fatalf("Choose: %v", err)
		}
	}
	mean := time.Since(started) / runs

	// The budget is the router's whole accept-to-dispatch cost, and the routing
	// decision is only part of it. Measured against the mean rather than a tail,
	// because a test that failed on one scheduling hiccup would be turned off.
	if mean > policy.DecisionBudget {
		t.Errorf("a routing decision over a full index averaged %v, past the %v budget", mean, policy.DecisionBudget)
	}
	t.Logf("prefix-affinity decision over %d nodes: %v mean", ix.Len(), mean)
}

// Every policy the comparison reports has to be runnable, and prefix affinity
// needs an index it cannot invent for itself.
func TestByNameNeedsACalibratedIndexForPrefixAffinity(t *testing.T) {
	if _, err := policy.ByName(policy.PrefixAffinityName, policy.Options{}); err == nil {
		t.Error("prefix affinity was built with no index, so it would route on beliefs about nothing")
	}
	p, err := policy.ByName(policy.PrefixAffinityName, policy.Options{PrefixIndex: prefixIndex(t)})
	if err != nil {
		t.Fatalf("ByName: %v", err)
	}
	if p.Name() != policy.PrefixAffinityName {
		t.Errorf("policy reports its name as %q, so its cells would be labelled wrong", p.Name())
	}
}

// Prefix affinity is policy 4 and it goes last: the comparison table puts the
// baselines in the columns before it, whichever order the runs happened in.
func TestPrefixAffinityIsComparedAfterTheBaselines(t *testing.T) {
	if got := policy.Order[len(policy.Order)-1]; got != policy.PrefixAffinityName {
		t.Errorf("the last policy compared is %q, want %q", got, policy.PrefixAffinityName)
	}
	if len(policy.Order) != 4 {
		t.Errorf("the comparison covers %d policies, want the four idea.md §5 numbers", len(policy.Order))
	}
}

// Several replicas commonly hold the same leading run — every request of this
// workload shares its chat envelope, and a configurable fraction of sessions
// share a system prompt. Taking the index's own deterministic order there would
// send every one of those requests to whichever replica sorts first, which
// idea.md §5 calls out as strictly worse than scattering them.
func TestReplicasTiedOnMatchAreSeparatedByLoad(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t))
	state := fleetOf(t, 3)

	// Teach every replica the same opening, by routing it while each in turn is
	// the only one available.
	shared := conversation("shared", 0)
	for i := range state.Replicas {
		only := fleet.State{Replicas: []fleet.Candidate{state.Replicas[i]}}
		if _, err := p.Choose(policy.Request{Body: shared}, only); err != nil {
			t.Fatalf("seeding %s: %v", state.Replicas[i].ID, err)
		}
	}

	// All three now hold it equally, and one of them is idle while the others
	// are buried. The idle one is the only sensible answer: the cache is a wash.
	state.Replicas[0].Inflight = 40
	state.Replicas[1].Inflight = 40
	state.Replicas[2].Inflight = 0

	got, err := p.Choose(policy.Request{Body: shared}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q — every replica holds this", got.Reason, policy.ReasonPrefixAffinity)
	}
	if got.Replica.ID != "replica-2" {
		t.Errorf("a tie between equal holders went to %s at %d inflight, want the idle replica-2", got.Replica.ID, got.Inflight)
	}
}

// A longer match still wins outright. Load separates equals; it never buys a
// shorter match, because that would be the spill rule and this policy does not
// have one.
func TestALongerMatchStillBeatsAnIdleReplica(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t))
	state := fleetOf(t, 2)

	// replica-0 learns the whole conversation; replica-1 only its opening.
	long := conversation("deep", 4)
	if _, err := p.Choose(policy.Request{Body: long},
		fleet.State{Replicas: []fleet.Candidate{state.Replicas[0]}}); err != nil {
		t.Fatalf("seeding replica-0: %v", err)
	}
	if _, err := p.Choose(policy.Request{Body: conversation("deep", 0)},
		fleet.State{Replicas: []fleet.Candidate{state.Replicas[1]}}); err != nil {
		t.Fatalf("seeding replica-1: %v", err)
	}

	state.Replicas[0].Inflight = 64
	state.Replicas[1].Inflight = 0

	got, err := p.Choose(policy.Request{Body: long}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Replica.ID != "replica-0" {
		t.Errorf("the deeper match was declined for an idle replica: went to %s. That is the spill rule, which #16 owns", got.Replica.ID)
	}
}
