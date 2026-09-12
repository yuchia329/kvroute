package belief_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/belief"
)

// Replaying a measured run through different windows, to ask whether the
// honoured rate's saturation is a property of the signal or of the window it is
// averaged over.
//
// #28's observing run (2026-09-12, WS 3 skew 0, c32, spill off) came back with
// the rate pinned: 70% of readings exactly 1.0, p50 = p90 = max = 1.0, and a
// total spread of 1.7 percentage points across the whole load range. Two
// explanations fit that, and they call for different work:
//
//   - STRUCTURAL. The prefix index is calibrated not to over-predict (ADR-0006,
//     ADR-0008): its TTL and node cap make it drop beliefs before the engines
//     evict the blocks, so it is nearly always right about whatever it still
//     claims. Then "are this replica's claims honoured" measures the index's own
//     conservatism, no window will uncover a range, and the branch needs a
//     different signal altogether.
//   - SMOOTHING. At roughly two scoring requests per second per replica, the
//     production window (64 requests, 30 s) is TTL-bound and averages over some
//     thirty seconds, which could flatten real short-lived dips.
//
// A fleet run cannot separate those cheaply, but a replay can, because every
// pair the rate is made of — what the router claimed and what the engine said it
// held — is on the rows the run already wrote. So this reads them back and feeds
// them through the real Feedback at several windows.
//
// It is opt-in, like the libzmq interop tests, because it needs a run's rows:
//
//	KVROUTE_REPLAY=path/to/requests.jsonl go test ./internal/belief/ -run Replay -v
//
// The spread alone is not the answer, and reading it as one is the trap. A short
// window widens the histogram whether or not it adds information: one
// dishonoured request in eight drags a reading to 0.5, which is what the quorum
// exists to prevent. So this also asks whether a low reading PREDICTS anything —
// whether the requests that follow one are honoured less than the requests that
// follow a high reading. A signal whose spread grows while its predictive gap
// does not has become noise with a wider histogram, and thresholding it would
// spill on coin flips.
func TestReplayMeasuredRunThroughDifferentWindows(t *testing.T) {
	path := os.Getenv("KVROUTE_REPLAY")
	if path == "" {
		t.Skip("set KVROUTE_REPLAY to a run's per-request rows (.jsonl) to replay them")
	}
	pairs := loadScoringPairs(t, path)
	if len(pairs) == 0 {
		t.Fatalf("%s carried no scoring requests: no row claimed a match and had its usage read", path)
	}
	t.Logf("replaying %d scoring requests across %d replicas", len(pairs), countReplicas(pairs))

	windows := []belief.Window{
		{Requests: 64, TTL: 30 * time.Second, Quorum: 8}, // the production default
		{Requests: 32, TTL: 30 * time.Second, Quorum: 8},
		{Requests: 16, TTL: 10 * time.Second, Quorum: 4},
		{Requests: 8, TTL: 10 * time.Second, Quorum: 4},
		{Requests: 8, TTL: 5 * time.Second, Quorum: 2},
	}

	t.Logf("%-22s %7s %7s %7s %7s %7s  %9s  %s", "window", "read", "min", "p05", "p25", "p50", "spread", "predictive gap")
	for _, w := range windows {
		if err := w.Validate(); err != nil {
			t.Fatalf("window %v: %v", w, err)
		}
		r := replay(pairs, w)
		if r.read == 0 {
			t.Logf("%-22s %7d  (never reached quorum)", w.String(), 0)
			continue
		}
		t.Logf("%-22s %7d %7.4f %7.4f %7.4f %7.4f  %9.4f  %s",
			w.String(), r.read, r.min, r.q(0.05), r.q(0.25), r.q(0.50), r.q(0.50)-r.min, r.predictive())

		for _, x := range r.readings {
			if x < 0 || x > 1 {
				t.Fatalf("window %v produced a rate of %v, outside [0,1]", w, x)
			}
		}
	}
}

// scoringPair is one request that says something about whether a replica is
// honouring beliefs: what the router claimed of it, in the engine's tokens, and
// how much of that the engine turned out to hold.
type scoringPair struct {
	replica string
	at      time.Time
	claimed float64
	held    float64
}

// honoured is this one request's share, which is what a window of them averages.
func (p scoringPair) honoured() float64 {
	if p.claimed <= 0 {
		return 0
	}
	return min(p.claimed, p.held) / p.claimed
}

func loadScoringPairs(t *testing.T, path string) []scoringPair {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var pairs []scoringPair
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for scanner.Scan() {
		var row struct {
			Replica            string `json:"replica"`
			StartedAtNs        int64  `json:"started_at_ns"`
			PrefixMatchBytes   int    `json:"prefix_match_bytes"`
			PrefixMatchTokens  int    `json:"prefix_match_tokens"`
			PromptBytes        int    `json:"prompt_bytes"`
			EnginePromptTokens int    `json:"engine_prompt_tokens"`
			EngineCachedTokens int    `json:"engine_cached_tokens"`
			EngineCacheRead    bool   `json:"engine_cache_read"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		if !row.EngineCacheRead || row.Replica == "" {
			continue
		}
		// The same conversion the router applies live, so the replay scores what
		// the run scored rather than a second definition of it.
		claimed, ok := belief.PredictedTokens(row.PrefixMatchBytes, row.PrefixMatchTokens, row.PromptBytes, row.EnginePromptTokens)
		if !ok {
			continue
		}
		pairs = append(pairs, scoringPair{
			replica: row.Replica,
			at:      time.Unix(0, row.StartedAtNs),
			claimed: claimed,
			held:    float64(row.EngineCachedTokens),
		})
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].at.Before(pairs[j].at) })
	return pairs
}

func countReplicas(pairs []scoringPair) int {
	seen := map[string]bool{}
	for _, p := range pairs {
		seen[p.replica] = true
	}
	return len(seen)
}

// result is what one window produced over the replay.
type result struct {
	readings []float64
	min      float64
	read     int
	// lowNext and highNext are what happened AFTER a reading: the mean honoured
	// share of the next request on that replica, for readings in the bottom and
	// top fifth. The gap between them is whether the signal predicts anything.
	lowNext, highNext float64
	lowN, highN       int
}

func (r result) q(p float64) float64 {
	if len(r.readings) == 0 {
		return 0
	}
	return r.readings[min(int(p*float64(len(r.readings))), len(r.readings)-1)]
}

func (r result) predictive() string {
	if r.lowN == 0 || r.highN == 0 {
		return "n/a"
	}
	return fmt.Sprintf("after a low reading %.4f vs %.4f after a high one (gap %+.4f, n=%d/%d)",
		r.lowNext, r.highNext, r.lowNext-r.highNext, r.lowN, r.highN)
}

// replay feeds the pairs through the real Feedback in time order, per replica,
// reading the rate before each observation exactly as the router does: it
// decides on what it knows, then learns from the answer.
func replay(pairs []scoringPair, w belief.Window) result {
	feeds := map[string]*belief.Feedback{}
	type sample struct{ rate, next float64 }
	var samples []sample
	var res result

	// pending holds, per replica, the rate read at the last decision, waiting for
	// that replica's next request to say what followed it.
	pending := map[string]float64{}
	pendingSet := map[string]bool{}

	for _, p := range pairs {
		f, ok := feeds[p.replica]
		if !ok {
			f = belief.NewFeedback(w)
			feeds[p.replica] = f
		}
		if pendingSet[p.replica] {
			samples = append(samples, sample{rate: pending[p.replica], next: p.honoured()})
			pendingSet[p.replica] = false
		}
		if reading := f.Rate(p.at); reading.Read {
			res.readings = append(res.readings, reading.Fraction)
			pending[p.replica], pendingSet[p.replica] = reading.Fraction, true
		}
		f.Observe(p.at, p.claimed, p.held)
	}

	res.read = len(res.readings)
	if res.read == 0 {
		return res
	}
	sort.Float64s(res.readings)
	res.min = res.readings[0]

	// The predictive gap, over the readings that had a next request to check.
	sort.Slice(samples, func(i, j int) bool { return samples[i].rate < samples[j].rate })
	fifth := len(samples) / 5
	if fifth == 0 {
		return res
	}
	for _, s := range samples[:fifth] {
		res.lowNext += s.next
	}
	for _, s := range samples[len(samples)-fifth:] {
		res.highNext += s.next
	}
	res.lowN, res.highN = fifth, fifth
	res.lowNext /= float64(fifth)
	res.highNext /= float64(fifth)
	return res
}
