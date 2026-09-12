package policy_test

import (
	"testing"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// spilling is the grid's centre point, used wherever a test needs a spill rule
// that is on and does not care which threshold fires.
var spilling = policy.Spill{HitRateLowWater: 0.20, LoadImbalanceFactor: 2.0}

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

// hitting is a replica whose prefix cache answered that share of the block
// queries in its window, over traffic enough for the reading to count.
func hitting(fraction float64) vllmmetrics.HitRate {
	return vllmmetrics.HitRate{Fraction: fraction, Read: true, Queries: 1000}
}

// The first spill condition: the best match's replica is answering too few of
// its block queries out of cache — it is evicting — so the affinity is declined
// and the request goes to the least loaded replica.
func TestAffinityIsDeclinedWhenTheBestMatchsCacheIsMissing(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 3)
	seed(t, p, state, "replica-0", "hot", 2)

	state.Replicas[0].HitRate = hitting(0.09)
	state.Replicas[1].HitRate, state.Replicas[2].HitRate = hitting(0.9), hitting(0.9)
	state.Replicas[1].Inflight, state.Replicas[2].Inflight = 4, 1

	got, err := p.Choose(policy.Request{Body: conversation("hot", 2)}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Replica.ID != "replica-2" {
		t.Errorf("the request went to %s, want the least loaded replica-2", got.Replica.ID)
	}
	if got.Reason != policy.ReasonSpillHitRate {
		t.Errorf("reason = %q, want %q", got.Reason, policy.ReasonSpillHitRate)
	}
}

// The second spill condition: the best match is buried under load relative to
// the rest of the fleet, whatever its cache is doing.
func TestAffinityIsDeclinedWhenTheBestMatchIsBuriedUnderLoad(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 3)
	seed(t, p, state, "replica-0", "busy", 2)

	// Every replica's cache is answering, so nothing but load can fire here.
	for i := range state.Replicas {
		state.Replicas[i].HitRate = hitting(0.8)
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
// both evicting and buried counts once, under the condition the rule actually
// stopped at.
//
// This test is why #28 existed: it passed throughout #16 and #18 while the two
// conditions were reading one pressure in two units, because nothing here says
// the two signals are independent. What says that is the measured correlation
// between them, which is a property of a run rather than of a unit test.
func TestTheTwoSpillConditionsAreNeverCollapsedIntoOne(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "both", 2)

	state.Replicas[0].HitRate, state.Replicas[0].Inflight = hitting(0.05), 40
	state.Replicas[1].HitRate, state.Replicas[1].Inflight = hitting(0.9), 1

	got, _ := p.Choose(policy.Request{Body: conversation("both", 2)}, state)
	if got.Reason != policy.ReasonSpillHitRate {
		t.Errorf("reason = %q, want %q: residency is checked first and a request cannot spill twice", got.Reason, policy.ReasonSpillHitRate)
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
		state.Replicas[i].HitRate = hitting(0.01)
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

	state.Replicas[0].HitRate, state.Replicas[1].HitRate = hitting(0.01), hitting(0.9)

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

// The honoured rate the decision was weighed against travels on the decision,
// for the same reason inflight does: a grid table claiming one low-water mark
// spilled more than another rests on nothing the rows can show otherwise.
func TestTheDecisionRecordsThePressureItWasMadeAgainst(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "recorded", 1)
	state.Replicas[0].HitRate, state.Replicas[1].HitRate = hitting(0.45), hitting(0.89)

	got, _ := p.Choose(policy.Request{Body: conversation("recorded", 1)}, state)
	if got.Replica.ID != "replica-0" {
		t.Fatalf("went to %s, want the affinity replica-0", got.Replica.ID)
	}
	if !got.HitRate.Read || got.HitRate.Fraction != 0.45 {
		t.Errorf("the decision recorded %v, want the 0.45 it was actually made against", got.HitRate)
	}
}

// Graceful degradation, and the acceptance criterion #28 states in as many
// words. A fleet nobody has scraped — a fresh router, a replica too quiet to
// clear the window's query floor, a scrape that stopped answering — routes as
// though the residency signal did not exist, rather than spilling every request
// on a reading nobody took. The load condition still fires: it is counted
// locally and depends on no scrape at all.
func TestAFleetWithNoScrapeStillTakesAffinityAndStillSpillsOnLoad(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "unscraped", 1)

	got, _ := p.Choose(policy.Request{Body: conversation("unscraped", 1)}, state)
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: an unread replica must not fire a residency spill", got.Reason, policy.ReasonPrefixAffinity)
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

	state.Replicas[0].HitRate, state.Replicas[0].Inflight = hitting(0.01), 64
	state.Replicas[1].HitRate, state.Replicas[1].Inflight = hitting(0.99), 0

	got, _ := p.Choose(policy.Request{Body: conversation("unspilled", 1)}, state)
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: an unconfigured spill rule must not fire", got.Reason, policy.ReasonPrefixAffinity)
	}
}

// Each threshold stands alone, so the grid can hold one fixed while it moves
// the other and attribute what changed to the axis that moved.
func TestEitherThresholdCanBeSweptWithTheOtherOff(t *testing.T) {
	state := fleetOf(t, 2)

	residencyOnly := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{HitRateLowWater: 0.20})
	seed(t, residencyOnly, state, "replica-0", "kv-only", 1)
	state.Replicas[0].HitRate, state.Replicas[0].Inflight = hitting(0.8), 64
	state.Replicas[1].HitRate, state.Replicas[1].Inflight = hitting(0.8), 0
	if got, _ := residencyOnly.Choose(policy.Request{Body: conversation("kv-only", 1)}, state); got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: the load threshold is off", got.Reason, policy.ReasonPrefixAffinity)
	}

	loadOnly := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{LoadImbalanceFactor: 2.0})
	seed(t, loadOnly, state, "replica-0", "load-only", 1)
	state.Replicas[0].HitRate, state.Replicas[0].Inflight = hitting(0.01), 0
	state.Replicas[1].HitRate, state.Replicas[1].Inflight = hitting(0.01), 0
	if got, _ := loadOnly.Choose(policy.Request{Body: conversation("load-only", 1)}, state); got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: the residency threshold is off", got.Reason, policy.ReasonPrefixAffinity)
	}
}

func TestAnImpossibleThresholdIsRefusedRatherThanRun(t *testing.T) {
	for _, spill := range []policy.Spill{
		{HitRateLowWater: 1.5},
		{HitRateLowWater: -0.2},
		{HitRateLowWater: 1},
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
// the very replica the rule just declined — it is evicting and also the
// quietest, which is an ordinary state for a fleet whose traffic has moved on.
// Sending the request back there under a spill reason would put a decision in
// the mix that never happened.
func TestASpillWithNowhereToGoIsNotASpill(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), spilling)

	// The declined replica is also the idlest, so least-outstanding would
	// choose it again.
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "nowhere", 1)
	state.Replicas[0].HitRate, state.Replicas[0].Inflight = hitting(0.05), 0
	state.Replicas[1].HitRate, state.Replicas[1].Inflight = hitting(0.9), 8

	got, _ := p.Choose(policy.Request{Body: conversation("nowhere", 1)}, state)
	if got.Replica.ID != "replica-1" || got.Reason != policy.ReasonSpillHitRate {
		t.Errorf("went to %s as %q, want replica-1 as %q: the declined replica is not its own spill target",
			got.Replica.ID, got.Reason, policy.ReasonSpillHitRate)
	}

	// A fleet of one has no elsewhere at all, so the match is taken rather than
	// declined into thin air.
	alone := fleet.State{Replicas: []fleet.Candidate{state.Replicas[0]}}
	got, _ = p.Choose(policy.Request{Body: conversation("nowhere", 1)}, alone)
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: there is nowhere to spill to", got.Reason, policy.ReasonPrefixAffinity)
	}
}

// A residency spill has to relieve the pressure it fired on. Ties on prefix
// match are the common case in this workload, and a fleet oversubscribed enough
// to evict is evicting everywhere at once — so the least loaded replica other
// than the declined one is very often another replica missing just as much.
// Spilling there declines a match, pays a prefill and relieves nothing, while
// recording a spill that did no work in the column the grid is read from.
func TestAResidencySpillDoesNotLandOnAReplicaWhoseCacheIsEquallyCold(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{HitRateLowWater: 0.20})
	state := fleetOf(t, 3)

	// Two replicas hold the conversation and both are under the mark; the third
	// is serving out of cache and is the only useful target.
	seed(t, p, state, "replica-0", "pressure", 2)
	seed(t, p, state, "replica-1", "pressure", 2)
	state.Replicas[0].HitRate, state.Replicas[1].HitRate = hitting(0.05), hitting(0.07)
	state.Replicas[2].HitRate = hitting(0.8)
	state.Replicas[2].Inflight = 6 // busier, and still the right target

	got, err := p.Choose(policy.Request{Body: conversation("pressure", 2)}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Replica.ID != "replica-2" {
		t.Errorf("a residency spill went to %s at %.0f%% hit rate, want the replica whose cache is still answering",
			got.Replica.ID, got.HitRate.Fraction*100)
	}
	if got.Reason != policy.ReasonSpillHitRate {
		t.Errorf("reason = %q, want %q", got.Reason, policy.ReasonSpillHitRate)
	}
}

// When the whole fleet is under the low-water mark there is no relief to be
// had, and declining the match buys nothing while costing the locality. The
// request keeps its match and the mix does not record a spill that relieved
// nothing.
func TestAFleetEntirelyUnderTheMarkKeepsItsMatchRatherThanChurning(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{HitRateLowWater: 0.20})
	state := fleetOf(t, 3)
	seed(t, p, state, "replica-0", "saturated", 2)
	for i := range state.Replicas {
		state.Replicas[i].HitRate = hitting(0.05)
	}

	got, _ := p.Choose(policy.Request{Body: conversation("saturated", 2)}, state)
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: every replica is under the mark, so there is nothing to spill to",
			got.Reason, policy.ReasonPrefixAffinity)
	}
	if got.Replica.ID != "replica-0" {
		t.Errorf("went to %s, want the replica holding the match", got.Replica.ID)
	}
}

// The load condition needs no such guard and must not acquire one: the target
// is the fleet minimum, and a minimum can never be more than a factor of at
// least one above itself. Nor may it start consulting feedback it does not
// read.
func TestALoadSpillTargetsTheMinimumWhateverItsResidencySays(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{LoadImbalanceFactor: 2.0})
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "loaded", 1)

	// The only escape is under the mark, but this spill is not about residency.
	state.Replicas[0].HitRate, state.Replicas[0].Inflight = hitting(0.9), 30
	state.Replicas[1].HitRate, state.Replicas[1].Inflight = hitting(0.01), 10

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
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{HitRateLowWater: 0.20})
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "declined-residency", 2)
	state.Replicas[0].HitRate = hitting(0.07)
	state.Replicas[1].HitRate = hitting(0.89)

	got, err := p.Choose(policy.Request{Body: conversation("declined-residency", 2)}, state)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got.Reason != policy.ReasonSpillHitRate {
		t.Fatalf("reason = %q, want %q", got.Reason, policy.ReasonSpillHitRate)
	}
	if !got.HitRate.Read || got.HitRate.Fraction != 0.89 {
		t.Errorf("Honoured = %v, want the target's 0.89", got.HitRate)
	}
	if !got.DeclinedHitRate.Read || got.DeclinedHitRate.Fraction != 0.07 {
		t.Errorf("DeclinedHitRate = %v, want the declined replica's 0.07 — the figure the rule fired on", got.DeclinedHitRate)
	}
}

// A load spill records it too, and a decision that declined nothing records no
// declined pressure rather than a zero that reads as an empty cache.
func TestOnlyASpillRecordsDeclinedPressure(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{LoadImbalanceFactor: 2.0})
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "declined-load", 1)
	state.Replicas[0].HitRate, state.Replicas[0].Inflight = hitting(0.58), 30
	state.Replicas[1].HitRate, state.Replicas[1].Inflight = hitting(0.9), 10

	got, _ := p.Choose(policy.Request{Body: conversation("declined-load", 1)}, state)
	if got.Reason != policy.ReasonSpillLoad {
		t.Fatalf("reason = %q, want %q", got.Reason, policy.ReasonSpillLoad)
	}
	if got.DeclinedInflight != 30 {
		t.Errorf("DeclinedInflight = %d, want the 30 the rule fired on", got.DeclinedInflight)
	}
	if !got.DeclinedHitRate.Read || got.DeclinedHitRate.Fraction != 0.58 {
		t.Errorf("DeclinedHitRate = %v, want 0.58", got.DeclinedHitRate)
	}

	// An affinity declines nothing, so it reports no declined pressure at all.
	state.Replicas[0].Inflight = 10
	got, _ = p.Choose(policy.Request{Body: conversation("declined-load", 1)}, state)
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Fatalf("reason = %q, want %q", got.Reason, policy.ReasonPrefixAffinity)
	}
	if got.DeclinedHitRate.Read || got.DeclinedInflight != 0 {
		t.Errorf("an affinity reported declined pressure %v / %d", got.DeclinedHitRate, got.DeclinedInflight)
	}
}

// The signal declines nothing on the evidence of a trickle of traffic. Below the
// window's query floor a replica's rate is unread, and the whole of the rule's
// degradation is that an unread reading is never under any mark — so a replica
// that has just come back into rotation, or one the fleet has barely touched,
// keeps its matches until there is enough traffic to take them away.
func TestAReplicaWithTooLittleTrafficDeclinesNothing(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{HitRateLowWater: 0.20})
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "thin-evidence", 1)

	// Its cache is answering nothing at all, but too little has gone through the
	// window to say so.
	state.Replicas[0].HitRate = vllmmetrics.HitRate{}
	state.Replicas[1].HitRate = hitting(0.90)

	got, _ := p.Choose(policy.Request{Body: conversation("thin-evidence", 1)}, state)
	if got.Reason != policy.ReasonPrefixAffinity {
		t.Errorf("reason = %q, want %q: a replica with no reading must not be spilled away from",
			got.Reason, policy.ReasonPrefixAffinity)
	}
}

// A replica with no reading is not a spill target to be excluded either. The
// exclusion exists to keep a spill from landing on a cache equally cold,
// and "unread" is not evidence of that: excluding it would empty the target set
// on a fresh fleet and turn every spill into a kept match.
func TestAReplicaWithNoReadingIsStillASpillTarget(t *testing.T) {
	p := policy.NewPrefixAffinity(prefixIndex(t), policy.Spill{HitRateLowWater: 0.20})
	state := fleetOf(t, 2)
	seed(t, p, state, "replica-0", "fresh-target", 1)

	state.Replicas[0].HitRate = hitting(0.05)
	state.Replicas[1].HitRate = vllmmetrics.HitRate{}

	got, _ := p.Choose(policy.Request{Body: conversation("fresh-target", 1)}, state)
	if got.Replica.ID != "replica-1" || got.Reason != policy.ReasonSpillHitRate {
		t.Errorf("went to %s as %q, want replica-1 as %q", got.Replica.ID, got.Reason, policy.ReasonSpillHitRate)
	}
}
