package policy

import (
	"sync/atomic"

	"github.com/yuchia329/kvroute/internal/fleet"
)

// LeastOutstandingName is the configuration name of the least-outstanding policy.
const LeastOutstandingName = "least_outstanding"

// LeastOutstanding routes to the replica holding the fewest inflight requests.
//
// It is what nginx's least_conn and a Kubernetes Service already give you, so it
// is the baseline that the cache-aware policies have to beat to be worth
// anything: beating round-robin only shows that counting load beats not counting
// it.
//
// It weighs the router's own count, which is the whole point of counting it
// locally. The same policy fed a scraped figure is the classic stale
// load-balancer failure — see TestExactCountsPreventTheHerdAStaleViewWouldCause,
// which runs both over one window of arrivals.
type LeastOutstanding struct {
	// next breaks ties, and is the reason an idle fleet does not funnel into one
	// replica: with every candidate at zero inflight there is nothing to choose
	// between them, and the whole low-concurrency end of the sweep runs in that
	// state. It is atomic so that requests choosing at the same instant take
	// different turns of the rotation rather than reading the same offset.
	next atomic.Uint64
}

// NewLeastOutstanding builds the least-outstanding policy.
func NewLeastOutstanding() *LeastOutstanding { return &LeastOutstanding{} }

func (p *LeastOutstanding) Name() string { return LeastOutstandingName }

// Choose returns the least loaded replica, breaking ties by rotation.
//
// The count it reads is a snapshot taken a moment before this call, so two
// requests choosing simultaneously can both see the same minimum and both take
// it. That window is the width of one routing decision — microseconds — and it
// closes as soon as either request is dispatched. It is not the polling window
// that scraping this figure would open, which would be 250 ms to 1 s wide and
// would catch every arrival inside it.
func (p *LeastOutstanding) Choose(_ Request, state fleet.State) (Choice, error) {
	if len(state.Replicas) == 0 {
		return Choice{}, ErrNoReplica
	}

	chosen := leastLoadedOf(state.Replicas, &p.next)
	return Choice{
		Replica:  chosen.Replica,
		Reason:   ReasonLeastOutstanding,
		Inflight: chosen.Inflight,
	}, nil
}

// leastLoadedOf returns the least loaded of these candidates, rotating between
// those tied at the minimum.
//
// The rotation is why an idle fleet does not funnel into one replica: with every
// candidate at zero inflight there is nothing to choose between them, and the
// whole low-concurrency end of the sweep runs in that state. next is taken by
// pointer and advanced atomically so that requests choosing at the same instant
// take different turns rather than reading the same offset.
//
// Shared with prefix affinity, which needs the same answer over the subset of
// replicas tied on prefix match. Two implementations would be two tie-breaks,
// and a cold request placed differently by the two policies would be a
// difference between them that is not the mechanism.
func leastLoadedOf(candidates []fleet.Candidate, next *atomic.Uint64) fleet.Candidate {
	fewest := candidates[0].Inflight
	for _, c := range candidates[1:] {
		fewest = min(fewest, c.Inflight)
	}
	tied := make([]fleet.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Inflight == fewest {
			tied = append(tied, c)
		}
	}
	turn := next.Add(1) - 1
	return tied[turn%uint64(len(tied))]
}
