package characterize

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// DefaultEngineSampleInterval is how often a probe asks a replica what it is
// doing. Fast enough to see a batch form and drain inside a 90-second probe,
// slow enough that scraping is not itself load on the host.
const DefaultEngineSampleInterval = time.Second

// EngineLoad is what the engine said about its own queue while a probe ran.
//
// It exists because offered load and actual load are different questions. A
// driver holding 32 requests against a replica knows 32 are outstanding; it
// does not know how many the engine put in a batch and how many are queued
// behind them. The contention experiment is about the second — whether a
// replica sharing a NUMA node with three others gets through fewer of them per
// step — and without this the record could only say what was asked for.
type EngineLoad struct {
	Samples int `json:"samples"`
	// Running is vllm:num_requests_running: the engine's actual batch.
	MeanRunning float64 `json:"mean_running"`
	MaxRunning  float64 `json:"max_running"`
	// Waiting is vllm:num_requests_waiting: what is queued behind it. Never
	// call this inflight — inflight is the router's count, and there is no
	// router in a probe's path.
	MeanWaiting float64 `json:"mean_waiting"`
	MaxWaiting  float64 `json:"max_waiting"`
	// Read is false when the gauges could never be scraped, so "the engine was
	// idle" and "nobody asked" do not read the same.
	Read bool `json:"read"`
}

// engineWatcher samples one replica's queue gauges for the length of a probe.
type engineWatcher struct {
	stop context.CancelFunc
	done chan struct{}

	mu     sync.Mutex
	result EngineLoad
	// Sums rather than a slice of samples: a 90-second probe at one sample a
	// second needs a mean and a max, and keeping every reading to compute two
	// numbers would put a growing allocation inside every probe.
	runningSum, waitingSum float64
}

// watchEngineLoad starts sampling until the returned watcher is stopped. A nil
// client samples nothing and reports load whose reading was never taken.
func watchEngineLoad(ctx context.Context, client *http.Client, replica fleet.Replica, interval time.Duration) *engineWatcher {
	if interval <= 0 {
		interval = DefaultEngineSampleInterval
	}
	w := &engineWatcher{done: make(chan struct{})}

	// Detached from the parent's cancellation so that an interrupted probe
	// still reports what it saw, and stopped explicitly by Stop.
	sampling, stop := context.WithCancel(context.WithoutCancel(ctx))
	w.stop = stop
	go w.sample(sampling, client, replica, interval)
	return w
}

// Stop ends sampling and returns what the engine reported.
func (w *engineWatcher) Stop() EngineLoad {
	w.stop()
	<-w.done

	w.mu.Lock()
	defer w.mu.Unlock()
	result := w.result
	if result.Samples > 0 {
		result.Read = true
		result.MeanRunning = w.runningSum / float64(result.Samples)
		result.MeanWaiting = w.waitingSum / float64(result.Samples)
	}
	return result
}

func (w *engineWatcher) sample(ctx context.Context, client *http.Client, replica fleet.Replica, interval time.Duration) {
	defer close(w.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Sampled immediately, so a probe short enough to finish inside one tick
	// still carries a reading.
	w.once(ctx, client, replica)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.once(ctx, client, replica)
		}
	}
}

func (w *engineWatcher) once(ctx context.Context, client *http.Client, replica fleet.Replica) {
	if client == nil {
		client = http.DefaultClient
	}
	body, err := scrape(ctx, client, replica.URL("/metrics", ""))
	if err != nil {
		return
	}
	running, runningOK := vllmmetrics.Value(body, vllmmetrics.NumRequestsRunning)
	waiting, waitingOK := vllmmetrics.Value(body, vllmmetrics.NumRequestsWaiting)
	if !runningOK || !waitingOK {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.result.Samples++
	w.runningSum += running
	w.waitingSum += waiting
	w.result.MaxRunning = max(w.result.MaxRunning, running)
	w.result.MaxWaiting = max(w.result.MaxWaiting, waiting)
}
