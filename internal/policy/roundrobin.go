package policy

import (
	"sync/atomic"

	"github.com/yuchia329/kvroute/internal/fleet"
)

// RoundRobinName is the configuration name of the round-robin policy.
const RoundRobinName = "round_robin"

// RoundRobin distributes requests evenly with no state. It is the naive
// baseline the comparison starts from.
type RoundRobin struct {
	next atomic.Uint64
}

// NewRoundRobin builds the round-robin policy.
func NewRoundRobin() *RoundRobin { return &RoundRobin{} }

func (p *RoundRobin) Name() string { return RoundRobinName }

func (p *RoundRobin) Choose(_ Request, state fleet.State) (Choice, error) {
	if len(state.Replicas) == 0 {
		return Choice{}, ErrNoReplica
	}
	chosen := state.Replicas[(p.next.Add(1)-1)%uint64(len(state.Replicas))]
	// The load is reported even though nothing here weighed it: the rows are what
	// show how balanced each policy left the fleet, and the baseline's imbalance
	// is half of that comparison.
	return Choice{
		Replica:  chosen.Replica,
		Reason:   ReasonRoundRobin,
		Inflight: chosen.Inflight,
	}, nil
}
