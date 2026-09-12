package bench

import "github.com/yuchia329/kvroute/internal/vllmmetrics"

// PolicyPrefill is the prompt-token work one policy left the fleet's GPUs at one
// load point, together with the measured requests that work served.
//
// The two travel in one value because the figure the comparison reports is the
// quotient, and the quotient is the only form of it that means anything under the
// closed-loop driver. A virtual user there sends its next turn when its last one
// returns, so a policy that answers faster gets further through the same sequence
// and offers *more* prompts in the same window. Refusing cells whose workload
// names differ guarantees both policies the same generator; it does not guarantee
// them the same number of prompts. Absolute recomputed tokens therefore rise with
// throughput, and a column built on them credits the slower policy with having
// wasted less — which is exactly backwards, and is what #18's grid showed at six
// of its twelve points.
//
// Keeping the denominator beside the numerator is what stops that from being
// reintroduced: there is no way to hold one of these values and divide by another
// policy's requests, and no way to subtract two totals without noticing they were
// earned over different request counts.
type PolicyPrefill struct {
	// Prefill is the pooled counters: prompt tokens the fleet processed over this
	// policy's usable repetitions at this load point, and the share of them it
	// answered out of cache. Embedded so Read, Recomputed and Evidenced mean here
	// exactly what they mean on the reading itself.
	vllmmetrics.Prefill
	// Requests is the measured requests those same repetitions served, summed the
	// way the counters above are and over the same cells. Warm-up requests are
	// not in it, for the reason they are not in the counters: both are read over
	// the cell's measured window.
	Requests int
}

// RecomputedPerRequest is the prompt tokens this policy left the GPUs to compute
// for each request it served, and whether there is a figure at all.
//
// A ratio needs its denominator, so a policy whose cells recorded no measured
// request reports nothing rather than zero — there is no such thing as the
// prefill per request of no requests, and a zero in that column would read as a
// policy that wasted nothing.
func (p PolicyPrefill) RecomputedPerRequest() (float64, bool) {
	if !p.Read || p.Requests <= 0 {
		return 0, false
	}
	return p.Recomputed() / float64(p.Requests), true
}

// RedundantAgainst is the redundant prefill this policy carries over a baseline:
// the extra prompt tokens it computed per request, on the same bytes.
//
// The comparison is what makes it redundant rather than merely computed. Both
// policies sent the identical workload, so prompt tokens one fleet computed per
// request and another did not are tokens some replica was already holding — the
// physical work routing can remove, measured rather than inferred from the
// router's own beliefs.
//
// Per request rather than in total: see the type's own comment for why a
// difference of totals measures throughput instead.
func (p PolicyPrefill) RedundantAgainst(baseline PolicyPrefill) (float64, bool) {
	mine, ok := p.RecomputedPerRequest()
	if !ok {
		return 0, false
	}
	theirs, ok := baseline.RecomputedPerRequest()
	if !ok {
		return 0, false
	}
	return mine - theirs, true
}

// RedundantPerRequest is a policy's redundant prefill at this load point: the
// prompt tokens it left the fleet computing per request, over and above the
// policy that computed fewest per request on identical bytes.
//
// It reports false unless every policy the comparison covers was measured here,
// with a denominator to divide by. A row where one policy's counters are missing
// has no floor to measure the others against, and picking the lowest of what was
// read would credit a scrape failure — or a policy that never ran at this load
// point — as the best result in the table.
//
// The check is against the comparison's own policy list rather than against the
// readings present, because those are the same set only when nothing went
// missing, and the case this guards is exactly the one where something did.
func (r ComparisonRow) RedundantPerRequest(name string) (float64, bool) {
	mine, ok := r.Prefill[name]
	if !ok {
		return 0, false
	}
	if _, ok := mine.RecomputedPerRequest(); !ok {
		return 0, false
	}
	best := mine
	for _, policy := range r.policies {
		other, measured := r.Prefill[policy]
		if !measured {
			return 0, false
		}
		rate, ok := other.RecomputedPerRequest()
		if !ok {
			return 0, false
		}
		if floor, _ := best.RecomputedPerRequest(); rate < floor {
			best = other
		}
	}
	return mine.RedundantAgainst(best)
}

// RedundantTokens is the same excess expressed as prompt tokens: this policy's
// per-request redundancy times the requests *it* served.
//
// Its own requests, deliberately. The quantity is the work this policy's own
// traffic left the GPUs doing that a better-placed fleet would not have, so the
// only request count that converts it is the one it earned it over. A difference
// of two policies' raw totals is not this number and is not any number: under a
// closed loop the two totals were earned over different amounts of traffic.
func (r ComparisonRow) RedundantTokens(name string) (float64, bool) {
	excess, ok := r.RedundantPerRequest(name)
	if !ok {
		return 0, false
	}
	return excess * float64(r.Prefill[name].Requests), true
}

// poolPolicyPrefill adds up a policy's repetitions' prompt-token counters and the
// requests they served, for the reason poolPrefixCache sums rather than averages:
// these are counts of work and of traffic, and two windows of either combine by
// adding. One unscraped repetition makes the whole figure unread.
//
// Both sides are summed over the same cells, which is what makes the quotient a
// ratio rather than two unrelated pools divided.
func poolPolicyPrefill(cells []Cell) PolicyPrefill {
	pooled := PolicyPrefill{Prefill: poolPrefill(cells)}
	for _, cell := range cells {
		pooled.Requests += cell.Requests
	}
	return pooled
}
