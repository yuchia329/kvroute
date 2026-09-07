package bench

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

// openLoopPoolSeconds is how many seconds of arrivals the default client keeps
// idle connections for.
//
// Sized from the rate because open-loop has no concurrency to size from: past
// saturation the number of requests in flight grows without bound, which is the
// condition this driver exists to measure. The pool is a reuse cache, so an
// undersized one costs TCP setup rather than correctness — but at the top of a
// rate ladder the standard library's default of two idle connections per host
// would have the driver measuring connection setup as though it were inference.
const openLoopPoolSeconds = 30

// OpenLoopClient dispatches an open-loop cell to the router.
func OpenLoopClient(rate float64) *http.Client {
	return DefaultClient(max(1, int(rate*openLoopPoolSeconds)))
}

// RunOpenLoop runs one cell at a fixed arrival rate and returns its rows.
//
// An open-loop driver fires on a schedule regardless of whether earlier requests
// have finished, so offered load is an input rather than an outcome. That is the
// whole difference from the closed-loop driver, and it is the difference that
// matters at saturation: a closed-loop driver slows down when the fleet slows
// down, so it never pushes past the knee and its tail is systematically
// optimistic. Goodput under an SLO is a question about behaviour at and past
// saturation, so the headline number is measured here.
//
// Two consequences are deliberate. Requests in flight are unbounded — a fleet
// that cannot keep up accumulates them, which is the fact being measured, and
// capping it would reintroduce the throttling the driver exists to avoid. And
// the k-th request is due at start + k/rate computed from the cell's own origin
// rather than by accumulating sleeps, so a late dispatch cannot push the ones
// after it later still.
//
// The schedule is even rather than random. It is the schedule CONTEXT.md
// defines, it is reproducible between repetitions of the same cell, and it is
// the one whose lateness can be read off a row without reconstructing a
// generator's state: every row carries the time it was due beside the time it
// was sent, so a driver that failed to hold its own schedule says so in the
// record instead of quietly reporting a rate it never offered.
func RunOpenLoop(ctx context.Context, cfg DriverConfig) ([]Result, error) {
	if cfg.Target == "" {
		return nil, errors.New("bench: a target router URL is required")
	}
	if cfg.ArrivalRate <= 0 {
		return nil, fmt.Errorf("bench: an arrival rate in requests per second is required, got %g", cfg.ArrivalRate)
	}
	if cfg.Duration <= 0 {
		return nil, fmt.Errorf("bench: an open-loop cell needs a duration to fire across, got %v", cfg.Duration)
	}
	if cfg.Workload == nil {
		return nil, errors.New("bench: a workload is required")
	}
	interval := time.Duration(float64(time.Second) / cfg.ArrivalRate)
	if interval <= 0 {
		return nil, fmt.Errorf("bench: an arrival rate of %g requests per second is finer than the clock this schedule is kept on", cfg.ArrivalRate)
	}
	if cfg.Client == nil {
		cfg.Client = OpenLoopClient(cfg.ArrivalRate)
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Warmup >= cfg.Duration {
		return nil, fmt.Errorf("bench: warm-up %v leaves nothing of a %v cell to measure", cfg.Warmup, cfg.Duration)
	}
	cfg.Labels.Driver = OpenLoopDriver
	cfg.Labels.ArrivalRate = cfg.ArrivalRate
	// Cleared for the same reason RunClosedLoop clears the rate: under this
	// driver concurrency is an outcome, and a row that labelled one as an input
	// would be a row disagreeing with the run that produced it.
	cfg.Labels.Concurrency = 0

	start := time.Now()
	deadline := start.Add(cfg.Duration)
	warmUntil := start.Add(cfg.Warmup)

	var (
		mu      sync.Mutex
		results []Result
		wg      sync.WaitGroup
	)
	for k := 0; ; k++ {
		due := start.Add(time.Duration(k) * interval)
		// The deadline stops new arrivals and never cuts one already in flight:
		// cutting there would put the driver's own clock in the failure column.
		if !due.Before(deadline) {
			break
		}
		if !waitUntil(ctx, due) {
			break
		}
		user, turn := arrival(k)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// One goroutine per arrival, so a request the fleet is slow to
			// answer cannot delay the next dispatch. This is the mechanism
			// behind the schedule holding.
			row := sendTurn(ctx, cfg, user, turn, warmUntil, due)
			mu.Lock()
			results = append(results, row)
			mu.Unlock()
			writeRow(cfg, row)
		}()
	}
	wg.Wait()

	// Ordered by start so the rows read as the cell ran rather than as the
	// goroutines happened to be scheduled.
	sort.SliceStable(results, func(i, j int) bool { return results[i].StartedAtNs < results[j].StartedAtNs })
	return results, nil
}

// arrival maps the k-th arrival onto the workload's (user, turn) space.
//
// A cell's slice of that space is WorkloadStride wide and an open-loop cell can
// fire more requests than that, so arrivals wrap onto turns rather than running
// off the end into the next cell's slice. The workload is deterministic in the
// pair, so every arrival still sends bytes no other arrival in the cell sends —
// which is what ADR-0004 requires and what keeps a cell measuring prefill rather
// than the replicas' prefix caches.
func arrival(k int) (user, turn int) {
	return k % WorkloadStride, k / WorkloadStride
}

// waitUntil blocks until due, reporting false if the run was cancelled first.
//
// A due time already past returns immediately, so the driver fires late rather
// than dropping the arrival: a skipped arrival is load the fleet was never
// offered, and nothing in the record would show it was missing. Being late is
// visible instead, on the row that was late.
func waitUntil(ctx context.Context, due time.Time) bool {
	d := time.Until(due)
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
