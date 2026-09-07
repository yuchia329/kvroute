// Package stats summarises a stream of durations as percentiles.
//
// It backs the router's own overhead reporting. The JSONL rows remain the
// system of record; this exists so the router can answer "what did I add to the
// request path" without an analysis pass.
package stats

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"sync"
	"time"
)

// DefaultCapacity bounds memory for a long run.
const DefaultCapacity = 1 << 20

// Recorder accumulates durations. It is safe for concurrent use.
//
// Past its capacity it keeps a uniform random sample of everything it has seen
// (Vitter's algorithm R) rather than the first N observations. Keeping the
// first N would quietly turn a long run's percentiles into percentiles of its
// warm-up, which is exactly the window least representative of the run.
type Recorder struct {
	mu       sync.Mutex
	rng      *rand.Rand
	samples  []time.Duration
	observed int64
	capacity int
}

// Summary is a percentile view of what a Recorder has seen. Observed counts
// every observation; Sampled counts the ones retained, which is fewer once the
// capacity is reached.
type Summary struct {
	Observed int64         `json:"observed"`
	Sampled  int           `json:"sampled"`
	P50      time.Duration `json:"-"`
	P99      time.Duration `json:"-"`
	Max      time.Duration `json:"-"`
	P50Us    float64       `json:"p50_us"`
	P99Us    float64       `json:"p99_us"`
	MaxUs    float64       `json:"max_us"`
}

// NewRecorder builds a recorder retaining at most capacity samples; a capacity
// of zero or less uses DefaultCapacity.
func NewRecorder(capacity int) *Recorder {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	// Seeded fixed: a rerun of a cell should summarise the same way.
	return &Recorder{capacity: capacity, rng: rand.New(rand.NewPCG(1, 2))}
}

// Observe records one duration.
func (r *Recorder) Observe(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observed++
	if len(r.samples) < r.capacity {
		r.samples = append(r.samples, d)
		return
	}
	if i := r.rng.Int64N(r.observed); i < int64(r.capacity) {
		r.samples[i] = d
	}
}

// Summary computes percentiles over the retained samples by nearest rank.
func (r *Recorder) Summary() Summary {
	r.mu.Lock()
	sorted := append([]time.Duration(nil), r.samples...)
	observed := r.observed
	r.mu.Unlock()

	slices.Sort(sorted)
	s := Summary{Observed: observed, Sampled: len(sorted)}
	if len(sorted) == 0 {
		return s
	}
	s.P50 = Quantile(sorted, 0.50)
	s.P99 = Quantile(sorted, 0.99)
	s.Max = sorted[len(sorted)-1]
	s.P50Us = float64(s.P50.Nanoseconds()) / 1000
	s.P99Us = float64(s.P99.Nanoseconds()) / 1000
	s.MaxUs = float64(s.Max.Nanoseconds()) / 1000
	return s
}

// Quantile is nearest-rank over an already-sorted slice, and returns zero for
// an empty one.
//
// Exported so that the benchmark harness and the router's own overhead
// reporting share one definition of a percentile. Two definitions would be two
// numbers that disagree by a rank in the README.
//
// Generic over the ordered type for that same reason rather than for reuse's
// sake: latencies are durations and goodput is a rate, and a median over rates
// computed some other way would be a second definition by another route.
// Nearest-rank interpolates nothing, so it needs no arithmetic on T — the figure
// it returns is always one that was measured.
func Quantile[T cmp.Ordered](sorted []T, q float64) T {
	if len(sorted) == 0 {
		var zero T
		return zero
	}
	rank := int(q*float64(len(sorted)) + 0.5)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
