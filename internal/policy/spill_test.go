package policy_test

import (
	"testing"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// spilling is the grid's centre point, used wherever a test needs a spill rule
// that is on and does not care which threshold fires.
var spilling = policy.Spill{KVHighWater: 0.80, LoadImbalanceFactor: 2.0}

// seed teaches the index that one replica holds a conversation, by routing its
// opening turn to a fleet of exactly that replica.
func seed(t *testing.T, p *policy.PrefixAffinity, state fleet.State, id, conversationID string, turns int) {
	t.Helper()
	only, present := state.Candidate(id)
	if !present {
		t.Fatalf("no replica %s to seed", id)
	}
	for turn := range turns + 1 {
		if _, err := p.Choose(policy.Request{Body: conversation(conversationID, turn)},
			fleet.State{Replicas: []fleet.Candidate{only}}); err != nil {
			t.Fatalf("seeding %s: %v", id, err)
		}
	}
}

func read(fraction float64) vllmmetrics.KVUtilization {
	return vllmmetrics.KVUtilization{Fraction: fraction, Read: true}
}

// The first spill condition: the best match is out of cache room, so the
// affinity is declined and the request goes to the least loaded replica.
func TestAffinityIsDeclinedWhenTheBestMatchIsOverTheHighWaterMark(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 3)
	seed(t, p, state, "replica-0", "hot", 2)

	state.Replicas[0].KV = read(0.91)
	state.Replicas[1].KV, state.Replicas[2].KV = read(0.10), read(0.10)
	state.Replicas[1].Inflight, state.Replicas[2].Inflight = 4, 1

	got, err := p.Choose(policy.Request{Body: conversation("hot", 2)}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Replica.ID != "replica-2" {
		t.Errorf("the request went to %s, want the least loaded replica-2", got.Replica.ID)
	}
	if got.Reason != policy.ReasonSpillKV {
		t.Errorf("reason = %q, want %q", got.Reason, policy.ReasonSpillKV)
	}
}

// The second spill condition: the best match is buried under load relative to
// the rest of the fleet, whatever its cache is doing.
func TestAffinityIsDeclinedWhenTheBestMatchIsBuriedUnderLoad(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 3)
	seed(t, p, state, "replica-0", "busy", 2)

	// Every replica has cache room, so nothing but load can fire here.
	for i := range state.Replicas {
		state.Replicas[i].KV = read(0.20)
	}
	state.Replicas[0].Inflight = 21
	state.Replicas[1].Inflight, state.Replicas[2].Inflight = 12, 10

	got, err := p.Choose(policy.Request{Body: conversation("busy", 2)}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Replica.ID != "replica-2" {
		t.Errorf("the request went to %s, want the least loaded replica-2", got.Replica.ID)
	}
	if got.Reason != policy.ReasonSpillLoad {
		t.Errorf("reason = %q, want %q", got.Reason, policy.ReasonSpillLoad)
	}
}

// The two conditions are separate columns in the decision mix, so a grid point
// has to be able to say which of them its spills came from. A replica that is
// both out of cache and buried counts once, under the condition the rule
// actually stopped at.
func TestTheTwoSpillConditionsAreNeverCollapsedIntoOne(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "both", 2)

	state.Replicas[0].KV, state.Replicas[0].Inflight = read(0.95), 40
	state.Replicas[1].KV, state.Replicas[1].Inflight = read(0.10), 1

	got, _ := p.Choose(policy.Request{Body: conversation("both", 2)}, state)
	if got.Reason != policy.ReasonSpillKV {
		t.Errorf("reason = %q, want %q: KV pressure is checked first and a request cannot spill twice", got.Reason, policy.ReasonSpillKV)
	}
}

// A spill is not a cold request. The index had a match and the rule declined
// it, which is a different thing from there being nothing to decline — and the
// difference is what says whether a grid point is spilling too eagerly or
// whether the index simply is not finding anything.
func TestARequestWithNoMatchIsColdRatherThanADeclinedAffinity(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 2)
	for i := range state.Replicas {
		state.Replicas[i].KV = read(0.99)
		state.Replicas[i].Inflight = 40 * (i + 1)
	}

	got, err := p.Choose(policy.Request{Body: conversation("unseen", 0)}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Reason != policy.ReasonCold {
		t.Errorf("reason = %q, want %q: nothing was declined, because nothing was believed", got.Reason, policy.ReasonCold)
	}
}

// The prefix match on a spilled row is the one the request actually got, not
// the one it gave up. The row's match is the router's prediction of the
// engine's prefix cache hit for the replica it was sent to, and a spilled row
// carrying the declined replica's match would predict a hit on a replica that
// never saw the request — which is precisely the divergence measurement #17
// draws off this field.
func TestASpilledRowPredictsTheReplicaItWasActuallySentTo(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "given-up", 3)

	state.Replicas[0].KV, state.Replicas[1].KV = read(0.99), read(0.10)

	got, _ := p.Choose(policy.Request{Body: conversation("given-up", 3)}, state)
	if got.Replica.ID != "replica-1" {
		t.Fatalf("the request went to %s, want the spill target replica-1", got.Replica.ID)
	}
	if got.PrefixMatchBytes != 0 {
		t.Errorf("a spilled row predicts %d bytes on a replica that holds none of it", got.PrefixMatchBytes)
	}
	if got.DeclinedMatchBytes <= 0 {
		t.Errorf("the row does not say what the spill cost: declined %d bytes", got.DeclinedMatchBytes)
	}
}

// The KV utilization the decision was weighed against travels on the decision,
// for the same reason inflight does: a grid table claiming one high-water mark
// spilled more than another rests on nothing the rows can show otherwise.
func TestTheDecisionRecordsThePressureItWasMadeAgainst(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "recorded", 1)
	state.Replicas[0].KV, state.Replicas[1].KV = read(0.55), read(0.11)

	got, _ := p.Choose(policy.Request{Body: conversation("recorded", 1)}, state)
	if got.Replica.ID != "replica-0" {
		t.Fatalf("went to %s, want the affinity replica-0", got.Replica.ID)
	}
	if !got.KV.Read || got.KV.Fraction != 0.55 {
		t.Errorf("the decision recorded %+v, want the 0.55 it was actually made against", got.KV)
	}
}

// Graceful degradation. A fleet whose scrapes have stopped answering routes as
// though the KV signal did not exist, rather than spilling every request on a
// reading nobody took. The load condition still fires: it is counted locally
// and does not depend on a scrape at all.
func TestAnUnscrapedFleetStillTakesAffinityAndStillSpillsOnLoad(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "unscraped", 1)

	got, _ := p.Choose(policy.Request{Body: conversation("unscraped", 1)}, state)
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: an unread replica must not fire a KV spill", got.Reason, policy.ReasonPrefixAffinity)
	}

	state.Replicas[0].Inflight, state.Replicas[1].Inflight = 30, 10
	got, _ = p.Choose(policy.Request{Body: conversation("unscraped", 1)}, state)
	if got.Reason != policy.ReasonSpillLoad {
		t.Errorf("reason = %q, want %q: inflight is counted locally and needs no scrape", got.Reason, policy.ReasonSpillLoad)
	}
}

// An idle fleet has a minimum inflight of zero, and a factor times zero is
// zero. Read literally, the rule would then decline every affinity to a replica
// holding a single request whenever any sibling was idle, which is the whole
// bottom of the concurrency sweep. The floor of one is what keeps the low end
// of the sweep measuring prefix affinity rather than least-outstanding.
func TestAnIdleFleetDoesNotSpillEverySecondRequest(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 3)
	seed(t, p, state, "replica-0", "quiet", 1)

	state.Replicas[0].Inflight = 2 // the fleet minimum is 0
	got, _ := p.Choose(policy.Request{Body: conversation("quiet", 1)}, state)
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: two requests on an otherwise idle fleet is not an imbalance", got.Reason, policy.ReasonPrefixAffinity)
	}

	state.Replicas[0].Inflight = 3 // past 2.0 x the floor of one
	got, _ = p.Choose(policy.Request{Body: conversation("quiet", 1)}, state)
	if got.Reason != policy.ReasonSpillLoad {
		t.Errorf("reason = %q, want %q", got.Reason, policy.ReasonSpillLoad)
	}
}

// The zero value is no spill rule at all, which is the policy #15 measured. A
// threshold that defaulted itself on would silently change what the
// four-policy comparison means without a single cell saying so.
func TestTheZeroSpillIsThePolicyThatWasMeasuredWithoutOne(t *testing.T) {
	if spill := (policy.Spill{}); spill.Enabled() {
		t.Fatal("the zero Spill is enabled")
	}
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{})
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "unspilled", 1)

	state.Replicas[0].KV, state.Replicas[0].Inflight = read(0.99), 64
	state.Replicas[1].KV, state.Replicas[1].Inflight = read(0.01), 0

	got, _ := p.Choose(policy.Request{Body: conversation("unspilled", 1)}, state)
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: an unconfigured spill rule must not fire", got.Reason, policy.ReasonPrefixAffinity)
	}
}

// Each threshold stands alone, so the grid can hold one fixed while it moves
// the other and attribute what changed to the axis that moved.
func TestEitherThresholdCanBeSweptWithTheOtherOff(t *testing.T) {
	state := fleetOf(t, 2)

	kvOnly := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{KVHighWater: 0.80})
	seed(t, kvOnly, state, "replica-0", "kv-only", 1)
	state.Replicas[0].KV, state.Replicas[0].Inflight = read(0.20), 64
	state.Replicas[1].KV, state.Replicas[1].Inflight = read(0.20), 0
	if got, _ := kvOnly.Choose(policy.Request{Body: conversation("kv-only", 1)}, state); got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: the load threshold is off", got.Reason, policy.ReasonPrefixAffinity)
	}

	loadOnly := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{LoadImbalanceFactor: 2.0})
	seed(t, loadOnly, state, "replica-0", "load-only", 1)
	state.Replicas[0].KV, state.Replicas[0].Inflight = read(0.99), 0
	state.Replicas[1].KV, state.Replicas[1].Inflight = read(0.99), 0
	if got, _ := loadOnly.Choose(policy.Request{Body: conversation("load-only", 1)}, state); got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: the KV threshold is off", got.Reason, policy.ReasonPrefixAffinity)
	}
}

func TestAnImpossibleThresholdIsRefusedRatherThanRun(t *testing.T) {
	for _, spill := range []policy.Spill{
		{KVHighWater: 1.5},
		{KVHighWater: -0.2},
		{LoadImbalanceFactor: 0.5},
	} {
		if err := spill.Validate(); err == nil {
			t.Errorf("%+v was accepted", spill)
		}
	}
	if err := spilling.Validate(); err != nil {
		t.Errorf("the grid's centre point was refused: %v", err)
	}
}

// A spill needs somewhere to go. The least loaded replica in the fleet can be
// the very replica the rule just declined — it is over its high-water mark and
// also the quietest, which is an ordinary state for a fleet whose cache is full
// and whose traffic has moved on. Sending the request back there under a spill
// reason would put a decision in the mix that never happened.
func TestASpillWithNowhereToGoIsNotASpill(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)

	// The declined replica is also the idlest, so least-outstanding would
	// choose it again.
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "nowhere", 1)
	state.Replicas[0].KV, state.Replicas[0].Inflight = read(0.95), 0
	state.Replicas[1].KV, state.Replicas[1].Inflight = read(0.10), 8

	got, _ := p.Choose(policy.Request{Body: conversation("nowhere", 1)}, state)
	if got.Replica.ID != "replica-1" || got.Reason != policy.ReasonSpillKV {
		t.Errorf("went to %s as %q, want replica-1 as %q: the declined replica is not its own spill target",
			got.Replica.ID, got.Reason, policy.ReasonSpillKV)
	}

	// A fleet of one has no elsewhere at all, so the match is taken rather than
	// declined into thin air.
	alone := fleet.State{Replicas: []fleet.Candidate{state.Replicas[0]}}
	got, _ = p.Choose(policy.Request{Body: conversation("nowhere", 1)}, alone)
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: there is nowhere to spill to", got.Reason, policy.ReasonPrefixAffinity)
	}
}

// A KV spill has to relieve the pressure it fired on. Ties on prefix match are
// the common case in this workload, and under memory pressure a whole fleet
// crosses its high-water mark together — so the least loaded replica other than
// the declined one is very often another replica equally out of cache room.
// Spilling there declines a match, pays a prefill and relieves nothing, while
// recording a spill that did no work in the column the grid is read from.
func TestAKVSpillDoesNotLandOnAReplicaEquallyOutOfCache(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{KVHighWater: 0.80})
	state := fleetOf(t, 3)

	// Two replicas hold the conversation and both are over the mark; the third
	// has cache room and is the only useful target.
	seed(t, p, state, "replica-0", "pressure", 2)
	seed(t, p, state, "replica-1", "pressure", 2)
	state.Replicas[0].KV, state.Replicas[1].KV = read(0.95), read(0.93)
	state.Replicas[2].KV = read(0.20)
	state.Replicas[2].Inflight = 6 // busier, and still the right target

	got, err := p.Choose(policy.Request{Body: conversation("pressure", 2)}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Replica.ID != "replica-2" {
		t.Errorf("a KV spill went to %s at %.0f%% KV, want the replica with cache room",
			got.Replica.ID, got.KV.Fraction*100)
	}
	if got.Reason != policy.ReasonSpillKV {
		t.Errorf("reason = %q, want %q", got.Reason, policy.ReasonSpillKV)
	}
}

// When the whole fleet is over the high-water mark there is no relief to be
// had, and declining the match buys nothing while costing the locality. The
// request keeps its match and the mix does not record a spill that relieved
// nothing.
func TestAFleetEntirelyOverTheMarkKeepsItsMatchRatherThanChurning(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{KVHighWater: 0.80})
	state := fleetOf(t, 3)
	seed(t, p, state, "replica-0", "saturated", 2)
	for i := range state.Replicas {
		state.Replicas[i].KV = read(0.95)
	}

	got, _ := p.Choose(policy.Request{Body: conversation("saturated", 2)}, state)
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: every replica is over the mark, so there is nothing to spill to",
			got.Reason, policy.ReasonPrefixAffinity)
	}
	if got.Replica.ID != "replica-0" {
		t.Errorf("went to %s, want the replica holding the match", got.Replica.ID)
	}
}

// The load condition needs no such guard and must not acquire one: the target
// is the fleet minimum, and a minimum can never be more than a factor of at
// least one above itself. An unscraped fleet still spills on load.
func TestALoadSpillTargetsTheMinimumWhateverItsCacheSays(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{LoadImbalanceFactor: 2.0})
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "loaded", 1)

	// The only escape is over the mark, but this spill is not about cache.
	state.Replicas[0].KV, state.Replicas[0].Inflight = read(0.10), 30
	state.Replicas[1].KV, state.Replicas[1].Inflight = read(0.99), 10

	got, _ := p.Choose(policy.Request{Body: conversation("loaded", 1)}, state)
	if got.Replica.ID != "replica-1" || got.Reason != policy.ReasonSpillLoad {
		t.Errorf("went to %s as %q, want replica-1 as %q", got.Replica.ID, got.Reason, policy.ReasonSpillLoad)
	}
}

// A spill row records the pressure on the replica it DECLINED, not only on the
// one it chose.
//
// The run of 2026-09-10 could not explain its own KV axis because of this. Every
// row carried the target's utilization -- the target being, by construction, a
// replica under the mark -- so the column's maximum was 0.697 while a threshold
// of 0.70 was demonstrably firing. The figure that triggered the decision was
// the only one not written down.
func TestASpillRecordsThePressureItDeclined(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{KVHighWater: 0.80})
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "declined-kv", 2)
	state.Replicas[0].KV = read(0.93)
	state.Replicas[1].KV = read(0.11)

	got, err := p.Choose(policy.Request{Body: conversation("declined-kv", 2)}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Reason != policy.ReasonSpillKV {
		t.Fatalf("reason = %q, want %q", got.Reason, policy.ReasonSpillKV)
	}
	if !got.KV.Read || got.KV.Fraction != 0.11 {
		t.Errorf("KV = %+v, want the target's 0.11", got.KV)
	}
	if !got.DeclinedKV.Read || got.DeclinedKV.Fraction != 0.93 {
		t.Errorf("DeclinedKV = %+v, want the declined replica's 0.93 — the figure the rule fired on", got.DeclinedKV)
	}
}

// A load spill records it too, and a decision that declined nothing records no
// declined pressure rather than a zero that reads as an empty cache.
func TestOnlyASpillRecordsDeclinedPressure(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{LoadImbalanceFactor: 2.0})
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "declined-load", 1)
	state.Replicas[0].KV, state.Replicas[0].Inflight = read(0.42), 30
	state.Replicas[1].KV, state.Replicas[1].Inflight = read(0.10), 10

	got, _ := p.Choose(policy.Request{Body: conversation("declined-load", 1)}, state)
	if got.Reason != policy.ReasonSpillLoad {
		t.Fatalf("reason = %q, want %q", got.Reason, policy.ReasonSpillLoad)
	}
	if got.DeclinedInflight != 30 {
		t.Errorf("DeclinedInflight = %d, want the 30 the rule fired on", got.DeclinedInflight)
	}
	if !got.DeclinedKV.Read || got.DeclinedKV.Fraction != 0.42 {
		t.Errorf("DeclinedKV = %+v, want 0.42", got.DeclinedKV)
	}

	// An affinity declines nothing, so it reports no declined pressure at all.
	state.Replicas[0].Inflight = 10
	got, _ = p.Choose(policy.Request{Body: conversation("declined-load", 1)}, state)
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Fatalf("reason = %q, want %q", got.Reason, policy.ReasonPrefixAffinity)
	}
	if got.DeclinedKV.Read || got.DeclinedInflight != 0 {
		t.Errorf("an affinity reported declined pressure %+v / %d", got.DeclinedKV, got.DeclinedInflight)
	}
}
