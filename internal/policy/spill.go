package policy

import (
	"fmt"

	"github.com/yuchia329/kvroute/internal/fleet"
)

// Spill is the pair of thresholds at which prefix affinity is declined.
//
// The rule is idea.md §4.1's, and its two conditions are two different
// pressures rather than two spellings of one. KV utilization says the best
// replica is out of cache room, so the match the index believes in there is
// already being evicted and routing to it pays the prefill anyway. Inflight
// says the best replica is buried under work, so the match would be paid for in
// queueing rather than in prefill. A fleet can be in either state without being
// in the other, and CONTEXT.md's pressure grid crosses two axes precisely
// because working set ratio and skew fire these two branches separately.
//
// Neither threshold is a measurement, which is what makes them different in
// kind from the prefix index's bounds — those are derived from the fleet and
// ADR-0006 refuses to default them. These are the knobs the tradeoff curve is
// drawn by sweeping, so a value here is a grid point rather than a claim, and
// every cell records the point it ran at.
type Spill struct {
	// KVHighWater is the KV utilization above which the best match is declined,
	// as a fraction. Zero disables the condition.
	KVHighWater float64 `json:"kv_high_water"`
	// LoadImbalanceFactor is the multiple of the fleet's minimum inflight above
	// which the best match is declined. Zero disables the condition.
	//
	// A multiple of the fleet minimum rather than an absolute count, because the
	// question it asks is whether this replica is out of line with its siblings,
	// and an absolute count would mean something different at every rung of the
	// concurrency ladder.
	LoadImbalanceFactor float64 `json:"load_imbalance_factor"`
}

// MinimumInflightFloor is the fleet minimum the load condition compares
// against when the real minimum is zero.
//
// Read literally, idea.md's rule multiplies the factor by the fleet's minimum
// inflight, and an idle fleet has a minimum of zero. Any factor times zero is
// zero, so the rule would decline every affinity to a replica holding a single
// request whenever one sibling happened to be idle — which is the entire bottom
// of the concurrency ladder, where the fleet sits idle between arrivals. Policy
// 4 would collapse into policy 2 at low load and the sweep would report that as
// a property of cache-aware routing.
//
// The floor of one makes the condition read as "more than a factor's worth of
// work above an idle replica", which is the imbalance the rule is about. It
// changes nothing once the fleet is busy, which is where the comparison is
// decided.
const MinimumInflightFloor = 1

// Enabled reports whether either condition can fire.
//
// The zero value is no spill rule at all, and that is deliberate: it is the
// policy measured under #15, whose comparison is the baseline this one is read
// against. A threshold that defaulted itself on would change what a
// prefix_affinity cell means without a single cell saying so.
func (s Spill) Enabled() bool { return s.KVHighWater > 0 || s.LoadImbalanceFactor > 0 }

// Validate refuses a threshold that cannot mean what it says.
func (s Spill) Validate() error {
	if s.KVHighWater < 0 || s.KVHighWater > 1 {
		return fmt.Errorf("policy: the KV high-water mark is a fraction of cache capacity and must be between 0 and 1, got %v", s.KVHighWater)
	}
	if s.LoadImbalanceFactor < 0 {
		return fmt.Errorf("policy: the load imbalance factor is a multiple of the fleet minimum and cannot be negative, got %v", s.LoadImbalanceFactor)
	}
	if s.LoadImbalanceFactor > 0 && s.LoadImbalanceFactor < 1 {
		return fmt.Errorf("policy: a load imbalance factor of %v is below 1, so the best match would be declined for replicas carrying more work than it: use 0 to disable the condition", s.LoadImbalanceFactor)
	}
	return nil
}

// String renders the grid point, with a disabled condition named as off rather
// than as a zero threshold that would read as the most aggressive setting
// possible.
func (s Spill) String() string {
	if !s.Enabled() {
		return "off"
	}
	kv, load := "off", "off"
	if s.KVHighWater > 0 {
		kv = fmt.Sprintf("%.2f", s.KVHighWater)
	}
	if s.LoadImbalanceFactor > 0 {
		load = fmt.Sprintf("%.1fx", s.LoadImbalanceFactor)
	}
	return "kv>" + kv + " load>" + load
}

// Declines reports whether the best match is under too much pressure to route
// to, and which condition said so.
//
// KV first, then load, and a request that is over both counts once under the
// first: the decision mix is a count of decisions, and a request that spilled
// for two reasons still spilled once. The order is the spec's and it is the
// right way round — a replica out of cache room cannot serve the match at all,
// where a buried one merely serves it late.
func (s Spill) Declines(best fleet.Candidate, state fleet.State) (Reason, bool) {
	if s.KVHighWater > 0 && best.KV.Over(s.KVHighWater) {
		return ReasonSpillKV, true
	}
	if s.LoadImbalanceFactor > 0 && float64(best.Inflight) > s.LoadImbalanceFactor*float64(minInflight(state)) {
		return ReasonSpillLoad, true
	}
	return "", false
}

// targets is the set a declined request may be placed on: the replicas that can
// actually relieve the pressure that declined it.
//
// The declined replica itself is always out. Least-outstanding can otherwise
// hand the request straight back to it — a replica over its high-water mark and
// also the quietest in the fleet is an ordinary state for a fleet whose cache is
// full and whose traffic has moved on — and routing there under a spill reason
// would record a decision that did not happen.
//
// A KV spill additionally excludes every replica that is itself over the mark.
// Ties on prefix match are the common case in this workload rather than the
// exception, and under memory pressure a whole fleet crosses the mark together,
// so the next least loaded replica is very often another holder equally out of
// cache room. Spilling there declines a match, pays the prefill and relieves
// nothing, while the decision mix and the declined-bytes column — the two halves
// of the grid's result — record work that was never done. An empty set means no
// relief is available, and the caller keeps the match rather than churning.
//
// The load condition needs no such exclusion and must not have one. Its target
// is the fleet's minimum inflight, and a minimum cannot be more than a factor of
// at least one above itself, so the condition cannot fire again on its own
// target. Excluding on KV there would also make a load spill depend on a scrape
// it does not consult.
func (s Spill) targets(state fleet.State, declined fleet.Candidate, reason Reason) fleet.State {
	rest := make([]fleet.Candidate, 0, len(state.Replicas))
	for _, c := range state.Replicas {
		if c.ID == declined.ID {
			continue
		}
		if reason == ReasonSpillKV && c.KV.Over(s.KVHighWater) {
			continue
		}
		rest = append(rest, c)
	}
	return fleet.State{Replicas: rest}
}

// minInflight is the fleet's least loaded replica's inflight, floored at
// MinimumInflightFloor. An empty fleet has no minimum and never spills, which
// is moot: nothing reaches here without a candidate.
func minInflight(state fleet.State) int {
	if len(state.Replicas) == 0 {
		return MinimumInflightFloor
	}
	fewest := state.Replicas[0].Inflight
	for _, c := range state.Replicas[1:] {
		fewest = min(fewest, c.Inflight)
	}
	return max(fewest, MinimumInflightFloor)
}
