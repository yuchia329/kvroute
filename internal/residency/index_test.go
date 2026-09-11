package residency_test

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/yuchia329/kvroute/internal/kvevents"
	"github.com/yuchia329/kvroute/internal/residency"
)

const blockSize = 16

// block returns sixteen tokens that no other call with a different seed
// returns, so a test can build prompts that share exactly the blocks it says
// they share.
func block(seed uint32) []uint32 {
	out := make([]uint32, blockSize)
	for i := range out {
		out[i] = seed*1000 + uint32(i)
	}
	return out
}

func index(t *testing.T, replicas ...string) *residency.Index {
	t.Helper()
	ix, err := residency.New(blockSize, replicas)
	if err != nil {
		t.Fatalf("residency.New: %v", err)
	}
	return ix
}

// stored is a BlockStored event as the engine sends one: its own hashes for the
// blocks, the hash of the block the run hangs off, and the tokens.
func stored(parent *uint64, hashes []uint64, tokens ...[]uint32) kvevents.Event {
	return kvevents.Event{
		Kind:        kvevents.Stored,
		BlockHashes: hashes,
		Parent:      parent,
		TokenIDs:    slices.Concat(tokens...),
		BlockSize:   blockSize,
		Medium:      "GPU",
	}
}

func removed(hashes ...uint64) kvevents.Event {
	return kvevents.Event{Kind: kvevents.Removed, BlockHashes: hashes, Medium: "GPU"}
}

func cleared() kvevents.Event { return kvevents.Event{Kind: kvevents.Cleared} }

func batch(events ...kvevents.Event) kvevents.Batch { return kvevents.Batch{Events: events} }

func matchOf(t *testing.T, ix *residency.Index, replica string, prompt []uint32) int {
	t.Helper()
	for _, m := range ix.Match(prompt) {
		if m.Replica == replica {
			return m.Blocks
		}
	}
	return 0
}

// The capability the whole policy rests on: once the engine says it stored a
// prompt's blocks, that prompt matches that replica for exactly those blocks.
func TestAPromptTheEngineStoredMatchesTheReplicaThatStoredIt(t *testing.T) {
	ix := index(t, "replica-0", "replica-1")
	ix.Apply("replica-0", batch(stored(nil, []uint64{101, 102}, block(1), block(2))))

	// The prompt goes on past what was stored, into a block the engine has not
	// filled, and ends partway through another.
	prompt := slices.Concat(block(1), block(2), block(3), block(4)[:5])
	got := ix.Match(prompt)

	want := []residency.Match{{Replica: "replica-0", Blocks: 2, Tokens: 32}}
	if !slices.Equal(got, want) {
		t.Errorf("Match = %+v, want %+v", got, want)
	}
}

// A session's next turn is stored as a run continuing the blocks its last turn
// left. The engine names the block it continues by its own hash, so the
// index has to have remembered what that name referred to.
func TestARunThatContinuesAStoredBlockExtendsTheMatch(t *testing.T) {
	ix := index(t, "replica-0")
	ix.Apply("replica-0", batch(stored(nil, []uint64{101}, block(1))))
	parent := uint64(101)
	ix.Apply("replica-0", batch(stored(&parent, []uint64{102, 103}, block(2), block(3))))

	got := ix.Match(slices.Concat(block(1), block(2), block(3)))
	want := []residency.Match{{Replica: "replica-0", Blocks: 3, Tokens: 48}}
	if !slices.Equal(got, want) {
		t.Errorf("Match = %+v, want %+v", got, want)
	}
}

// A block hanging off a parent the index never heard of cannot be placed: its
// tokens say what it holds, not what came before them, and a prefix cache only
// serves whole leading runs. It is left out rather than guessed at. That can
// only make the index forget something the engine holds, which forfeits a match
// — never invent one, the direction ADR-0006 calls strictly worse than routing
// on load.
func TestABlockWhoseParentWasNeverReportedIsNotBelieved(t *testing.T) {
	ix := index(t, "replica-0")
	unseen := uint64(999)
	ix.Apply("replica-0", batch(stored(&unseen, []uint64{102}, block(2))))

	if got := ix.Match(slices.Concat(block(1), block(2))); len(got) != 0 {
		t.Errorf("a run continuing an unreported block matched: %+v", got)
	}
	if got := ix.Match(block(2)); len(got) != 0 {
		t.Errorf("the orphaned block matched as though it opened a prompt: %+v", got)
	}
}

// Two prompts that share an opening and then part share exactly the opening,
// and one token different in the first block means they share nothing.
func TestAMatchEndsWhereThePromptLeavesWhatWasStored(t *testing.T) {
	ix := index(t, "replica-0")
	ix.Apply("replica-0", batch(stored(nil, []uint64{101, 102, 103}, block(1), block(2), block(3))))

	got := ix.Match(slices.Concat(block(1), block(2), block(9)))
	want := []residency.Match{{Replica: "replica-0", Blocks: 2, Tokens: 32}}
	if !slices.Equal(got, want) {
		t.Errorf("Match = %+v, want %+v", got, want)
	}

	altered := slices.Concat(block(1), block(2))
	altered[7]++
	if got := ix.Match(altered); len(got) != 0 {
		t.Errorf("a prompt differing in its first block matched: %+v", got)
	}
}

// A block the engine lets go ends the match there, even though the blocks after
// it may still be resident: nothing can reach them without it. And it is gone
// from that replica only.
func TestARemovedBlockEndsTheMatchWhereItWas(t *testing.T) {
	ix := index(t, "replica-0", "replica-1")
	three := batch(stored(nil, []uint64{101, 102, 103}, block(1), block(2), block(3)))
	ix.Apply("replica-0", three)
	ix.Apply("replica-1", three)

	ix.Apply("replica-0", batch(removed(102)))

	prompt := slices.Concat(block(1), block(2), block(3))
	if got := matchOf(t, ix, "replica-0", prompt); got != 1 {
		t.Errorf("replica-0 matched %d blocks after losing its second, want 1", got)
	}
	if got := matchOf(t, ix, "replica-1", prompt); got != 3 {
		t.Errorf("replica-1 matched %d blocks, want all 3: the removal was replica-0's", got)
	}
}

func TestRemovingTheFirstBlockLeavesNoMatch(t *testing.T) {
	ix := index(t, "replica-0")
	ix.Apply("replica-0", batch(stored(nil, []uint64{101, 102}, block(1), block(2))))
	ix.Apply("replica-0", batch(removed(101)))

	if got := ix.Match(slices.Concat(block(1), block(2))); len(got) != 0 {
		t.Errorf("a prompt matched with its first block evicted: %+v", got)
	}
}

// A request that asks for full reporting has the blocks it reused out of the
// cache reported stored again, with no removal to pair with the second report.
// Holding is a fact rather than a count: however often a block is reported
// stored, one removal means it is gone. Counting the reports would keep
// believing in a block long after the engine let it go.
func TestABlockReportedStoredTwiceIsGoneAfterOneRemoval(t *testing.T) {
	ix := index(t, "replica-0")
	ix.Apply("replica-0", batch(stored(nil, []uint64{101}, block(1))))
	ix.Apply("replica-0", batch(stored(nil, []uint64{101}, block(1))))
	ix.Apply("replica-0", batch(removed(101)))

	if got := ix.Match(block(1)); len(got) != 0 {
		t.Errorf("a block re-reported once and removed once still matched: %+v", got)
	}
}

func TestAClearForgetsEverythingThatReplicaHeld(t *testing.T) {
	ix := index(t, "replica-0", "replica-1")
	one := batch(stored(nil, []uint64{101}, block(1)))
	ix.Apply("replica-0", one)
	ix.Apply("replica-1", one)

	ix.Apply("replica-0", batch(cleared()))

	want := []residency.Match{{Replica: "replica-1", Blocks: 1, Tokens: 16}}
	if got := ix.Match(block(1)); !slices.Equal(got, want) {
		t.Errorf("Match = %+v, want %+v", got, want)
	}
}

// Reset is the router's own clear, for when the stream has lost history and
// what the index holds for a replica may include blocks the engine has since let
// go. It forgets that replica's names for its blocks as well as the blocks, so a
// later run hanging off one of them cannot be placed on a chain the index no
// longer vouches for.
func TestAResetForgetsOnlyThatReplicaAndItsNamesForBlocks(t *testing.T) {
	ix := index(t, "replica-0", "replica-1")
	one := batch(stored(nil, []uint64{101}, block(1)))
	ix.Apply("replica-0", one)
	ix.Apply("replica-1", one)

	ix.Reset("replica-0")

	if got := matchOf(t, ix, "replica-0", block(1)); got != 0 {
		t.Errorf("replica-0 still matched %d blocks after a reset", got)
	}
	if got := matchOf(t, ix, "replica-1", block(1)); got != 1 {
		t.Errorf("replica-1 matched %d blocks, want 1: the reset was replica-0's", got)
	}

	parent := uint64(101)
	ix.Apply("replica-0", batch(stored(&parent, []uint64{102}, block(2))))
	if got := matchOf(t, ix, "replica-0", slices.Concat(block(1), block(2))); got != 0 {
		t.Errorf("a run continuing a block from before the reset matched %d blocks", got)
	}
}

// A block computed under a LoRA adapter hashes differently from the same tokens
// without one, so no prompt identified by its tokens alone can be served from
// it. Believing in it would predict a hit the engine cannot give.
func TestBlocksStoredUnderAnAdapterAreNotBelieved(t *testing.T) {
	ix := index(t, "replica-0")
	e := stored(nil, []uint64{101}, block(1))
	e.LoRA = true
	ix.Apply("replica-0", batch(e))

	if got := ix.Match(block(1)); len(got) != 0 {
		t.Errorf("a block stored under an adapter matched: %+v", got)
	}
}

// A block whose hash covers keys beyond its tokens cannot be matched by tokens,
// and neither can anything after it: every later block's hash chains through it.
// So a run is believed up to its first keyed block and no further — including by
// a later run that names one of the unbelieved blocks as its parent.
func TestARunIsBelievedOnlyUpToItsFirstKeyedBlock(t *testing.T) {
	ix := index(t, "replica-0")
	e := stored(nil, []uint64{101, 102, 103}, block(1), block(2), block(3))
	e.ExtraKeys = []bool{false, true, false}
	ix.Apply("replica-0", batch(e))

	prompt := slices.Concat(block(1), block(2), block(3), block(4))
	if got := matchOf(t, ix, "replica-0", prompt); got != 1 {
		t.Errorf("matched %d blocks, want 1: the second is keyed", got)
	}

	parent := uint64(103)
	ix.Apply("replica-0", batch(stored(&parent, []uint64{104}, block(4))))
	if got := matchOf(t, ix, "replica-0", prompt); got != 1 {
		t.Errorf("matched %d blocks after a run continued past the keyed block, want 1", got)
	}
}

// Residency here is the device's own cache. A block held in a CPU tier is one a
// request would have to wait to load back, and a removal from that tier says
// nothing about the GPU's copy.
func TestOnlyTheGPUTierCounts(t *testing.T) {
	ix := index(t, "replica-0")
	offloaded := stored(nil, []uint64{201}, block(5))
	offloaded.Medium = "CPU"
	ix.Apply("replica-0", batch(offloaded))
	if got := ix.Match(block(5)); len(got) != 0 {
		t.Errorf("a block held only in the CPU tier matched: %+v", got)
	}

	ix.Apply("replica-0", batch(stored(nil, []uint64{101}, block(1))))
	fromCPU := removed(101)
	fromCPU.Medium = "CPU"
	ix.Apply("replica-0", batch(fromCPU))
	if got := matchOf(t, ix, "replica-0", block(1)); got != 1 {
		t.Errorf("matched %d blocks, want 1: the removal was from the CPU tier", got)
	}
}

// An event whose tokens do not fill its blocks at the engine's block size is one
// the index cannot place. It is refused whole rather than read out of bounds or
// placed with the wrong tokens.
func TestAStoreWhoseTokensDoNotFillItsBlocksIsRefused(t *testing.T) {
	ix := index(t, "replica-0")
	prompt := slices.Concat(block(1), block(2))

	short := stored(nil, []uint64{101, 102}, block(1), block(2)[:4])
	ix.Apply("replica-0", batch(short))
	if got := ix.Match(prompt); len(got) != 0 {
		t.Errorf("a store short of its tokens matched: %+v", got)
	}

	wide := stored(nil, []uint64{101}, block(1), block(2))
	wide.BlockSize = 2 * blockSize
	ix.Apply("replica-0", batch(wide))
	if got := ix.Match(prompt); len(got) != 0 {
		t.Errorf("a store at another block size matched: %+v", got)
	}
}

// The router reports how much each replica's engine holds, so that a run can
// show its exact index was fed at all. A policy whose index is empty routes
// everything cold, and the decision mix would say so without saying why.
func TestStatsReportTheBlocksEachReplicaHolds(t *testing.T) {
	ix := index(t, "replica-0", "replica-1")
	ix.Apply("replica-0", batch(stored(nil, []uint64{101, 102}, block(1), block(2))))
	ix.Apply("replica-1", batch(stored(nil, []uint64{201}, block(1))))
	ix.Apply("replica-0", batch(removed(102)))

	want := []residency.ReplicaStats{
		{Replica: "replica-0", Blocks: 1},
		{Replica: "replica-1", Blocks: 1},
	}
	if got := ix.Stats(); !slices.Equal(got, want) {
		t.Errorf("Stats = %+v, want %+v", got, want)
	}
}

// Runs the index could not place are counted by why, because the two reasons
// mean different things. An orphan is history the router never saw — the
// cold-start limitation, measured — and a refusal is a block no prompt
// identified by its tokens could ever have matched.
func TestStatsCountTheRunsThatCouldNotBePlacedByWhy(t *testing.T) {
	ix := index(t, "replica-0")
	unseen := uint64(999)
	ix.Apply("replica-0", batch(stored(&unseen, []uint64{102}, block(2))))
	adapter := stored(nil, []uint64{103}, block(3))
	adapter.LoRA = true
	ix.Apply("replica-0", batch(adapter))

	got := ix.Stats()[0]
	if got.Orphaned != 1 {
		t.Errorf("orphaned = %d runs, want 1", got.Orphaned)
	}
	if got.Refused != 1 {
		t.Errorf("refused = %d runs, want 1", got.Refused)
	}
	if got.Blocks != 0 {
		t.Errorf("blocks = %d, want 0: neither run was placed", got.Blocks)
	}
}

// Every replica's stream applies its own events while every request matches
// against all of them. Run under -race.
func TestTheIndexIsSafeUnderConcurrentUse(t *testing.T) {
	ix := index(t, "replica-0", "replica-1", "replica-2")
	var wg sync.WaitGroup
	for r := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("replica-%d", r)
			for i := range 200 {
				name := uint64(r*1000 + i)
				ix.Apply(id, batch(stored(nil, []uint64{name}, block(uint32(i)))))
				if i%3 == 0 {
					ix.Apply(id, batch(removed(name)))
				}
				if i%50 == 0 {
					ix.Reset(id)
				}
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				ix.Match(block(uint32(i)))
				ix.Stats()
			}
		}()
	}
	wg.Wait()
}

func TestARemovalOfABlockNeverReportedChangesNothing(t *testing.T) {
	ix := index(t, "replica-0")
	ix.Apply("replica-0", batch(stored(nil, []uint64{101}, block(1))))
	ix.Apply("replica-0", batch(removed(555)))

	if got := matchOf(t, ix, "replica-0", block(1)); got != 1 {
		t.Errorf("replica-0 matched %d blocks, want 1", got)
	}
}
