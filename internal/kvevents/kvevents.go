// Package kvevents reads the stream of KV cache events a vLLM replica
// publishes: which blocks of which prompts its prefix cache now holds, and which
// it has let go.
//
// It is the engine's own account of its cache, where the prefix index is the
// router's guess at it from its own dispatch history. Consuming it gives exact
// residency rather than a belief, at the cost of depending on the engine to
// publish — CONTEXT.md's KV cache event.
//
// The protocol is vLLM 0.28.0's, pinned like everything else about the engine
// (ADR-0001): a ZeroMQ PUB socket carrying msgpack-encoded batches, with a
// ROUTER socket beside it that replays recent batches to a subscriber that
// missed some. Only the subset of each that this router reads is implemented
// here, and it is tested against the engine's own encoder (testdata/gen.py) and
// a real libzmq rather than against itself.
package kvevents

// Kind is which of the three things an event says happened to the cache.
type Kind uint8

const (
	// Stored is blocks entering the prefix cache: the engine can now serve
	// their tokens without computing them.
	Stored Kind = iota + 1
	// Removed is blocks leaving it.
	Removed
	// Cleared is the whole cache emptied at once.
	Cleared
)

func (k Kind) String() string {
	switch k {
	case Stored:
		return "BlockStored"
	case Removed:
		return "BlockRemoved"
	case Cleared:
		return "AllBlocksCleared"
	default:
		return "unknown"
	}
}

// Batch is one message off the stream: every event one engine step produced,
// in the order the engine queued them.
type Batch struct {
	// Seq is the publisher's sequence number for this batch. It travels in the
	// message's framing rather than in its payload, so Decode leaves it zero and
	// the stream fills it in.
	Seq uint64
	// TS is the engine's wall clock when it published the batch, in seconds.
	TS     float64
	Events []Event
}

// Event is one change to what a replica's cache holds.
type Event struct {
	Kind Kind
	// BlockHashes are the engine's own hashes of the blocks, in prompt order.
	// Each is the low 64 bits of the engine's digest, which is the form it sends
	// by default.
	BlockHashes []uint64
	// Parent is the hash of the block before the first of BlockHashes, or nil
	// when the run starts at the beginning of its prompt. Stored events only.
	Parent *uint64
	// TokenIDs are the tokens the stored blocks hold, BlockSize to a block and
	// in prompt order. Stored events only.
	TokenIDs  []uint32
	BlockSize int
	// Medium is the tier the blocks live in: "GPU" for the device's own cache.
	// Empty when the engine did not say.
	Medium string
	// LoRA says the blocks were computed under a LoRA adapter, and ExtraKeys
	// says, block by block, whether a block's hash covers keys beyond its tokens
	// — a cache salt, multimodal inputs, prompt embeddings. Nil when no block
	// has any. Stored events only.
	//
	// Both are reported because either makes a block one that no prompt
	// identified by its tokens alone can match, however alike the tokens: the
	// engine would compute a different hash for that prompt and find nothing.
	LoRA      bool
	ExtraKeys []bool
}
