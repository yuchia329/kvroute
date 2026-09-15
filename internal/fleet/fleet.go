// Package fleet owns the set of replicas and the load state the router knows
// about them.
//
// State is the whole of what a policy is allowed to see. It carries three
// signals of three different kinds and they are never confused for one another:
// inflight is counted locally and exactly, batch KV occupancy is scraped and
// therefore up to one polling window old, and the honoured rate is fed back
// from the engines' own answers over a window of recent requests. The first two
// are load and the third is cache residency — which is a distinction #16 and
// #18 did not have, because the gauge they read as residency was measuring the
// batch (ADR-0011).
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
	"time"

	"github.com/yuchia329/kvroute/internal/belief"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
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
	// BatchKV is the replica's last scraped batch KV occupancy, or an unread
	// reading when no scrape has answered for it.
	//
	// A load signal, and a third-hand one. It is the engine's own view of how
	// much cache the batch it is running is holding, and the router already
	// knows what it dispatched exactly and without a polling window: measured,
	// the two agree at r = 0.973 (ADR-0011). It is on the candidate so that
	// every decision can record what the engine was doing beside what the router
	// believed, and no policy routes on it.
	BatchKV vllmmetrics.BatchOccupancy
	// Honoured is how much of what the router has recently claimed this replica
	// was holding the replica turned out to be holding, or an unread reading
	// when it has answered too few scoring requests to say.
	//
	// This is the residency signal, and it is the one thing here that is neither
	// counted by the router nor scraped off a gauge: it is fed back from the
	// engines' own usage blocks as the responses pass through. A replica that
	// has stopped honouring the index's beliefs is a replica that is evicting
	// them, which is the pressure the spill rule's first branch is about and
	// which no published gauge reports. See package belief.
	//
	// It carries a window rather than a polling interval, so it lags differently
	// from everything else on this struct: it is an average over the replica's
	// last few answered requests, and it says nothing at all until there have
	// been a few.
	Honoured belief.Honoured
	// HitRate is what this replica's prefix cache served over a recent window,
	// as the engine's own counters report it.
	//
	// This is the residency signal the spill rule reads. It is the engine's
	// ground truth for what the replica is holding, rather than the router's
	// belief about it or a gauge of the running batch: a replica whose hit rate
	// is falling is a replica that is evicting, and unlike the honoured rate
	// beside it, this one has range (#28's second run). Scraped, so it carries a
	// polling window; windowed, so it says what the replica is doing now rather
	// than what it has done since it started.
	HitRate vllmmetrics.HitRate
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
	// position is stable for the life of the fleet. Leaving rotation does not
	// change that. A replica that is drained or ejected keeps its position and its
	// counter, because the requests it was already serving are still in flight and
	// still have to be counted down; State simply stops offering it to the policy.
	inflight []atomic.Int64
	index    map[string]int
	// kv is indexed as replicas, and holds the last reading the scraper took of
	// each. A nil pointer is a replica nobody has scraped yet, which is not the
	// same as one whose cache is empty — see vllmmetrics.BatchOccupancy. Written
	// once per replica per scrape interval and read on every routing decision,
	// so it is a pointer swap rather than anything held under the fleet's mutex.
	kv []atomic.Pointer[vllmmetrics.BatchOccupancy]
	// honoured is indexed as replicas, and holds each one's running honoured
	// rate. Unlike kv it is fed by the router rather than by a scraper — one
	// observation per answered request that claimed something — and it keeps its
	// own window, so it is a live accumulator rather than a swapped-in reading.
	//
	// Always present, even when no policy reads it. A rate nobody consults costs
	// one ring step per response and keeps the column on every row, which is what
	// the correlation against inflight is measured from; refusing to feed it
	// unless the condition was on would make the run that chooses the grid
	// impossible to take.
	honoured []*belief.Feedback
	// hitRate is indexed as replicas, and holds each one's moving prefix cache
	// hit rate, fed by the same scrape that reads the batch gauge. Like honoured
	// it is a live accumulator rather than a swapped-in reading, because a rate
	// over a window cannot be taken from one scrape.
	hitRate []*vllmmetrics.HitRateWindow
	// rotation is indexed as replicas, and says why a replica is out of rotation
	// when it is. Under mu rather than atomic like the counts above: it changes
	// rarely, and it has to change atomically with the check Dispatch makes
	// against it, which is what lets a drain promise that nothing lands after it.
	rotation []rotation
}

// Option configures a fleet at New.
//
// Variadic because the one thing there is to configure is a window on a signal
// most callers never read: a required parameter would make every test and every
// command that fronts replicas state a window for a rate it does not consult.
type Option func(*config)

type config struct {
	honoured belief.Window
	hitRate  vllmmetrics.HitRateSpan
}

// WithHonouredWindow sets the window each replica's honoured rate is taken
// over. Unset uses belief.DefaultWindow.
func WithHonouredWindow(w belief.Window) Option {
	return func(c *config) { c.honoured = w }
}

// WithHitRateSpan sets the window each replica's prefix cache hit rate is taken
// over. Unset uses vllmmetrics.DefaultHitRateSpan.
func WithHitRateSpan(s vllmmetrics.HitRateSpan) Option {
	return func(c *config) { c.hitRate = s }
}

// New builds a fleet. It rejects duplicate ids and unusable base URLs, because
// a typo in a replica spec should fail at startup rather than as a routing
// error under load.
func New(replicas []Replica, opts ...Option) (*Fleet, error) {
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
	cfg := config{honoured: belief.DefaultWindow, hitRate: vllmmetrics.DefaultHitRateSpan}
	for _, opt := range opts {
		opt(&cfg)
	}
	if err := cfg.honoured.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.hitRate.Validate(); err != nil {
		return nil, err
	}

	index := make(map[string]int, len(replicas))
	for i, r := range replicas {
		index[r.ID] = i
	}
	honoured := make([]*belief.Feedback, len(replicas))
	for i := range honoured {
		honoured[i] = belief.NewFeedback(cfg.honoured)
	}
	hitRate := make([]*vllmmetrics.HitRateWindow, len(replicas))
	for i := range hitRate {
		hitRate[i] = vllmmetrics.NewHitRateWindow(cfg.hitRate)
	}
	return &Fleet{
		replicas: append([]Replica(nil), replicas...),
		inflight: make([]atomic.Int64, len(replicas)),
		kv:       make([]atomic.Pointer[vllmmetrics.BatchOccupancy], len(replicas)),
		honoured: honoured,
		hitRate:  hitRate,
		rotation: make([]rotation, len(replicas)),
		index:    index,
	}, nil
}

// ObserveHonoured folds one answered request into a replica's honoured rate:
// what the router claimed that replica was holding, and how much of the claim
// the engine's usage block says it held.
//
// Called by the router once a response has been relayed, because that is when
// the engine's account of it arrives. A request that claimed nothing is not
// evidence and is dropped by the window; see belief.Feedback.Observe.
//
// A reading for a replica the fleet does not front is an error rather than a
// discarded write, for the reason ObserveBatchOccupancy's is: silently writing
// into nothing would leave every replica unread and the condition quietly
// disabled for the length of a run, which is precisely the failure this signal
// was built to stop repeating.
func (f *Fleet) ObserveHonoured(id string, at time.Time, claimed, held float64) error {
	f.mu.RLock()
	i, ok := f.index[id]
	f.mu.RUnlock()
	if !ok {
		return fmt.Errorf("fleet: no replica %q to record an honoured-belief observation for", id)
	}
	f.honoured[i].Observe(at, claimed, held)
	return nil
}

// State returns a snapshot for one routing decision.
//
// Only the replicas in rotation are in it. A replica that has been drained or
// ejected is not a candidate at all, which is how its leaving reaches a policy:
// no policy has to know that replicas can leave, and none can route to one that
// has by forgetting to check.
func (f *Fleet) State() State {
	f.mu.RLock()
	defer f.mu.RUnlock()

	candidates := make([]Candidate, 0, len(f.replicas))
	for i := range f.replicas {
		if f.rotation[i].in() {
			candidates = append(candidates, f.candidate(i))
		}
	}
	return State{Replicas: candidates}
}

// candidate is replica i with the load the fleet knows it is under. The caller
// holds mu.
func (f *Fleet) candidate(i int) Candidate {
	c := Candidate{Replica: f.replicas[i], Inflight: int(f.inflight[i].Load())}
	if reading := f.kv[i].Load(); reading != nil {
		c.BatchKV = *reading
	}
	// Taken at the snapshot rather than kept current in the background, because
	// the window expires by age: a replica the spill rule has stopped sending
	// matches to answers nothing, so nothing would ever refresh its reading, and
	// it has to age out on the clock of whoever is asking.
	now := time.Now()
	c.Honoured = f.honoured[i].Rate(now)
	// Taken at the snapshot for the reason the honoured rate is: the window
	// expires by age, so a replica nobody has scraped lately has to go unread on
	// the clock of whoever is asking rather than stay at its last reading.
	c.HitRate = f.hitRate[i].Rate(now)
	return c
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
//
// A replica that has left rotation since the policy's snapshot was taken is
// refused with ErrOutOfRotation. The check and the count happen under one read
// lock, and leaving rotation takes the write lock, so a dispatch lands either
// wholly before a drain or not at all: see Drain.
func (f *Fleet) Dispatch(id string) (func(), error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	i, ok := f.index[id]
	if !ok {
		return nil, fmt.Errorf("fleet: no replica %q to dispatch to", id)
	}
	if !f.rotation[i].in() {
		return nil, fmt.Errorf("fleet: %s: %w", id, ErrOutOfRotation)
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
