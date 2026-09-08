package prefix

import (
	"container/list"
	"errors"
	"sort"
	"sync"
	"time"
)

// Config bounds an index. Both bounds are required and neither has a default.
//
// An index without them believes everything for ever, and the failure mode of
// that is not a crash: it is a policy sending requests to a replica because of
// something that stopped being true hours ago, which is strictly worse than
// routing on load alone. The two figures are measurements — see Calibration —
// so a default here would be a guess wearing a measurement's clothes.
type Config struct {
	// NodeCap is the most blocks the index will hold beliefs about, across all
	// replicas and conversations. Past it, the least recently served
	// conversations are forgotten.
	NodeCap int
	// TTL is how long a belief about one replica holding one block stands
	// without being renewed.
	TTL time.Duration
	// Now is the clock, so that expiry can be tested by advancing time rather
	// than by waiting for it. Nil uses time.Now.
	Now func() time.Time
}

// Match is one replica's prefix match: the longest leading run of a prompt's
// blocks it is believed to hold.
//
// Reported in bytes, and in blocks that are the router's own and not the
// engine's. It is a prediction of the prefix cache hit the engine will record,
// never that hit itself; CONTEXT.md keeps the two terms apart because the gap
// between them is a measured result of this project rather than an error to be
// tidied away.
type Match struct {
	Replica string
	Blocks  int
	Bytes   int
}

// Index maps block hashes to the replicas believed to hold them.
//
// The trie is in the hashes rather than in the pointers. Every node's key
// covers the whole leading run of the prompt that reaches it, so a node already
// identifies one path from the root and the parent links a literal trie would
// carry would be links between values that are already nested. Longest-chain
// match is then a walk down one chain, ending at the first block nobody holds.
//
// Beliefs are recorded from the router's own decisions rather than from the
// engines: this is the approximate index llm-d calls a routing-history-derived
// belief, and its cost in accuracy is a measured quantity (#17) rather than an
// assumption.
type Index struct {
	// One mutex over both maps rather than a read/write pair, because Match is
	// followed by an Admit of the same chain on every request the router
	// handles: there is no read-mostly phase for an RWMutex to pay off in, and
	// a walk of a few dozen map lookups is well inside the router's overhead
	// budget.
	mu sync.Mutex
	// nodes indexes into the LRU list, whose elements hold the nodes. Together
	// they are one structure: a map for lookup by hash, a list for the order the
	// cap discards in.
	nodes map[uint64]*list.Element
	lru   *list.List
	cap   int
	ttl   time.Duration
	now   func() time.Time
}

// node is what the index believes about one block: which replicas hold it, and
// when each of them was last believed to.
type node struct {
	hash     uint64
	replicas map[string]time.Time
}

// New builds an index.
func New(cfg Config) (*Index, error) {
	if cfg.NodeCap <= 0 {
		return nil, errors.New("prefix: a node cap is required, and it is a measurement rather than a default: see Calibration")
	}
	if cfg.TTL <= 0 {
		return nil, errors.New("prefix: a TTL is required, and it is a measurement rather than a default: see Calibration")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Index{
		nodes: make(map[uint64]*list.Element, cfg.NodeCap),
		lru:   list.New(),
		cap:   cfg.NodeCap,
		ttl:   cfg.TTL,
		now:   cfg.Now,
	}, nil
}

// Match returns each replica's prefix match against this prompt, best first,
// ties broken by replica id.
//
// A replica appears only if it is believed to hold the prompt's first block: the
// engine's prefix cache is prefix-only, so a replica holding the middle of a
// conversation and not its opening can reuse none of it, and crediting it with a
// match would route the request somewhere it must prefill the whole prompt
// anyway.
//
// The order is total and deterministic. A policy takes the head of this list, so
// an order settled by map iteration would send identical requests to different
// replicas from one call to the next, and no record could reconstruct why.
func (ix *Index) Match(chain Chain) []Match {
	if len(chain) == 0 {
		return nil
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	now := ix.now()

	// matched is each replica's run length so far; alive is the replicas whose
	// run is still unbroken. A replica leaves alive at the first block it does
	// not hold and keeps the length it had reached, which is what makes the
	// match a leading run rather than a count of blocks held anywhere.
	matched := map[string]int{}
	alive := map[string]bool{}
	for depth, h := range chain {
		element, held := ix.nodes[h]
		if !held {
			break
		}
		n := element.Value.(*node)
		if depth == 0 {
			for replica, at := range n.replicas {
				if now.Sub(at) <= ix.ttl {
					alive[replica], matched[replica] = true, 1
				}
			}
		} else {
			for replica := range alive {
				at, holds := n.replicas[replica]
				if !holds || now.Sub(at) > ix.ttl {
					delete(alive, replica)
					continue
				}
				matched[replica] = depth + 1
			}
		}
		if len(alive) == 0 {
			break
		}
	}

	matches := make([]Match, 0, len(matched))
	for replica, blocks := range matched {
		matches = append(matches, Match{Replica: replica, Blocks: blocks, Bytes: blocks * BlockBytes})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Blocks != matches[j].Blocks {
			return matches[i].Blocks > matches[j].Blocks
		}
		return matches[i].Replica < matches[j].Replica
	})
	return matches
}

// Admit records that a replica is now believed to hold this prompt's blocks.
//
// The router calls it with the chain it just routed, so the belief is the
// router's own dispatch history. That is the belief's whole basis and also its
// whole weakness: the engine may never have cached what it was sent, or may
// have evicted it a moment later, and the TTL and the cap are what keep that
// weakness bounded.
func (ix *Index) Admit(chain Chain, replica string) {
	if len(chain) == 0 || replica == "" {
		return
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	now := ix.now()

	// Deepest block first, so that a conversation's opening ends up the most
	// recently used of its blocks. A shallow block is the only way in to the
	// deeper ones — the match walk stops at the first block nobody holds — so
	// evicting one discards every block behind it too. Admitting in prompt order
	// would leave the opening nearest the discard end and let the cap silently
	// make whole conversations unmatchable while their deep blocks survived.
	for i := len(chain) - 1; i >= 0; i-- {
		element, held := ix.nodes[chain[i]]
		if !held {
			element = ix.lru.PushFront(&node{hash: chain[i], replicas: map[string]time.Time{}})
			ix.nodes[chain[i]] = element
		} else {
			ix.lru.MoveToFront(element)
		}
		n := element.Value.(*node)
		n.replicas[replica] = now
		// Expired beliefs are dropped as the node is touched rather than swept
		// for: a node nobody touches is one the cap will reach anyway, and a
		// periodic sweep would be a second mechanism holding the same lock for
		// no reading anybody makes of the index.
		for other, at := range n.replicas {
			if now.Sub(at) > ix.ttl {
				delete(n.replicas, other)
			}
		}
	}

	for ix.lru.Len() > ix.cap {
		oldest := ix.lru.Back()
		ix.lru.Remove(oldest)
		delete(ix.nodes, oldest.Value.(*node).hash)
	}
}

// Len is how many blocks the index currently believes anything about. It is the
// figure the node cap bounds, and it is reported so a run can show that the
// index was sized to the fleet rather than merely told to be.
func (ix *Index) Len() int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.lru.Len()
}
