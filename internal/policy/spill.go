package policy

import (
	"fmt"

	"github.com/yuchia329/kvroute/internal/fleet"
)

// Spill is the pair of thresholds at which prefix affinity is declined.
//
// The rule is idea.md §4.1's, and its two conditions are two different
// pressures rather than two spellings of one. The residency condition says the
// best replica has stopped honouring what the index believes about it, so the
// match believed in there is already being evicted and routing to it pays the
// prefill anyway. The load condition says the best replica is buried under
// work, so the match would be paid for in queueing rather than in prefill. A
// fleet can be in either state without being in the other, and CONTEXT.md's
// pressure grid crosses two axes precisely because working set ratio and skew
// fire these two branches separately.
//
// That last paragraph was false for the life of #16 and #18, and ADR-0011
// records why. The first condition used to read vllm:kv_cache_usage_perc as
// memory pressure, and that gauge counts the blocks held by a replica's
// *running* requests — the active batch, which the second condition already
// measures exactly and locally. Over 13,658 decisions the two agreed at
// r = 0.973. The two conditions were one condition in two units, two of the
// three high-water levels swept could not fire at the concurrency they ran at,
// and the separability the pressure grid rests on could not be evaluated.
//
// The residency branch now reads the engines' own prefix cache hit rate, per
// replica and over a moving window. It read honoured belief first — the share
// of the router's claims each replica turned out to be holding — and that was
// built, measured and rejected on the evidence: the prefix index is calibrated
// not to over-predict (ADR-0006, ADR-0008), so it gives up beliefs before the
// engines evict the blocks and is nearly always right about whatever it still
// claims. Its rate came back pinned at 1.0 for 70% of readings, and replaying
// the run through five windows widened the histogram 2.4x while the signal's
// predictive gap stayed flat at ~1.6 points. The hit rate has range on the same
// run: 68.7% fleet-wide. ADR-0011 records both findings.
//
// Neither threshold is a measurement, which is what makes them different in
// kind from the prefix index's bounds — those are derived from the fleet and
// ADR-0006 refuses to default them. These are the knobs the tradeoff curve is
// drawn by sweeping, so a value here is a grid point rather than a claim, and
// every cell records the point it ran at.
type Spill struct {
	// HitRateLowWater is the prefix cache hit rate below which the best match is
	// declined, as a fraction. Zero disables the condition.
	//
	// A low-water mark where the gauge it replaced had a high-water one, because
	// the two run in opposite directions: a replica in trouble reads high on an
	// occupancy gauge and low on a rate of blocks its cache answered. The grid is
	// swept downward from 1 rather than upward from 0.
	HitRateLowWater float64 `json:"hit_rate_low_water"`
	// LoadImbalanceFactor is the multiple of the fleet's minimum inflight above
	// which the best match is declined. Zero disables the condition.
	//
	// A multiple of the fleet minimum rather than an absolute count, because the
	// question it asks is whether this replica is out of line with its siblings,
	// and an absolute count would mean something different at every rung of the
	// concurrency ladder. Whether it manages to be a multiple of anything is
	// what MeanInflightDenominator is about.
	LoadImbalanceFactor float64 `json:"load_imbalance_factor"`
	// MeanInflightDenominator holds the factor against the fleet's mean inflight
	// rather than against its minimum. False is the rule as #16 swept it and #18
	// settled it, and every cell measured before #31 ran at false.
	//
	// It is on this struct rather than hidden in the comparison because it is a
	// grid axis: at the rung the factor was settled at the two denominators ask
	// for much the same thing, and at low open-loop load they are different rules
	// (#31). A cell labelled with a factor alone would be two rules wearing one
	// number. See loaddenominator.go for what each one asks.
	MeanInflightDenominator bool `json:"mean_inflight_denominator"`
}

// MinimumInflightFloor is the least the load condition will compare against,
// whichever denominator it is comparing.
//
// Read literally, idea.md's rule multiplies the factor by the fleet's minimum
// inflight, and an idle fleet has a minimum of zero. Any factor times zero is
// zero, so the rule would decline every affinity to a replica holding a single
// request whenever one sibling happened to be idle — which is the entire bottom
// of the concurrency ladder, where the fleet sits idle between arrivals. Policy
// 4 would collapse into policy 2 at low load and the sweep would report that as
// a property of cache-aware routing. An idle fleet's mean is zero too, so the
// floor is not a property of the minimum and applies to both.
//
// The floor of one makes the condition read as "more than a factor's worth of
// work above an idle replica", which is the imbalance the rule is about. What it
// does not do is keep the comparison a ratio: a floored comparison is an
// absolute inflight threshold for as long as the floor is what binds, and at
// low open-loop load that is most decisions (#31). MinimumInflightFloor stops
// the rule collapsing on an idle fleet; it does not make a small integer behave
// like a scale.
const MinimumInflightFloor = 1

// HasSpillRule reports whether the named policy declines matches under the spill
// rule. Prefix affinity and exact residency do — the same rule at the same
// thresholds, since #24 compares the two to find what exact knowledge of the
// caches is worth and nothing else — and the other three have no valve.
//
// By name, for the callers that only have one: a cell records the policy it was
// labelled with, not the policy. A caller holding the policy itself asks it
// instead, through Tuned.
func HasSpillRule(name string) bool {
	return name == PrefixAffinityName || name == ExactResidencyName
}

// Enabled reports whether either condition can fire.
//
// The zero value is no spill rule at all, and that is deliberate: it is the
// policy measured under #15, whose comparison is the baseline this one is read
// against. A threshold that defaulted itself on would change what a
// prefix_affinity cell means without a single cell saying so.
func (s Spill) Enabled() bool { return s.HitRateLowWater > 0 || s.LoadImbalanceFactor > 0 }

// Validate refuses a threshold that cannot mean what it says.
func (s Spill) Validate() error {
	if s.HitRateLowWater < 0 || s.HitRateLowWater > 1 {
		return fmt.Errorf("policy: the hit-rate low-water mark is a share of block queries and must be between 0 and 1, got %v", s.HitRateLowWater)
	}
	if s.HitRateLowWater == 1 {
		// The comparison is strict, so a mark of 1 declines every match to every
		// replica whose cache ever missed a block — which is every replica
		// serving unseen bytes, and ADR-0004 makes every cell send some. Far more
		// likely a mistyped 0.1 than a deliberate setting.
		return fmt.Errorf("policy: a hit-rate low-water mark of 1 declines every match to any replica whose cache missed a single block, which is every replica under this workload: use a mark below 1, or 0 to disable the condition")
	}
	if s.LoadImbalanceFactor < 0 {
		return fmt.Errorf("policy: the load imbalance factor is a multiple of the fleet minimum and cannot be negative, got %v", s.LoadImbalanceFactor)
	}
	if s.LoadImbalanceFactor > 0 && s.LoadImbalanceFactor < 1 {
		return fmt.Errorf("policy: a load imbalance factor of %v is below 1, so the best match would be declined for replicas carrying more work than it: use 0 to disable the condition", s.LoadImbalanceFactor)
	}
	if s.MeanInflightDenominator && s.LoadImbalanceFactor == 0 {
		// A denominator for a comparison nobody is making. It reaches a cell's
		// label and a router's stats either way, so it would describe a rule the
		// run did not run — and it is far more likely to be a grid point written
		// half way than a deliberate setting.
		return fmt.Errorf("policy: the mean inflight denominator is what the load imbalance factor is compared against, and that condition is disabled: set a factor, or leave the denominator alone")
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
	hit, load := "off", "off"
	if s.HitRateLowWater > 0 {
		hit = fmt.Sprintf("%.2f", s.HitRateLowWater)
	}
	if s.LoadImbalanceFactor > 0 {
		// The denominator is named on every enabled point rather than only on
		// the new one. A run that reports "2.0x" and leaves the reader to assume
		// which fleet figure it was 2.0 times is the ambiguity #31 spent a chaos
		// arm on.
		load = fmt.Sprintf("%.1fx %s", s.LoadImbalanceFactor, s.LoadDenominatorName())
	}
	return "hit<" + hit + " load>" + load
}

// LoadDenominatorName is what the load condition is comparing against, as a
// word, for the labels and reports that have to say which rule ran.
func (s Spill) LoadDenominatorName() string {
	if s.MeanInflightDenominator {
		return "mean"
	}
	return "min"
}

// Declines reports whether the best match is under too much pressure to route
// to, and which condition said so.
//
// Residency first, then load, and a request that is over both counts once under
// the first: the decision mix is a count of decisions, and a request that
// spilled for two reasons still spilled once. The order is the spec's and it is
// the right way round — a replica that is evicting the match cannot serve it at
// all, where a buried one merely serves it late.
func (s Spill) Declines(best fleet.Candidate, state fleet.State) (Reason, bool) {
	if s.HitRateLowWater > 0 && best.HitRate.Under(s.HitRateLowWater) {
		return ReasonSpillHitRate, true
	}
	// The factor is checked before the fleet is walked, not inside DeclinesLoad.
	// A disabled condition is the observing pass and the four-policy comparison's
	// own prefix_affinity cells, which is most decisions this project has ever
	// made, and they should pay nothing for a comparison nobody asked for. Both
	// figures are then taken even though one denominator is used: two passes over
	// five candidates is nothing against DecisionBudget, and a lazier version
	// would put a branch on the denominator in the one place the rule reads
	// clearest.
	if s.LoadImbalanceFactor > 0 && s.DeclinesLoad(best.Inflight, state.MinInflight(), state.MeanInflight()) {
		return ReasonSpillLoad, true
	}
	return "", false
}

// targets is the set a declined request may be placed on: the replicas that can
// actually relieve the pressure that declined it.
//
// The declined replica itself is always out. Least-outstanding can otherwise
// hand the request straight back to it — a replica that is evicting and also
// the quietest in the fleet is an ordinary state for a fleet whose traffic has
// moved on — and routing there under a spill reason would record a decision
// that did not happen.
//
// A residency spill additionally excludes every replica that is itself under
// the mark. Ties on prefix match are the common case in this workload rather
// than the exception, and a fleet oversubscribed enough to evict is evicting
// everywhere at once, so the next least loaded replica is very often another
// holder honouring just as little. Spilling there declines a match, pays the
// prefill and relieves nothing, while the decision mix and the declined-bytes
// column — the two halves of the grid's result — record work that was never
// done. An empty set means no relief is available, and the caller keeps the
// match rather than churning.
//
// The load condition needs no such exclusion and must not have one. Its target is
// the fleet's least loaded replica, and that replica is at or below whatever the
// condition compares against — the minimum by definition, and the mean because a
// minimum is never above its own mean — so with a factor of at least one the
// condition cannot fire again on its own target under either denominator.
// Excluding on the hit rate there would also make a load spill depend on
// feedback it does not consult.
func (s Spill) targets(state fleet.State, declined fleet.Candidate, reason Reason) fleet.State {
	rest := make([]fleet.Candidate, 0, len(state.Replicas))
	for _, c := range state.Replicas {
		if c.ID == declined.ID {
			continue
		}
		if reason == ReasonSpillHitRate && c.HitRate.Under(s.HitRateLowWater) {
			continue
		}
		rest = append(rest, c)
	}
	return fleet.State{Replicas: rest}
}
