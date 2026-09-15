package belief

import (
	"sync"
	"time"
)

// Feedback is one replica's running honoured rate, fed by the responses the
// router proxies from it.
//
// A ring of the window's most recent scoring requests, with the two sums the
// rate is derived from carried alongside it. Derived rather than stored, so a
// reading cannot disagree with the evidence it came from — the same rule
// prefix.Divergence and vllmmetrics.PrefixCache are built on.
//
// It is written once per response and read once per routing decision, both on
// hot paths, so the whole of it sits under one mutex held for a ring step
// rather than under anything that allocates. Expiry happens on read rather than
// on a timer: a replica the rule has spilled away from receives no responses at
// all, and that is precisely the replica whose stale evidence has to age out.
type Feedback struct {
	window Window

	mu sync.Mutex
	// ring holds at most window.Requests observations, oldest at first.
	ring        []observation
	first, held int
	// claimed and honoured are the sums over the ring: the tokens claimed, and
	// the tokens claimed that the engines turned out to be holding.
	claimed, honoured float64
}

// observation is one scoring request: when it was answered, what the router had
// claimed of the replica, and how much of that claim the engine honoured.
type observation struct {
	at                time.Time
	claimed, honoured float64
}

// NewFeedback builds a replica's running rate over the given window. The window
// is the caller's to validate; an invalid one is a run misconfigured at startup
// rather than something this can repair.
func NewFeedback(w Window) *Feedback {
	if w.Requests <= 0 {
		w.Requests = DefaultWindow.Requests
	}
	return &Feedback{window: w, ring: make([]observation, w.Requests)}
}

// Observe folds one answered request into the rate: what the router claimed the
// replica was holding, and what the engine said it held.
//
// A request that claimed nothing is not evidence and is dropped. It says
// nothing about whether this replica is honouring beliefs — there was no belief
// — and counting it would be counting a claim that was perfectly honoured by
// arithmetic, which drags every rate towards one and silently disables the
// condition.
//
// The honoured part is min(claimed, held), which bounds the rate above by one:
// a replica holding more than was claimed of it has honoured the claim, and the
// surplus is an under-prediction, which is the index's other defect and has its
// own measurement. Letting it push the rate past one would let a forgetful index
// look like a replica with cache room to spare.
func (f *Feedback) Observe(at time.Time, claimed, held float64) {
	if claimed <= 0 {
		return
	}
	honoured := min(claimed, held)

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held == len(f.ring) {
		f.drop()
	}
	f.ring[(f.first+f.held)%len(f.ring)] = observation{at: at, claimed: claimed, honoured: honoured}
	f.held++
	f.claimed += claimed
	f.honoured += honoured
}

// Rate is the reading a routing decision is made against.
//
// Evidence older than the TTL is dropped first, so a replica that has stopped
// being sent matches goes unread rather than staying frozen at whatever it last
// reported. Below the quorum the reading is unread too, and an unread reading
// declines nothing.
func (f *Feedback) Rate(now time.Time) Honoured {
	f.mu.Lock()
	defer f.mu.Unlock()

	cutoff := now.Add(-f.window.TTL)
	for f.held > 0 && f.ring[f.first].at.Before(cutoff) {
		f.drop()
	}
	if f.held < f.window.Quorum || f.claimed <= 0 {
		return Honoured{}
	}
	// Clamped, because the sums are maintained incrementally and drift. Every
	// observation contributes honoured <= claimed, so the ratio is bounded by one
	// in exact arithmetic; in float64 a window that has added and subtracted a few
	// thousand values can land a few ulps above it. Replaying #28's measured run
	// produced 1.0000000000000004 within the first window.
	//
	// No threshold can see the difference — Validate refuses a mark of 1, and
	// every mark below it compares the same either way — but the invariant is what
	// the type means, and a reported maximum above 1 would read as a defect in the
	// measurement rather than in the arithmetic.
	return Honoured{Fraction: min(f.honoured/f.claimed, 1), Read: true, Claims: f.held}
}

// drop forgets the oldest observation in the ring and takes it back out of the
// sums. The caller holds the mutex.
//
// The sums are maintained incrementally rather than re-added over the ring on
// every read, because the read is on the routing decision's path and the write
// is on every response's: re-summing sixty-four entries six times per decision
// would put the whole fleet's history inside the decision budget.
func (f *Feedback) drop() {
	oldest := f.ring[f.first]
	f.claimed -= oldest.claimed
	f.honoured -= oldest.honoured
	f.ring[f.first] = observation{}
	f.first = (f.first + 1) % len(f.ring)
	f.held--
	if f.held == 0 {
		// Re-zeroed rather than left at whatever the subtractions came to, so
		// that floating-point drift over a long run cannot leave an empty window
		// holding a residue that reads as a claim.
		f.claimed, f.honoured = 0, 0
	}
}
