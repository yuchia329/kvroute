package prefix_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/yuchia329/kvroute/internal/prefix"
)

// prompt is a conversation of n blocks plus tail bytes, filled so that no two
// positions carry the same bytes and a chain cannot pass by luck.
func prompt(seed byte, blocks, tail int) []byte {
	b := make([]byte, blocks*prefix.BlockBytes+tail)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return b
}

// The property every later turn of a conversation rests on: turn N resends
// turns 1..N-1 unchanged, so the blocks it shares with turn N-1 have to hash to
// the same values. A chain that rehashed the whole prompt would produce a fresh
// set of hashes on every turn, and the index would believe no replica held
// anything a conversation had already sent it.
func TestAGrowingConversationExtendsItsChainRatherThanReplacingIt(t *testing.T) {
	turn1 := prompt(1, 4, 0)
	turn2 := append(slices.Clone(turn1), prompt(9, 3, 0)...)

	first, second := prefix.Blocks(turn1), prefix.Blocks(turn2)
	if len(first) != 4 || len(second) != 7 {
		t.Fatalf("chain lengths = %d and %d, want 4 and 7", len(first), len(second))
	}
	if !slices.Equal(second[:len(first)], first) {
		t.Errorf("the second turn rehashed its history:\n first  = %v\n second = %v", first, second[:len(first)])
	}
}

// Chained, not per-block: a block's hash covers every byte before it. Without
// that, two conversations that happen to share a later block would be credited
// with a match they do not have — prefix caching is prefix-only, and a match
// that did not start at byte zero would be a belief about a cache that works
// some other way.
func TestABlockHashCoversEveryByteBeforeIt(t *testing.T) {
	original := prompt(1, 3, 0)
	altered := slices.Clone(original)
	altered[0]++

	first, second := prefix.Blocks(original), prefix.Blocks(altered)
	for i := range first {
		if first[i] == second[i] {
			t.Errorf("block %d hashed the same after the first byte of the prompt changed", i)
		}
	}
}

// Two conversations that open alike and then diverge share exactly the blocks
// they actually share. This is the shape the index's longest-chain match reads.
func TestTwoConversationsShareOnlyTheBlocksTheyBothSent(t *testing.T) {
	shared := prompt(1, 2, 0)
	left := prefix.Blocks(append(slices.Clone(shared), prompt(40, 2, 0)...))
	right := prefix.Blocks(append(slices.Clone(shared), prompt(80, 2, 0)...))

	if !slices.Equal(left[:2], right[:2]) {
		t.Errorf("the shared opening did not hash alike: %v vs %v", left[:2], right[:2])
	}
	if left[2] == right[2] {
		t.Errorf("the diverging block hashed alike: %v", left[2])
	}
}

// A prompt's trailing bytes are not a block. They would hash to a value that
// changes as soon as the conversation grows past them, so every turn would
// leave one node behind that no later turn can ever match — an index filling
// with entries that exist only to be evicted.
func TestATrailingPartialBlockIsNotChained(t *testing.T) {
	if got := prefix.Blocks(prompt(1, 0, prefix.BlockBytes-1)); len(got) != 0 {
		t.Errorf("a prompt shorter than one block chained %d blocks, want 0", len(got))
	}
	whole := prefix.Blocks(prompt(1, 3, 0))
	ragged := prefix.Blocks(prompt(1, 3, prefix.BlockBytes-1))
	if !slices.Equal(whole, ragged) {
		t.Errorf("the trailing bytes changed the chain:\n whole  = %v\n ragged = %v", whole, ragged)
	}
}

// The blocks are the bytes the client sent, in order, with nothing skipped.
// Stated as a test because the alternative — hashing a normalised or re-rendered
// prompt — would need a tokenizer and a byte-exact chat template, which §4.3
// keeps off the request path deliberately.
func TestTheChainCoversThePromptsLeadingBytesInOrder(t *testing.T) {
	body := prompt(1, 5, 3)
	chain := prefix.Blocks(body)
	if want := len(body) / prefix.BlockBytes; len(chain) != want {
		t.Fatalf("chained %d blocks of a %d byte prompt, want %d", len(chain), len(body), want)
	}
	// Rechaining the leading bytes alone must produce the same values, which is
	// only true if each hash covers a leading run of the prompt.
	for i := 1; i <= len(chain); i++ {
		leading := prefix.Blocks(body[:i*prefix.BlockBytes])
		if !slices.Equal(leading, chain[:i]) {
			t.Errorf("the first %d blocks rechained differently: %v vs %v", i, leading, chain[:i])
		}
	}
}

// The chain is a function of the bytes and nothing else: no per-process seed, no
// clock. Two routers over one fleet, and one router across a restart, have to
// agree about what a conversation's blocks are.
func TestTheChainIsAFunctionOfTheBytesAlone(t *testing.T) {
	body := bytes.Repeat([]byte("the quick brown fox "), 20)
	if !slices.Equal(prefix.Blocks(body), prefix.Blocks(slices.Clone(body))) {
		t.Error("the same bytes chained differently twice")
	}
}
