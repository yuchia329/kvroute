package policy

// What the load condition holds a replica's inflight against, and why that is a
// choice rather than a detail.
//
// idea.md §4.1 writes the condition as a ratio: decline the best match when its
// replica is carrying more than a factor times the fleet's minimum inflight. A
// ratio is scale-free, which is the property that lets one factor settled at one
// rung describe the rule at every other. The minimum does not have that
// property. It is a single replica's count, it is a small integer, and at low
// load it is 0 or 1 — so the comparison becomes a factor times the floor, which
// is an absolute number of requests, and the rule stops being the rule it is
// written as.
//
// #31 measured what that costs. Driven open-loop at 6 requests per second over
// five replicas, the fleet holds around twelve requests and the load condition
// declined 19–20% of later turns, against 0.674% at the closed-loop 32-user rung
// the factor was settled at. Those spilled turns found 11.9% of their prompt
// cached where the turns that stayed found 73.9%, and 42% of them missed the
// SLO. The rule was not doing what the factor says it does; it was declining any
// replica holding more than two requests.
//
// So the denominator is a grid axis. Two candidates, and a cell records which
// one it ran at:
//
//	minimum   the quietest replica's inflight     the rule as #16 settled it
//	mean      the fleet's inflight over its replicas
//
// One piece of arithmetic is worth stating, because it collapses the option
// #31 proposed into the one below. The ticket suggests flooring the comparison
// at max(minimum, fleet inflight / replicas). The minimum of a set is never
// above its mean, so that expression is the mean, always — there is no regime
// where the floor releases and the minimum comes back. Flooring the minimum at
// the mean and comparing against the mean are the same rule, and calling it a
// floor would suggest a fallback that cannot happen.
//
// The floor of one survives both, because the failure it exists to prevent
// survives both: an idle fleet has a minimum of 0 and a mean of 0, and any
// factor times nothing declines every match to a replica holding a single
// request.
//
// Which denominator is better is not decided here and is not decidable from
// arithmetic: the mean declines less often at every rung, and declining less
// often is worth something only where the declines were not paying for
// themselves. That is a goodput question, and #31 leaves it to a sweep of the
// factor at open-loop rungs — the discipline ADR-0006 applies to the index's
// bounds and HitRateLowWaterGrid applies to the residency mark.

// LoadDenominator is what a replica's inflight is held against on this fleet:
// the quietest replica, or the average one, floored at MinimumInflightFloor.
//
// Exported because the decision and the analysis of the decision have to use one
// arithmetic. A report projecting which factors would have fired over a run's
// recorded rows computes this from two columns rather than from a fleet, and a
// second copy of the expression there would be a second rule that nothing
// compares against the first.
func (s Spill) LoadDenominator(minimum int, mean float64) float64 {
	against := float64(minimum)
	if s.MeanInflightDenominator {
		against = mean
	}
	return max(against, MinimumInflightFloor)
}

// LoadThreshold is the inflight a replica has to be strictly above for the load
// condition to decline its match, on a fleet with this minimum and this mean.
//
// Zero when the condition is disabled, which is not a threshold of zero: a
// disabled condition declines nothing, and DeclinesLoad is where that is
// decided. It is here so that a report can print what a grid point was actually
// asking for at a rung, beside how often it got it.
func (s Spill) LoadThreshold(minimum int, mean float64) float64 {
	if s.LoadImbalanceFactor <= 0 {
		return 0
	}
	return s.LoadImbalanceFactor * s.LoadDenominator(minimum, mean)
}

// DeclinesLoad reports whether the load condition declines a replica holding
// this many requests, on a fleet whose quietest replica holds minimum and whose
// replicas average mean.
//
// In scalars rather than in a fleet.State, because the two callers hold
// different things and must not answer differently. The router holds the
// snapshot the decision was made against; a report holds the two numbers that
// snapshot wrote onto the row, months later and with no fleet running.
func (s Spill) DeclinesLoad(inflight, minimum int, mean float64) bool {
	if s.LoadImbalanceFactor <= 0 {
		return false
	}
	return float64(inflight) > s.LoadThreshold(minimum, mean)
}
