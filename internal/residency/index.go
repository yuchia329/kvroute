// Package residency is what each replica's prefix cache holds, as the replica's
// own engine reported it.
//
// It is the exact counterpart of package prefix. That index is a belief built
// from the router's own dispatch history, bounded by a TTL and a node cap
// because nothing tells it when a replica evicts; this one is built from the KV
// cache events the engines publish (package kvevents), so it holds a block from
// the moment the engine says it stored it until the moment the engine says it
// let it go, and needs neither bound. What the approximation costs is the
// comparison between the two (#24).
//
// It is keyed by tokens, not bytes, because the engine's cache is: a prompt is
// matched here by the token ids the engine itself would compute for it, in the
// engine's own block size, so a match is a count of the engine's tokens rather
// than a length of prompt that has to be converted.
package residency

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/yuchia329/kvroute/internal/kvevents"
)

// Match is one replica's prefix match: the longest leading run of a prompt's
// blocks its engine reported holding.
type Match struct {
	Replica string
	Blocks  int
	// Tokens is Blocks in the engine's own units. It is a count the engine can
	// be held to — its usage block reports the cached tokens of the same request
	// — rather than a prediction that needs converting first.
	Tokens int
}

// Index maps prompts to the replicas whose engines hold them.
type Index struct {
	blockSize int

	mu sync.RWMutex
	// held is every chain some replica holds, with one bit per replica holding
	// it. A chain names one whole leading run of one prompt's tokens (see
	// extend), so holding a chain means holding every block before it too, which
	// is the only kind of holding a prefix cache can use.
	held     map[uint64]uint64
	replicas map[string]*replica
	// names is the replicas in bit order.
	names []string
}

// replica is what the index knows about one engine's cache.
type replica struct {
	bit uint64
	// chains maps the engine's own name for a block to the chain it ends. The
	// engine names blocks by a hash the router cannot compute — it is seeded per
	// process — so the only way to know which chain a removal refers to, or which
	// run a stored block continues, is to remember what the engine called it.
	chains map[uint64]uint64
	// orphaned and refused count the stored runs that could not be placed; see
	// ReplicaStats.
	orphaned, refused uint64
}

// ReplicaStats is what the index holds for one replica, and what it could not
// place.
type ReplicaStats struct {
	Replica string `json:"replica"`
	// Blocks is how many of the engine's blocks the index holds for it.
	Blocks int `json:"blocks"`
	// Orphaned is stored runs that continued a block this index never heard
	// of: history from before the router subscribed, or history it lost. The
	// engine holds them and the index cannot place them, so they are matches the
	// policy forfeits — the cold-start limitation, counted.
	Orphaned uint64 `json:"orphaned"`
	// Refused is stored runs, or the tails of them, that no prompt identified
	// by its tokens could match: another tier, a LoRA adapter, extra keys, or a
	// block size other than the engine's.
	Refused uint64 `json:"refused"`
}

// Stats reports each replica's residency, in the order New was given them.
func (ix *Index) Stats() []ReplicaStats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]ReplicaStats, 0, len(ix.names))
	for _, id := range ix.names {
		r := ix.replicas[id]
		out = append(out, ReplicaStats{Replica: id, Blocks: len(r.chains), Orphaned: r.orphaned, Refused: r.refused})
	}
	return out
}

// New builds an index over these replicas, for an engine whose blocks hold
// blockSize tokens.
func New(blockSize int, replicas []string) (*Index, error) {
	if blockSize <= 0 {
		return nil, fmt.Errorf("residency: block size %d: it is the engine's own, and a replica reports it", blockSize)
	}
	if len(replicas) == 0 {
		return nil, errors.New("residency: at least one replica is required")
	}
	if len(replicas) > 64 {
		return nil, fmt.Errorf("residency: %d replicas, and the index holds one bit per replica in a 64-bit word", len(replicas))
	}
	ix := &Index{
		blockSize: blockSize,
		held:      map[uint64]uint64{},
		replicas:  make(map[string]*replica, len(replicas)),
		names:     slices.Clone(replicas),
	}
	for i, id := range replicas {
		if _, dup := ix.replicas[id]; dup {
			return nil, fmt.Errorf("residency: replica %q named twice", id)
		}
		ix.replicas[id] = &replica{bit: 1 << i, chains: map[uint64]uint64{}}
	}
	return ix, nil
}

// Apply folds one batch of a replica's events into what it holds, in the order
// the engine queued them.
func (ix *Index) Apply(id string, batch kvevents.Batch) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	r, known := ix.replicas[id]
	if !known {
		return
	}
	for _, e := range batch.Events {
		switch e.Kind {
		case kvevents.Stored:
			ix.store(r, e)
		case kvevents.Removed:
			ix.remove(r, e)
		case kvevents.Cleared:
			ix.forget(r)
		}
	}
}

// Reset forgets everything one replica holds.
//
// It is for when the stream has lost history. What the index holds for that
// replica may then include blocks the engine has since let go, and believing in
// one of those would send a request to a replica that must prefill it anyway.
// Forgetting is the safe direction: whatever the engine still holds, it will
// report again as requests reuse it.
func (ix *Index) Reset(id string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if r, known := ix.replicas[id]; known {
		ix.forget(r)
	}
}

// remove drops blocks the engine has let go.
//
// Holding is a fact rather than a count, so one removal ends it however many
// times the block was reported stored. A request that asks for full reporting
// has every block it reused from the cache reported stored again, with no
// removal to pair with the report, and a count would keep such a block believed
// after the engine let it go. The price is a duplicate — two copies of one
// block, each stored and removed with events of their own — which is forgotten
// when either copy goes (ADR-0010).
func (ix *Index) remove(r *replica, e kvevents.Event) {
	// A block leaving another tier says nothing about the device's copy.
	if !onDevice(e.Medium) {
		return
	}
	for _, name := range e.BlockHashes {
		chain, known := r.chains[name]
		if !known {
			continue
		}
		delete(r.chains, name)
		ix.release(r, chain)
	}
}

// forget drops everything one replica holds, and its names for all of it.
func (ix *Index) forget(r *replica) {
	for _, chain := range r.chains {
		ix.release(r, chain)
	}
	clear(r.chains)
}

// onDevice reports whether a tier is the GPU's own cache. Residency here is what
// a request can be served from without loading anything back, and a block the
// engine moved to a CPU tier is not that. An engine that names no tier has no
// other tier.
func onDevice(medium string) bool { return medium == "" || medium == "GPU" }

// release takes one replica's bit off a chain, and the chain out of the index
// once nobody holds it.
func (ix *Index) release(r *replica, chain uint64) {
	if left := ix.held[chain] &^ r.bit; left != 0 {
		ix.held[chain] = left
	} else {
		delete(ix.held, chain)
	}
}

// store records a run of blocks the engine now holds.
func (ix *Index) store(r *replica, e kvevents.Event) {
	// A store this index could only misplace is refused whole: blocks in
	// another tier, blocks whose hash covers an adapter the prompt does not
	// name, and blocks that are not the engine's blocks at the size the prompt
	// is chunked in. Each would otherwise be believed as something it is not.
	if !onDevice(e.Medium) || e.LoRA || e.BlockSize != ix.blockSize ||
		len(e.TokenIDs) != len(e.BlockHashes)*ix.blockSize ||
		(e.ExtraKeys != nil && len(e.ExtraKeys) != len(e.BlockHashes)) {
		r.refused++
		return
	}
	chain := root
	if e.Parent != nil {
		parent, known := r.chains[*e.Parent]
		if !known {
			// Stored after a block this index never heard of — before it
			// subscribed, or in history it lost. Without the parent's chain
			// these tokens cannot be placed in any prompt, so the run is left
			// out: forgetting a block forfeits a match, and guessing one
			// could invent a match the engine does not have.
			r.orphaned++
			return
		}
		chain = parent
	}
	for i, name := range e.BlockHashes {
		if e.ExtraKeys != nil && e.ExtraKeys[i] {
			// This block's hash covers more than its tokens, and every later
			// block's hash chains through it, so neither it nor anything after
			// it can be matched by tokens. The run is believed up to here.
			r.refused++
			return
		}
		chain = extend(chain, e.TokenIDs[i*ix.blockSize:(i+1)*ix.blockSize])
		r.chains[name] = chain
		ix.held[chain] |= r.bit
	}
}

// Match returns each replica's prefix match against a prompt's tokens, longest
// first, ties broken by replica id.
//
// A replica appears only if it holds the prompt's first block, and its match
// ends at the first block it does not hold: the engine's prefix cache serves a
// leading run and nothing else. The order is total so that a policy taking the
// head of the list makes the same choice for the same state every time.
func (ix *Index) Match(tokens []uint32) []Match {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	// alive is the replicas whose run is still unbroken. One lookup per block
	// serves every replica at once, because held carries them all as bits.
	alive := uint64(1)<<len(ix.names) - 1
	runs := make([]int, len(ix.names))
	chain := root
	for depth := 0; (depth+1)*ix.blockSize <= len(tokens); depth++ {
		chain = extend(chain, tokens[depth*ix.blockSize:(depth+1)*ix.blockSize])
		alive &= ix.held[chain]
		if alive == 0 {
			break
		}
		for i := range runs {
			if alive&(1<<i) != 0 {
				runs[i] = depth + 1
			}
		}
	}

	var matches []Match
	for i, blocks := range runs {
		if blocks > 0 {
			matches = append(matches, Match{Replica: ix.names[i], Blocks: blocks, Tokens: blocks * ix.blockSize})
		}
	}
	slices.SortFunc(matches, func(a, b Match) int {
		return cmp.Or(cmp.Compare(b.Blocks, a.Blocks), cmp.Compare(a.Replica, b.Replica))
	})
	return matches
}

// extend continues a chain by one block of tokens.
//
// A chain is FNV-1a run over a prompt's tokens from the first, sampled at each
// block boundary, so its value names the whole leading run and not just the
// block that ends it. FNV's running state is its value, which is what lets a
// chain be continued from its parent's hash alone — and that is exactly what a
// stored event provides: the engine's name for the parent block, never the
// parent's tokens.
//
// FNV rather than the engine's own hash, which the router cannot reproduce: it
// is seeded per engine process. Both sides of a match here are hashed by this
// one function — the engine's reported tokens as they arrive, the prompt's
// tokens as they are matched — so it only has to agree with itself.
func extend(chain uint64, tokens []uint32) uint64 {
	for _, t := range tokens {
		for range 4 {
			chain = (chain ^ uint64(t&0xff)) * prime64
			t >>= 8
		}
	}
	return chain
}

// FNV-1a's 64-bit parameters. root is the chain of no tokens at all.
const (
	root    = uint64(14695981039346656037)
	prime64 = uint64(1099511628211)
)
