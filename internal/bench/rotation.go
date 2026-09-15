package bench

import (
	"hash/fnv"
	"math/rand/v2"
)

// rotation decides which conversation each open-loop arrival belongs to.
//
// Arrivals are spread over a pool of conversations rather than each being its
// own: the pool is ArrivalRate x ThinkTime, and every conversation takes exactly
// one turn per round of the pool. Without that the driver would offer every
// session's first turn and no session's second, which is a workload with no
// growing prefix for a cache-aware policy to be aware of.
//
// The order within a round is shuffled, and reshuffled every round. That is not
// cosmetic — a fixed order is what made round-robin look like session affinity
// on 2026-09-08. With a fixed order a conversation's next turn is always exactly
// `conversations` arrivals later, and a round-robin router advances one replica
// per arrival, so when the pool is a multiple of the replica count the two cancel
// and every turn of a conversation lands on the same replica. On this fleet that
// was every cell rather than a corner case: the pool is rate x 5s against five
// replicas, and 5R is a multiple of five for every integer rate. Round-robin took
// a 63% prefix cache hit rate and a knee two rungs higher than it had earned,
// and nothing in the goodput admitted to it.
//
// Reshuffling per round rather than once is the whole point: one order reused
// would put each conversation back at the same slot every round, which is the
// fixed stride again under another name.
//
// The gap between a conversation's turns therefore varies between 1 and
// 2n-1 arrivals with a mean of n, so think time becomes a distribution about the
// configured value rather than exactly it. That is the more honest workload
// anyway — real conversations do not resume on a metronome — and the mean, which
// is what sets how many conversations a cell holds open, is unchanged.
// ArrivalPlan names how an open-loop cell mapped arrivals onto conversations.
//
// It is on the cell because the workload name cannot carry it: the workload is
// what bytes a (user, turn) pair renders to, and this is which pair each arrival
// takes. Two cells can therefore share a workload name, send different traffic,
// and be pooled into one figure by a comparison that has no way to tell — which
// is exactly what would have happened reading cells from before 2026-09-08
// beside cells from after it. Closed-loop cells leave it empty: they have no
// pool and no rotation, their virtual users walk the workload directly.
const ArrivalPlan = "shuffled-rotation"

type rotation struct {
	conversations int
	seed          uint64
	// round is the round `order` was drawn for, and -1 before the first draw.
	round int
	order []int
}

// newRotation builds the rotation for a pool of the given size. The seed makes
// it replayable: ADR-0004 requires a re-run of a cell to send the same bytes, or
// the cache it is measuring is not the same cache.
func newRotation(conversations int, seed uint64) *rotation {
	return &rotation{conversations: max(1, conversations), seed: seed, round: -1}
}

// at maps the k-th arrival onto the workload's (user, turn) space.
//
// The turn is the round, so every conversation advances a turn each time the
// pool comes round — the same walk a closed-loop virtual user makes, which is
// deliberate, so the two drivers offer traffic of one shape and differ only in
// what paces it.
func (r *rotation) at(k int) (user, turn int) {
	round, slot := k/r.conversations, k%r.conversations
	if round != r.round {
		r.draw(round)
	}
	return r.order[slot], round
}

// draw lays out one round's order. Seeded on the round as well as the cell, so
// consecutive rounds differ, and so a re-run replays both.
func (r *rotation) draw(round int) {
	if r.order == nil {
		r.order = make([]int, r.conversations)
	}
	for i := range r.order {
		r.order[i] = i
	}
	rng := rand.New(rand.NewPCG(r.seed, uint64(round)))
	rng.Shuffle(len(r.order), func(i, j int) { r.order[i], r.order[j] = r.order[j], r.order[i] })
	r.round = round
}

// rotationSeed derives a cell's shuffle seed from its id, so a re-run of a cell
// replays its order while two cells do not share one. An unlabelled cell — the
// driver used directly, as the tests do — still gets a stable seed rather than
// an arbitrary one.
func rotationSeed(cellID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(cellID))
	return h.Sum64()
}
