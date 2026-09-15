package roofline_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/roofline"
)

// request is a completion as the engine received it.
type request struct {
	Model     string `json:"model"`
	Prompt    []int  `json:"prompt"`
	MaxTokens int    `json:"max_tokens"`
	IgnoreEOS bool   `json:"ignore_eos"`
	// profiled is whether the profile window was open when it arrived.
	profiled bool
	// concurrent is how many completions were in flight while it was, itself
	// included, at the most.
	concurrent int
}

// fakeEngine stands in for one vLLM replica serving its profiler endpoints. It
// holds every completion briefly, so requests meant to run together overlap.
type fakeEngine struct {
	*httptest.Server
	mu       sync.Mutex
	open     bool
	starts   int
	stops    int
	inflight []*request
	received []*request
}

func newFakeEngine(t *testing.T) *fakeEngine {
	e := &fakeEngine{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /start_profile", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.open = true
		e.starts++
		e.mu.Unlock()
	})
	mux.HandleFunc("POST /stop_profile", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.open = false
		e.stops++
		e.mu.Unlock()
	})
	mux.HandleFunc("POST /v1/completions", func(w http.ResponseWriter, r *http.Request) {
		req := &request{}
		if err := json.NewDecoder(r.Body).Decode(req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		e.mu.Lock()
		req.profiled = e.open
		e.inflight = append(e.inflight, req)
		for _, other := range e.inflight {
			other.concurrent = max(other.concurrent, len(e.inflight))
		}
		e.received = append(e.received, req)
		e.mu.Unlock()

		time.Sleep(20 * time.Millisecond)

		e.mu.Lock()
		for i, other := range e.inflight {
			if other == req {
				e.inflight = append(e.inflight[:i], e.inflight[i+1:]...)
				break
			}
		}
		e.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"usage": map[string]int{"prompt_tokens": len(req.Prompt), "completion_tokens": req.MaxTokens},
		})
	})
	e.Server = httptest.NewServer(mux)
	t.Cleanup(e.Close)
	return e
}

func (e *fakeEngine) requests() []*request {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*request(nil), e.received...)
}

var smallPlan = roofline.Plan{
	Model:        "llama",
	Prefill:      []int{128, 512},
	Repeats:      2,
	Decode:       []roofline.Batch{{Sequences: 1, Context: 32}, {Sequences: 4, Context: 32}},
	DecodeTokens: 8,
}

func drive(t *testing.T, e *fakeEngine, plan roofline.Plan) {
	t.Helper()
	if err := roofline.Drive(context.Background(), e.URL, plan, rand.New(rand.NewPCG(1, 2))); err != nil {
		t.Fatalf("the plan was not driven: %v", err)
	}
}

// The trace is only as clean as its window. The warm-up — the engine's first
// request after startup, which pays for whatever it initialises lazily — falls
// before the window opens, and every request of the plan falls inside it: two
// repeats of two prefill lengths, then a batch of one and a batch of four.
func TestTheProfileWindowHoldsThePlanAndNotTheWarmUp(t *testing.T) {
	e := newFakeEngine(t)
	drive(t, e, smallPlan)

	if e.starts != 1 || e.stops != 1 {
		t.Fatalf("the profile was started %d times and stopped %d; want one window", e.starts, e.stops)
	}
	var outside, inside int
	for _, r := range e.requests() {
		if r.profiled {
			inside++
		} else {
			outside++
		}
	}
	if outside != 1 {
		t.Errorf("%d requests ran outside the window, want the one warm-up", outside)
	}
	if inside != 2*2+1+4 {
		t.Errorf("%d requests ran inside the window, want 9", inside)
	}
	if e.open {
		t.Error("the profile window was left open")
	}
}

// A decode batch is what its point on the roofline stands for: B sequences
// sharing one read of the weights per step. Its requests have to be in flight
// together, or the engine decodes them as B batches of one. A prefill is the
// opposite and goes alone, so no step mixes two prompts, and one batch finishes
// before the next is sent.
func TestADecodeBatchIsInFlightTogetherAndPrefillsGoAlone(t *testing.T) {
	e := newFakeEngine(t)
	drive(t, e, smallPlan)

	var decodes []int
	for _, r := range e.requests() {
		if !r.profiled {
			continue
		}
		if r.MaxTokens == 1 && r.concurrent != 1 {
			t.Errorf("a %d-token prefill ran beside %d other requests", len(r.Prompt), r.concurrent-1)
		}
		if r.MaxTokens == smallPlan.DecodeTokens {
			decodes = append(decodes, r.concurrent)
		}
	}
	want := []int{1, 4, 4, 4, 4}
	if len(decodes) != len(want) {
		t.Fatalf("%d decoding requests, want %d", len(decodes), len(want))
	}
	for i := range want {
		if decodes[i] != want[i] {
			t.Fatalf("decoding requests were in flight %v at once, want %v", decodes, want)
		}
	}
}
