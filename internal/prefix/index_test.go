package prefix_test

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/prefix"
)

// clock is a hand-wound clock, so the TTL is tested by expiring entries rather
// than by sleeping through them.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func newClock() *clock { return &clock{at: time.Unix(1_700_000_000, 0)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func newIndex(t *testing.T, cfg prefix.Config) *prefix.Index {
	t.Helper()
	if cfg.NodeCap == 0 {
		cfg.NodeCap = 1024
	}
	if cfg.TTL == 0 {
		cfg.TTL = time.Minute
	}
	ix, err := prefix.New(cfg)
	if err != nil {
		t.Fatalf("New(%+v): %v", cfg, err)
	}
	return ix
}

// matched is what replica held, in bytes, out of a match result.
func matched(matches []prefix.Match, replica string) int {
	for _, m := range matches {
		if m.Replica == replica {
			return m.Bytes
		}
	}
	return 0
}

// The core reading: a replica's prefix match is the longest leading run of
// blocks it is believed to hold, in bytes.
func TestTheIndexReportsTheLongestLeadingRunEachReplicaIsBelievedToHold(t *testing.T) {
	ix := newIndex(t, prefix.Config{})
	conversation := prefix.Blocks(prompt(1, 6, 0))

	// replica-0 saw the whole conversation; replica-1 saw only its first two
	// turns' worth of blocks.
	ix.Admit(conversation, "replica-0")
	ix.Admit(conversation[:2], "replica-1")

	got := ix.Match(conversation)
	if want := 6 * prefix.BlockBytes; matched(got, "replica-0") != want {
		t.Errorf("replica-0 matched %dB, want %dB", matched(got, "replica-0"), want)
	}
	if want := 2 * prefix.BlockBytes; matched(got, "replica-1") != want {
		t.Errorf("replica-1 matched %dB, want %dB", matched(got, "replica-1"), want)
	}
	if matched(got, "replica-2") != 0 {
		t.Errorf("replica-2 was never admitted anything and matched %dB", matched(got, "replica-2"))
	}
}

// Prefix caching is prefix-only. A replica holding the middle of a conversation
// but not its opening can reuse none of it, and an index that credited it with
// a match would send the request to a replica that must prefill the whole
// prompt — strictly worse than sending it to the least loaded one.
func TestABeliefThatSkipsTheOpeningIsNoMatchAtAll(t *testing.T) {
	ix := newIndex(t, prefix.Config{})
	conversation := prefix.Blocks(prompt(1, 5, 0))

	ix.Admit(conversation[2:], "replica-0")

	if got := matched(ix.Match(conversation), "replica-0"); got != 0 {
		t.Errorf("a replica holding blocks 2..4 and not 0..1 matched %dB, want 0", got)
	}
}

// A run has to be contiguous. A hole in the middle truncates the match at the
// hole rather than being counted around.
func TestAHoleTruncatesTheMatchRatherThanBeingCountedAround(t *testing.T) {
	ix := newIndex(t, prefix.Config{})
	conversation := prefix.Blocks(prompt(1, 5, 0))

	ix.Admit(conversation[:2], "replica-0")
	ix.Admit(conversation[3:], "replica-0")

	if got, want := matched(ix.Match(conversation), "replica-0"), 2*prefix.BlockBytes; got != want {
		t.Errorf("match across a hole = %dB, want %dB", got, want)
	}
}

// The index believes for a bounded time. An unbounded belief routes affinity to
// a replica whose cache no longer holds the data, which is worse than routing
// on load alone: the request pays the full prefill anyway and lands on a replica
// chosen for a reason that has stopped being true.
func TestABeliefOlderThanTheTTLIsNotMatched(t *testing.T) {
	tick := newClock()
	ix := newIndex(t, prefix.Config{TTL: 30 * time.Second, Now: tick.Now})
	conversation := prefix.Blocks(prompt(1, 4, 0))

	ix.Admit(conversation, "replica-0")
	tick.advance(29 * time.Second)
	if got := matched(ix.Match(conversation), "replica-0"); got == 0 {
		t.Error("a belief inside the TTL was already forgotten")
	}

	tick.advance(2 * time.Second)
	if got := matched(ix.Match(conversation), "replica-0"); got != 0 {
		t.Errorf("a belief older than the TTL still matched %dB", got)
	}
}

// A conversation that keeps being served keeps its belief alive: the TTL runs
// from when a replica was last believed to hold a block, not from when it first
// was.
func TestServingAConversationAgainRenewsTheBelief(t *testing.T) {
	tick := newClock()
	ix := newIndex(t, prefix.Config{TTL: 30 * time.Second, Now: tick.Now})
	conversation := prefix.Blocks(prompt(1, 4, 0))

	ix.Admit(conversation, "replica-0")
	tick.advance(20 * time.Second)
	ix.Admit(conversation, "replica-0")
	tick.advance(20 * time.Second)

	if got := matched(ix.Match(conversation), "replica-0"); got == 0 {
		t.Error("a conversation served 20s ago was forgotten under a 30s TTL")
	}
}

// Two replicas can hold the same conversation, and their beliefs expire on
// their own clocks. A replica that has not been served for a while must drop out
// of the match without taking the other with it.
func TestOneReplicasBeliefExpiringLeavesTheOthersStanding(t *testing.T) {
	tick := newClock()
	ix := newIndex(t, prefix.Config{TTL: 30 * time.Second, Now: tick.Now})
	conversation := prefix.Blocks(prompt(1, 4, 0))

	ix.Admit(conversation, "replica-0")
	tick.advance(20 * time.Second)
	ix.Admit(conversation, "replica-1")
	tick.advance(15 * time.Second)

	got := ix.Match(conversation)
	if matched(got, "replica-0") != 0 {
		t.Errorf("replica-0's belief was 35s old and still matched %dB", matched(got, "replica-0"))
	}
	if matched(got, "replica-1") == 0 {
		t.Error("replica-1's belief was 15s old and was forgotten")
	}
}

// The cap is the whole reason the index models a fleet rather than a history.
func TestTheIndexIsBoundedByItsNodeCap(t *testing.T) {
	ix := newIndex(t, prefix.Config{NodeCap: 16})

	for session := range 20 {
		ix.Admit(prefix.Blocks(prompt(byte(session), 4, 0)), "replica-0")
	}

	if got := ix.Len(); got > 16 {
		t.Errorf("index holds %d nodes over a cap of 16", got)
	}
}

// Which node the cap discards matters. A shallow block is the way in to every
// deeper block of the same conversation, so evicting it makes the deeper ones
// unreachable — the match walk stops at the first block nobody holds. Eviction
// therefore has to reach the deep end of a conversation before its opening.
func TestTheCapDiscardsTheDeepEndOfAConversationBeforeItsOpening(t *testing.T) {
	// Room for six blocks, and two conversations of four wanting in.
	ix := newIndex(t, prefix.Config{NodeCap: 6})
	first := prefix.Blocks(prompt(1, 4, 0))
	second := prefix.Blocks(prompt(50, 4, 0))

	ix.Admit(first, "replica-0")
	ix.Admit(second, "replica-0")

	got := ix.Match(first)
	if matched(got, "replica-0") == 0 {
		t.Error("the older conversation was evicted opening-first, so none of it can be matched at all")
	}
	if matched(ix.Match(second), "replica-0") != 4*prefix.BlockBytes {
		t.Errorf("the newest conversation was not kept whole: %dB", matched(ix.Match(second), "replica-0"))
	}
}

// Least recently used, so the conversations still being served survive a burst
// of one-shot traffic rather than being flushed by it.
func TestTheCapEvictsTheLeastRecentlyServedConversation(t *testing.T) {
	ix := newIndex(t, prefix.Config{NodeCap: 8})
	hot := prefix.Blocks(prompt(1, 4, 0))
	cold := prefix.Blocks(prompt(50, 4, 0))

	ix.Admit(cold, "replica-0")
	ix.Admit(hot, "replica-0")
	// The hot conversation takes another turn, and then a third conversation
	// arrives needing room.
	ix.Admit(hot, "replica-0")
	ix.Admit(prefix.Blocks(prompt(100, 4, 0)), "replica-0")

	if got := matched(ix.Match(hot), "replica-0"); got != 4*prefix.BlockBytes {
		t.Errorf("the conversation served most recently matched %dB, want %dB", got, 4*prefix.BlockBytes)
	}
	if got := matched(ix.Match(cold), "replica-0"); got != 0 {
		t.Errorf("the least recently served conversation survived at %dB", got)
	}
}

// Matches come back best first, and ties are broken by replica id rather than by
// map iteration order. A policy that took matches[0] would otherwise route the
// same request to a different replica on every call, and no record could explain
// why.
func TestMatchesAreOrderedBestFirstAndTiesBrokenByReplicaID(t *testing.T) {
	ix := newIndex(t, prefix.Config{})
	conversation := prefix.Blocks(prompt(1, 6, 0))

	ix.Admit(conversation[:3], "replica-5")
	ix.Admit(conversation[:3], "replica-2")
	ix.Admit(conversation, "replica-9")
	ix.Admit(conversation[:1], "replica-0")

	want := []string{"replica-9", "replica-2", "replica-5", "replica-0"}
	for range 8 {
		var got []string
		for _, m := range ix.Match(conversation) {
			got = append(got, m.Replica)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("match order = %v, want %v", got, want)
		}
	}
}

// A prompt with no blocks, and a prompt nobody has seen, are both simply no
// match — not an error and not a zero-length entry admitted to the index.
func TestAnUnseenOrTinyPromptIsNoMatchAndAdmitsNothing(t *testing.T) {
	ix := newIndex(t, prefix.Config{})

	if got := ix.Match(prefix.Blocks(prompt(1, 0, 10))); len(got) != 0 {
		t.Errorf("a sub-block prompt matched %v", got)
	}
	ix.Admit(prefix.Blocks(prompt(1, 0, 10)), "replica-0")
	if got := ix.Len(); got != 0 {
		t.Errorf("a sub-block prompt admitted %d nodes", got)
	}
	if got := ix.Match(prefix.Blocks(prompt(9, 3, 0))); len(got) != 0 {
		t.Errorf("an unseen conversation matched %v", got)
	}
}

// The router is concurrent by construction — it is the sole ingress to six
// replicas — so the index is used from many goroutines at once. Run under -race.
func TestTheIndexIsSafeUnderConcurrentUse(t *testing.T) {
	ix := newIndex(t, prefix.Config{NodeCap: 256})

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for turn := range 50 {
				chain := prefix.Blocks(prompt(byte(worker), 1+turn%6, 0))
				ix.Match(chain)
				ix.Admit(chain, fmt.Sprintf("replica-%d", worker%6))
			}
		}()
	}
	wg.Wait()

	if ix.Len() > 256 {
		t.Errorf("index holds %d nodes over a cap of 256", ix.Len())
	}
}

// Both bounds are required. An index built without them is an index that
// believes everything for ever, and the failure mode of that is not a crash but
// a policy quietly routing on stale beliefs.
func TestAnIndexRefusesToBeBuiltWithoutBothBounds(t *testing.T) {
	for _, cfg := range []prefix.Config{
		{TTL: time.Minute},
		{NodeCap: 100},
		{},
	} {
		if _, err := prefix.New(cfg); err == nil {
			t.Errorf("New(%+v) built an unbounded index", cfg)
		}
	}
}
