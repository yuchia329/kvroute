package bench

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/yuchia329/kvroute/internal/policy"
)

// CacheRouteInflightBound is the one inflight bound bounded session affinity is
// measured at (#36): every replica held to a quarter above the fleet's mean.
//
// It is CacheRoute's setting for the same policy, taken so that a reader
// comparing this repo's table to theirs is comparing one policy. It is not a
// default — the router refuses to run the policy without a bound stated, and
// this constant is what the grid's commands state — and it is not tuned: no
// other bound is swept, and the objection that 0.25 was a bad choice is left
// open on purpose (#35).
const CacheRouteInflightBound = 0.25

// ParseInflightBound reads a bound written as the fraction above the fleet's
// mean inflight it allows: `0.25`.
//
// Zero is refused rather than read as "no bound". It is how a cell records that
// its policy had none, so a sweep naming it would label a bounded cell as an
// unbounded one; a policy with no bound is swept with no -inflight-bound at all.
func ParseInflightBound(spec string) (policy.InflightBound, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(spec), 64)
	if err != nil {
		return 0, fmt.Errorf("bench: %q is not an inflight bound: it is written as the fraction above the fleet's mean inflight a replica may hold, as 0.25: %w", spec, err)
	}
	bound := policy.InflightBound(value)
	if err := bound.Validate(); err != nil {
		return 0, err
	}
	if !bound.Stated() {
		return 0, fmt.Errorf("bench: an inflight bound of %v is not a bound: zero is how a cell records that its policy had none", value)
	}
	return bound, nil
}

// FormatInflightBound renders a bound into the spec ParseInflightBound reads.
func FormatInflightBound(b policy.InflightBound) string {
	return strconv.FormatFloat(float64(b), 'g', -1, 64)
}
