// Package fleet owns the set of replicas and the load state the router knows
// about them.
//
// State is the whole of what a policy is allowed to see, so later work can add
// scraped KV utilization here without any policy or the router's ingress needing
// to change shape.
//
// Inflight is counted here rather than read from anywhere: the router is the
// sole ingress, so it knows exactly what it dispatched and what has not come
// back. Deriving it from a scrape instead would make every request that arrived
// inside one polling window see the same least-loaded replica and stampede it,
// which would corrupt the load-aware policies without ever looking wrong.
package fleet

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

// Replica is one vLLM process serving one model on one GPU.
type Replica struct {
	ID      string
	BaseURL string
}

// URL builds an upstream URL on this replica, so that callers never assemble
// one out of a bare string.
func (r Replica) URL(path, rawQuery string) string {
	u := r.BaseURL + path
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
}

// Candidate is one replica as a policy sees it: its identity, plus the load the
// router knows it is under.
//
// A policy is handed candidates rather than bare replicas so that a policy which
// ignores load has to do so deliberately.
type Candidate struct {
	Replica
	// Inflight is how many requests the router had dispatched to this replica
	// and not yet seen complete when the snapshot was taken. Counted locally and
	// exactly, never scraped. It includes the requests the replica is running
	// and the ones it has queued, because the router cannot tell those apart and
	// does not need to.
	Inflight int
}

// State is a snapshot of the fleet, taken once per routing decision.
//
// The counts in it are read one replica at a time, so it is a snapshot rather
// than an instant: two of its figures may be nanoseconds apart. That is the same
// looseness as a policy's own decision being made before the request it is for
// is dispatched, and it is five orders of magnitude tighter than the polling
// window a scraped count would carry.
type State struct {
	Replicas []Candidate
}

// Candidate finds one replica in this snapshot, and reports whether the fleet
// still had it when the snapshot was taken.
//
// It lives on State rather than in the policy that wants it because the answer
// is about the fleet's own membership. A policy holding a replica id from
// somewhere other than this snapshot — a prefix index believes in replicas it
// routed to minutes ago — has to ask whether the fleet still has it, and asking
// by walking State's slice from another package would put the fleet's shape in
// that package's code.
func (s State) Candidate(id string) (Candidate, bool) {
	for _, c := range s.Replicas {
		if c.ID == id {
			return c, true
		}
	}
	return Candidate{}, false
}

// Fleet is the router's view of the replicas it fronts.
type Fleet struct {
	mu       sync.RWMutex
	replicas []Replica
	// inflight is indexed as replicas, and index maps a replica id to that
	// position. Atomics rather than a counter under mu, because every request
	// takes two of these on its hot path and the routing decision above them is
	// measured in microseconds.
	//
	// All three are fixed at New: nothing here removes or adds a replica, so a
	// position is stable for the life of the fleet and Dispatch can resolve one
	// under a read lock and then count against it without holding anything. Ejection
	// changes that — a Dispatch in flight holds a position, so whatever introduces it
	// has to keep a departed replica's counter alive until its requests have drained
	// rather than compacting these slices under it.
	inflight []atomic.Int64
	index    map[string]int
}

// New builds a fleet. It rejects duplicate ids and unusable base URLs, because
// a typo in a replica spec should fail at startup rather than as a routing
// error under load.
func New(replicas []Replica) (*Fleet, error) {
	if len(replicas) == 0 {
		return nil, errors.New("fleet: at least one replica is required")
	}
	seen := make(map[string]bool, len(replicas))
	for _, r := range replicas {
		if r.ID == "" {
			return nil, errors.New("fleet: replica id must not be empty")
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("fleet: duplicate replica id %q", r.ID)
		}
		seen[r.ID] = true

		u, err := url.Parse(r.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("fleet: replica %s: %w", r.ID, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("fleet: replica %s: base URL %q needs a scheme and a host", r.ID, r.BaseURL)
		}
	}
	index := make(map[string]int, len(replicas))
	for i, r := range replicas {
		index[r.ID] = i
	}
	return &Fleet{
		replicas: append([]Replica(nil), replicas...),
		inflight: make([]atomic.Int64, len(replicas)),
		index:    index,
	}, nil
}

// State returns a snapshot for one routing decision.
func (f *Fleet) State() State {
	f.mu.RLock()
	defer f.mu.RUnlock()

	candidates := make([]Candidate, len(f.replicas))
	for i, r := range f.replicas {
		candidates[i] = Candidate{Replica: r, Inflight: int(f.inflight[i].Load())}
	}
	return State{Replicas: candidates}
}

// Dispatch records that a request is on its way to a replica, and returns the
// function that records it as complete.
//
// The router calls this at the moment it commits a request to a replica, not the
// policy that chose it: inflight is what the router dispatched, and a policy that
// counted its own choices would also count the ones it declined.
//
// The returned function must be called on every path a request can end on —
// success, replica error, client disconnect and timeout alike — which is why the
// router defers it the moment it acquires. It is idempotent: a second call cannot
// drive the count below what is genuinely in flight, because an under-count is
// what makes a policy pile more work onto a replica that is already busy. A
// missed call is the direction that cannot be defended against here, and it
// would leave a replica permanently and wrongly loaded.
func (f *Fleet) Dispatch(id string) (func(), error) {
	f.mu.RLock()
	i, ok := f.index[id]
	f.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("fleet: no replica %q to dispatch to", id)
	}

	f.inflight[i].Add(1)
	var once sync.Once
	return func() { once.Do(func() { f.inflight[i].Add(-1) }) }, nil
}

// ParseSpecs turns "replica-0=http://127.0.0.1:8000" command-line specs into
// replicas. A bare URL is given a positional id.
func ParseSpecs(specs []string) ([]Replica, error) {
	replicas := make([]Replica, 0, len(specs))
	for i, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		id, base, found := strings.Cut(spec, "=")
		if !found {
			id, base = fmt.Sprintf("replica-%d", i), spec
		}
		replicas = append(replicas, Replica{ID: strings.TrimSpace(id), BaseURL: strings.TrimSuffix(strings.TrimSpace(base), "/")})
	}
	return replicas, nil
}
