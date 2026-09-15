package vllmmetrics

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// HitRate is what a replica's prefix cache served over a recent window: the
// share of the block queries in that window the cache answered.
//
// It is CONTEXT.md's prefix cache hit rate — the engine's own reported figure,
// ground truth for what a replica is holding — taken per replica over a moving
// window rather than over a run. That windowing is the whole of the type: the
// engine publishes cumulative counters, so a replica's lifetime ratio barely
// moves once a run is under way and says nothing about what it is doing now.
//
// It is the spill rule's residency signal since #28's second finding. The first
// candidate, honoured belief, was built and measured and does not work: the
// prefix index is calibrated not to over-predict (ADR-0006, ADR-0008), so it
// drops beliefs before the engines evict the blocks and is nearly always right
// about whatever it still claims. Its rate came back pinned — 70% of readings
// exactly 1.0, p50 = p90 = max = 1.0 — and replaying the run through five
// windows widened the histogram 2.4x without improving what the signal
// predicted. This one has range on the same run: 68.7% fleet-wide.
//
// Read is false when the window holds too little traffic to say, so "this
// replica is hitting nothing" and "nobody measured this replica" do not read the
// same. That is the spill rule's graceful degradation, and it matters more here
// than for a scraped gauge: a rate of zero means evicting everything, and a
// fleet reported that way on a failed scrape would spill itself apart.
type HitRate struct {
	Fraction float64 `json:"fraction"`
	Read     bool    `json:"read"`
	// Queries is how many block queries the reading rests on, so a rate is never
	// reported without the evidence behind it.
	Queries float64 `json:"queries"`
}

// Under reports whether this replica is known to have fallen below a low-water
// mark.
//
// An unread reading is never under, whatever the mark. It lives here rather than
// at the call site so that a policy cannot forget it — the same rule the signal
// this replaced carried, for the same reason.
//
// Strictly under, so a mark of 0 cannot fire on a replica hitting nothing by
// arithmetic and a mark of 1 does not fire on a perfect cache.
func (h HitRate) Under(lowWater float64) bool { return h.Read && h.Fraction < lowWater }

// String renders the reading with the evidence behind it, keeping "unread"
// distinct from a rate of zero.
func (h HitRate) String() string {
	if !h.Read {
		return "unread"
	}
	return fmt.Sprintf("%.1f%% of %.0f", h.Fraction*100, h.Queries)
}

// HitRateSpan bounds the evidence one reading rests on.
//
// Two sides rather than three, because a scrape loop is not a request stream:
// readings arrive on a fixed tick, so bounding the window in time bounds the
// count too. Window is how far back a rate looks; MinQueries is the fewest block
// queries it may rest on, which is the quorum — a replica that served three
// blocks in the window has a rate that is arithmetic rather than measurement.
//
// Neither is a measurement, which makes them knobs of the same kind as the
// thresholds rather than bounds of the kind ADR-0006 refuses to default.
type HitRateSpan struct {
	Window     time.Duration `json:"window"`
	MinQueries float64       `json:"min_queries"`
}

// DefaultHitRateSpan is the span the router uses when a run does not state one.
//
// Ten seconds at a 250 ms scrape tick is forty readings, and the engine queries
// its prefix cache once per block of every prompt — at roughly two requests per
// second per replica and ~600 blocks a prompt, ten seconds is some twelve
// thousand queries, two orders of magnitude above the floor. Short enough to
// respond within a conversation's turn, long enough that a quiet replica still
// clears the floor.
var DefaultHitRateSpan = HitRateSpan{Window: 10 * time.Second, MinQueries: 100}

// Validate refuses a span that could not produce a reading, at startup rather
// than by quietly disabling the condition for a run.
func (s HitRateSpan) Validate() error {
	if s.Window <= 0 {
		return fmt.Errorf("vllmmetrics: the hit-rate window is how far back a rate looks and must be positive, got %v", s.Window)
	}
	if s.MinQueries <= 0 {
		return fmt.Errorf("vllmmetrics: the hit-rate window's minimum queries is the fewest a reading may rest on and must be positive, got %v", s.MinQueries)
	}
	return nil
}

func (s HitRateSpan) String() string {
	return fmt.Sprintf("%v/%.0f queries", s.Window, s.MinQueries)
}

// HitRateWindow is one replica's moving hit rate, fed by the scrape loop.
//
// It keeps the readings inside the window and differences the oldest against the
// newest, which is what turns two cumulative counters into a rate. Readings are
// appended in scrape order and expired from the front, so the whole of it is a
// slice and a mutex held for an append.
type HitRateWindow struct {
	span HitRateSpan

	mu      sync.Mutex
	samples []stampedCounters
}

type stampedCounters struct {
	at time.Time
	pc PrefixCache
}

// NewHitRateWindow builds a replica's moving rate over the given span. The span
// is the caller's to validate; an invalid one is a run misconfigured at startup.
func NewHitRateWindow(s HitRateSpan) *HitRateWindow {
	if s.Window <= 0 {
		s.Window = DefaultHitRateSpan.Window
	}
	return &HitRateWindow{span: s}
}

// Observe folds one scrape of a replica's counters into the window.
//
// A scrape that did not answer is dropped rather than recorded: it is not
// evidence of anything, and entering it as a zero would look like a replica
// whose counters reset. The window simply spans the gap, which is what a
// counter difference does correctly anyway.
func (w *HitRateWindow) Observe(at time.Time, pc PrefixCache) {
	if !pc.Read {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.samples = append(w.samples, stampedCounters{at: at, pc: pc})
	w.expire(at)
}

// Rate is the reading a routing decision is made against: what this replica's
// cache served over the window.
//
// Counters that went backwards mean the replica restarted inside the window,
// which is not a delta — reporting one would publish a fraction of a lifetime
// the window never measured. The whole window is then dropped and rebuilt from
// the readings that follow, for the reason lost history is not guessed across
// (ADR-0010): what came before the restart describes a cache that no longer
// exists.
func (w *HitRateWindow) Rate(now time.Time) HitRate {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.expire(now)
	if len(w.samples) < 2 {
		// One point on a cumulative counter is not a rate, and no points is not
		// either. Reporting the lifetime ratio here would make every replica look
		// healthy for the first window of every run.
		return HitRate{}
	}
	oldest, newest := w.samples[0].pc, w.samples[len(w.samples)-1].pc
	delta := newest.Since(oldest)
	if !delta.Read {
		// Since refuses a counter that went backwards. Forget the window rather
		// than keep readings from before a restart.
		w.samples = w.samples[:0]
		return HitRate{}
	}
	if delta.Queries < w.span.MinQueries {
		return HitRate{}
	}
	return HitRate{Fraction: delta.HitRate(), Read: true, Queries: delta.Queries}
}

// expire drops readings that have fallen out of the window, keeping the last one
// that is older than it. The caller holds the mutex.
//
// That kept reading is the point the newest is differenced against, and dropping
// it too would shrink the window to whatever arrived after the cutoff — at the
// start of a run, to nothing. It is the left edge, not a straggler.
func (w *HitRateWindow) expire(now time.Time) {
	cutoff := now.Add(-w.span.Window)
	keep := 0
	for i, s := range w.samples {
		if s.at.After(cutoff) {
			break
		}
		keep = i
	}
	if keep > 0 {
		w.samples = append(w.samples[:0], w.samples[keep:]...)
	}
}

// ReplicaSample is everything the router reads off one replica's /metrics in
// one scrape: the batch gauge it records, and the prefix-cache counters it
// routes on.
//
// One type and one GET, because the two used to be two scrapes of the same
// endpoint on the same tick. That doubles the request rate against the very
// engines the measurement comes off, for two numbers that arrive in the same
// response body — and it makes the pair describe two different instants, which
// is exactly what the correlation between them is not allowed to have.
type ReplicaSample struct {
	BatchKV     BatchOccupancy
	PrefixCache PrefixCache
}

// ReadReplica scrapes one replica once and parses both signals out of it. A nil
// client uses http.DefaultClient.
//
// A failure is not an error: the caller is routing requests, and a scrape that
// did not answer must degrade the signals rather than the routing. Both come
// back unread together, because they came from one response that did not arrive.
func ReadReplica(ctx context.Context, client *http.Client, metricsURL string) ReplicaSample {
	body, ok := scrape(ctx, client, metricsURL)
	if !ok {
		return ReplicaSample{}
	}
	var sample ReplicaSample
	if fraction, found := Value(body, BatchKVUsage); found {
		sample.BatchKV = BatchOccupancy{Fraction: fraction, Read: true}
	}
	hits, hitsOK := Value(body, PrefixCacheHits)
	queries, queriesOK := Value(body, PrefixCacheQueries)
	if hitsOK && queriesOK {
		sample.PrefixCache = PrefixCache{Hits: hits, Queries: queries, Read: true}
	}
	return sample
}
