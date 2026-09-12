package fleet

// The two figures a snapshot can describe its own load with, and why there are
// two of them.
//
// A rule that asks whether one replica is out of line with its siblings needs
// something to hold it against, and the fleet offers two candidates that agree
// when the fleet is busy and diverge when it is not. The minimum is the quietest
// replica; the mean is the fleet's own average. At 32 concurrent users over five
// replicas they are close enough that the choice barely shows. At 6 requests per
// second the fleet holds six requests at the median, the minimum is 0 on 96% of
// decisions and never above 1, and the mean runs from 0.6 to 2.8 — so a multiple
// of the first is an absolute inflight threshold wearing a ratio's clothes, and
// a multiple of the second is at least a number that moves (#31, measured
// 2026-09-12).
//
// Both live here, on the snapshot, rather than in the policy that compares
// against one of them: what the fleet as a whole is carrying is a fact about the
// fleet, and a policy computing it by walking State's slice would put the
// fleet's shape in the policy package. The choice between them is the policy's
// and stays there — see policy.Spill.

// MinInflight is the least loaded replica's inflight, as counted.
//
// Unfloored on purpose. A rule that will not divide by an idle replica has to
// say so itself, because the floor it picks is the thing being measured, and a
// minimum that arrived already floored would hide which decisions were made
// against a real quietest replica and which against a constant.
//
// An empty fleet has no minimum and reports 0. Nothing reaches a policy without
// a candidate, so this is a defined value rather than a decision anybody makes.
func (s State) MinInflight() int {
	if len(s.Replicas) == 0 {
		return 0
	}
	fewest := s.Replicas[0].Inflight
	for _, c := range s.Replicas[1:] {
		fewest = min(fewest, c.Inflight)
	}
	return fewest
}

// MeanInflight is what the fleet is holding, spread evenly over the replicas it
// is holding it on.
//
// The replicas in this snapshot, which under a chaos run is fewer than the fleet
// was started with: a decision made while a replica was ejected was made over
// the replicas that were there, and averaging over the ones that were not would
// report a fleet quieter than the one the request met.
//
// A float rather than a rounded count, because it is a denominator and rounding
// 2.4 to 2 at five replicas would restore a sixth of the gap this figure exists
// to close.
func (s State) MeanInflight() float64 {
	if len(s.Replicas) == 0 {
		return 0
	}
	total := 0
	for _, c := range s.Replicas {
		total += c.Inflight
	}
	return float64(total) / float64(len(s.Replicas))
}
