// Package prefix is the router's belief about which replicas hold which parts
// of which conversation, and the chunking that belief is expressed in.
//
// It is a belief and not a fact. The replicas own their KV caches and evict
// from them on their own schedule, so everything here is a model of state this
// process does not control — which is why the index expires entries and is
// bounded rather than accumulating, and why the gap between what it predicts
// and what the engine actually held is itself a measured quantity.
//
// No tokenizer. Routing needs only self-consistency: the index ranks replicas
// against each other using its own hashes, so it never has to agree with vLLM
// about where a block boundary falls. Mirroring the engine's block size gives
// comparable granularity, and hash-level compatibility would cost cgo plus
// byte-exact chat template rendering for a ranking that does not need it. The
// consequence is that a prefix match is a length in bytes and is reported as
// one; see CONTEXT.md, which keeps prefix match and prefix cache hit rate as
// two separate terms for exactly this reason.
package prefix

// BlockBytes is the size of one prefix block.
//
// Sixty-four bytes is roughly sixteen tokens at this workload's measured prompt
// bytes per token, which is vLLM 0.28.0's own default block size — so the
// router's granularity is comparable to the engine's without being aligned to
// it. Smaller blocks would track a prompt more finely at a cost paid on every
// request in both index nodes and hash steps; larger ones would round a real
// match down to nothing.
const BlockBytes = 64

// Chain is a prompt's leading blocks as hashes, each covering every byte before
// it as well as its own.
//
// Chained rather than independent per-block hashes, because prefix caching is
// prefix-only: an engine that holds blocks 0..3 of a conversation holds them as
// a leading run, and two prompts that share only their fourth block share
// nothing an engine can reuse. A chained hash makes that structural — a node in
// the index identifies one whole leading run of one conversation, so a match
// that did not start at byte zero cannot be expressed, let alone believed.
type Chain []uint64

// Blocks chunks a prompt into fixed byte blocks and chains their hashes.
//
// The trailing bytes that do not fill a block are dropped. They would hash to a
// value that the conversation's next turn immediately invalidates by growing
// past it, so every turn would leave behind one node no later turn can ever
// match: an index filling with entries whose only future is eviction.
func Blocks(body []byte) Chain {
	count := len(body) / BlockBytes
	if count == 0 {
		return nil
	}
	chain := make(Chain, 0, count)
	// One running hash across the whole prompt, sampled at each block boundary,
	// is what makes the chain a chain: block i's value is the hash of bytes
	// 0..(i+1)*BlockBytes, so it cannot collide with the same block sent under a
	// different history.
	h := offset64
	for i := range count {
		for _, b := range body[i*BlockBytes : (i+1)*BlockBytes] {
			h = (h ^ uint64(b)) * prime64
		}
		chain = append(chain, h)
	}
	return chain
}

// FNV-1a's parameters. FNV rather than a cryptographic hash because this is an
// index key and nothing here is adversarial, and rather than Go's maphash
// because that is seeded per process: two routers over one fleet, and one
// router across a restart, have to agree about what a conversation's blocks
// are.
//
// It is used raw here, without the avalanche step the hash ring needs. The
// ring's keys are short and near-identical — "user-0", "user-1" — which FNV
// diffuses poorly; a block key is 64 bytes of prompt folded through 64 rounds,
// which is the case FNV is good at.
const (
	offset64 = uint64(14695981039346656037)
	prime64  = uint64(1099511628211)
)
