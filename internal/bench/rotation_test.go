package bench

import (
	"fmt"
	"testing"
)

// TestARotationDoesNotHandOneReplicaEveryTurnOfAConversation is the 2026-09-08
// artifact as an executable check, at the level where it was caused.
//
// The open-loop driver rotates arrivals through a pool of conversations, so with
// a fixed stride a conversation's next turn is always exactly `pool` arrivals
// later. Round-robin advances one replica per arrival. When the pool is a
// multiple of the replica count those two cancel, and every turn of a
// conversation lands on the same replica — round-robin silently becomes session
// affinity.
//
// That is not a corner case on this fleet, it is every cell: the pool is
// rate x think time, think time was 5s and the fleet is five replicas, so the
// pool is 5R for every integer rate R and 5R is always a multiple of five.
// Measured on the box before the fix: 57.8% of consecutive turns landed on the
// same replica against a 20% chance level, and round-robin took a 63% prefix
// cache hit rate it had not earned.
func TestARotationDoesNotHandOneReplicaEveryTurnOfAConversation(t *testing.T) {
	const replicas = 5
	for _, rate := range []int{2, 4, 6, 8, 10, 12, 14, 16, 20} {
		pool := rate * 5 // rate x the 5s think time: a multiple of the replica count every time
		r := newRotation(pool, 1)

		// Where each conversation's turns land under a round-robin router, which
		// advances one replica per arrival it accepts.
		home := map[int][]int{}
		for k := range pool * 6 {
			user, _ := r.at(k)
			home[user] = append(home[user], k%replicas)
		}

		same, total := 0, 0
		for _, replicas := range home {
			for i := 1; i < len(replicas); i++ {
				total++
				if replicas[i] == replicas[i-1] {
					same++
				}
			}
		}
		share := float64(same) / float64(total)
		// Chance is 1/5. Anything near 1 is the artifact; the bound is loose
		// because a shuffle is allowed to be unlucky, not systematic.
		if share > 0.40 {
			t.Errorf("rate %d (pool %d): %.1f%% of consecutive turns landed on the same replica, against a 20%% chance level",
				rate, pool, share*100)
		}
	}
}

// The pool's meaning has to survive the shuffle: it is how many conversations a
// cell holds open at once, so every one of them still takes exactly one turn per
// round and they all advance together.
func TestEveryConversationStillTakesOneTurnPerRound(t *testing.T) {
	const pool = 40
	r := newRotation(pool, 7)

	for round := range 5 {
		seen := map[int]bool{}
		for slot := range pool {
			user, turn := r.at(round*pool + slot)
			if turn != round {
				t.Fatalf("arrival %d of round %d reports turn %d", slot, round, turn)
			}
			if seen[user] {
				t.Fatalf("round %d served conversation %d twice", round, user)
			}
			seen[user] = true
		}
		if len(seen) != pool {
			t.Errorf("round %d served %d of %d conversations", round, len(seen), pool)
		}
	}
}

// ADR-0004: a cell that is re-run has to send the same bytes, or the cache it is
// measuring is not the same cache. The shuffle is seeded for exactly that.
func TestTheSameSeedReplaysTheSameRotation(t *testing.T) {
	a, b := newRotation(37, 3), newRotation(37, 3)
	for k := range 37 * 4 {
		u1, t1 := a.at(k)
		u2, t2 := b.at(k)
		if u1 != u2 || t1 != t2 {
			t.Fatalf("arrival %d replayed as (%d,%d) then (%d,%d)", k, u1, t1, u2, t2)
		}
	}

	// A different seed has to actually differ, or seeding it bought nothing.
	other := newRotation(37, 4)
	differs := false
	for k := range 37 * 4 {
		if u1, _ := a.at(k); func() int { u, _ := other.at(k); return u }() != u1 {
			differs = true
			break
		}
	}
	if !differs {
		t.Error("two seeds produced an identical rotation")
	}
}

// The order has to change between rounds. One shuffle reused every round would
// put each conversation back at a fixed slot, which is the fixed stride again
// wearing a different order.
func TestTheOrderChangesBetweenRounds(t *testing.T) {
	const pool = 60
	r := newRotation(pool, 11)

	slotOf := func(round int) map[int]int {
		at := map[int]int{}
		for slot := range pool {
			user, _ := r.at(round*pool + slot)
			at[user] = slot
		}
		return at
	}
	first, second := slotOf(0), slotOf(1)

	fixed := 0
	for user, slot := range first {
		if second[user] == slot {
			fixed++
		}
	}
	if fixed > pool/4 {
		t.Errorf("%d of %d conversations kept the same slot in the next round, so the stride is still fixed", fixed, pool)
	}
}

// A pool of one is the degenerate case the driver reaches at the bottom of the
// ladder, and it must still advance turns rather than divide by zero.
func TestAPoolOfOneStillAdvancesItsOneConversation(t *testing.T) {
	r := newRotation(1, 1)
	for k := range 5 {
		user, turn := r.at(k)
		if user != 0 || turn != k {
			t.Fatalf("arrival %d of a one-conversation pool is (%d,%d), want (0,%d)", k, user, turn, k)
		}
	}
	_ = fmt.Sprint()
}
