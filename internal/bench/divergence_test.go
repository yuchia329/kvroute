package bench_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/fakereplica"
)

// AC1 of #17: every request records the router's prediction beside the engine's
// own account of what it did not have to compute. Both sides on one row, because
// the divergence between them is the result and a figure assembled from two
// files nobody joined is not one.
func TestEveryRequestRecordsWhatTheEngineDidNotHaveToCompute(t *testing.T) {
	// A replica that answers a quarter of every prompt out of its cache.
	target, _, _ := fleetUnderTest(t, fakereplica.Config{OutputTokens: 4, CachedPromptFraction: 0.25})

	rows := drive(t, bench.DriverConfig{Target: target, Concurrency: 1, Duration: 60 * time.Millisecond})
	if len(rows) == 0 {
		t.Fatal("the cell sent nothing")
	}
	for _, r := range rows {
		if !r.EngineUsageRead {
			t.Fatalf("row %s carries no engine usage, so its prediction has nothing to be checked against", r.RequestID)
		}
		if r.EnginePromptTokens <= 0 {
			t.Fatalf("row %s reports %d engine prompt tokens", r.RequestID, r.EnginePromptTokens)
		}
		if !r.EngineCacheRead {
			t.Fatalf("row %s carries no cached-token account", r.RequestID)
		}
		want := r.EnginePromptTokens / 4
		if r.EngineCachedTokens != want {
			t.Errorf("row %s: engine cached tokens = %d, want %d", r.RequestID, r.EngineCachedTokens, want)
		}
		computed, ok := r.ComputedPrefillTokens()
		if !ok || computed != r.EnginePromptTokens-r.EngineCachedTokens {
			t.Errorf("row %s: computed prefill = %d (%v), want %d",
				r.RequestID, computed, ok, r.EnginePromptTokens-r.EngineCachedTokens)
		}
	}
}

// A replica that reports no usage at all leaves the row honestly empty rather
// than reporting an engine that computed nothing. "Nobody looked" and "nothing
// was cached" are the distinction every other reading in this project keeps.
func TestARequestWithNoEngineUsageSaysSoRatherThanReportingZero(t *testing.T) {
	// The fake only emits the usage chunk when the request asks for it, so a
	// workload that does not ask stands in for an engine that does not tell.
	target, _, _ := fleetUnderTest(t, fakereplica.Config{OutputTokens: 4, CachedPromptFraction: 0.25})

	rows := drive(t, bench.DriverConfig{
		Target:      target,
		Concurrency: 1,
		Duration:    60 * time.Millisecond,
		Workload:    silentUsage{model: testModel},
	})
	if len(rows) == 0 {
		t.Fatal("the cell sent nothing")
	}
	for _, r := range rows {
		if r.EngineUsageRead || r.EngineCacheRead {
			t.Errorf("row %s claims engine usage it was never sent", r.RequestID)
		}
		if _, ok := r.ComputedPrefillTokens(); ok {
			t.Errorf("row %s reports computed prefill with no usage behind it", r.RequestID)
		}
	}
}

// silentUsage is a workload that does not ask the engine for usage, standing in
// for an engine that does not report it.
type silentUsage struct{ model string }

func (silentUsage) Name() string { return "silent-usage" }

func (s silentUsage) Next(user, turn int) bench.Turn {
	body, _ := json.Marshal(map[string]any{
		"model":      s.model,
		"messages":   []map[string]string{{"role": "user", "content": strings.Repeat("x", 512)}},
		"stream":     true,
		"max_tokens": 4,
	})
	return bench.Turn{Session: "silent", Index: turn, Body: body}
}

// The conversion from the router's bytes to the engine's tokens is measured on
// the request it converts, not taken off a run-wide average. Both sides of it
// are already on the row, so a per-request ratio costs nothing and cannot be
// wrong about the request it is applied to.
func TestThePredictionIsConvertedAtTheRequestsOwnBytesPerToken(t *testing.T) {
	row := bench.Result{
		PromptBytes:        4096,
		EnginePromptTokens: 1024, // four bytes a token, on this request
		EngineCachedTokens: 100,
		EngineUsageRead:    true,
		EngineCacheRead:    true,
		PrefixMatchBytes:   2048,
	}

	predicted, ok := row.PredictedCachedTokens()
	if !ok {
		t.Fatal("a row carrying both sides reports no prediction in tokens")
	}
	if predicted != 512 {
		t.Errorf("predicted cached tokens = %v, want 512", predicted)
	}
}

// A request that sent no prompt bytes has no ratio, and a row that reported one
// anyway would be dividing by nothing.
func TestARowWithNoPromptBytesConvertsNothing(t *testing.T) {
	row := bench.Result{EnginePromptTokens: 10, EngineUsageRead: true, EngineCacheRead: true, PrefixMatchBytes: 64}
	if _, ok := row.PredictedCachedTokens(); ok {
		t.Error("a row with no prompt bytes reports a prediction in tokens")
	}
}

// AC2 of #17 needs divergence against working set ratio, so a cell has to carry
// the WS point it offered rather than leave it buried in the workload name.
func TestACellCarriesTheWorkingSetItOffered(t *testing.T) {
	offered, err := bench.NewMultiTurn(bench.MultiTurnWorkload{
		Model: testModel, WorkingSet: 3, CapacityTokens: 629_760,
	})
	if err != nil {
		t.Fatalf("new multi-turn: %v", err)
	}
	if got := bench.OfferedWorkingSet(offered); got != 3 {
		t.Errorf("working set = %v, want 3", got)
	}
}

// The sweep hands every cell a Shifted workload, so a wrapper that did not
// forward the working set would silently report every cell as unstated — and the
// WS axis of the divergence report would be one empty column that nothing failed
// over.
func TestShiftingAWorkloadDoesNotHideItsWorkingSet(t *testing.T) {
	offered, err := bench.NewMultiTurn(bench.MultiTurnWorkload{
		Model: testModel, WorkingSet: 3, CapacityTokens: 629_760,
	})
	if err != nil {
		t.Fatalf("new multi-turn: %v", err)
	}
	shifted := bench.Shifted(offered, bench.WorkloadStride)
	if got := bench.OfferedWorkingSet(shifted); got != 3 {
		t.Errorf("working set through Shifted = %v, want 3: the wrapper hides the workload's own", got)
	}
}

// A pool given as a session count still offers a working set, as long as the
// measured capacity it is a ratio of was passed. Deriving it rather than
// demanding the ratio flag is what puts the frozen headline workload — which
// names a count on purpose — onto the WS axis without changing the bytes it
// sends.
func TestASessionCountStillStatesAWorkingSetAgainstMeasuredCapacity(t *testing.T) {
	offered, err := bench.NewMultiTurn(bench.MultiTurnWorkload{
		Model: testModel, Sessions: 307, CapacityTokens: 629_760,
		TurnsPerSession: 4, PromptTokens: 448, OutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("new multi-turn: %v", err)
	}
	// 307 sessions of 2,048 tokens against 629,760 is WS 1.0.
	if got := bench.OfferedWorkingSet(offered); got < 0.99 || got > 1.01 {
		t.Errorf("working set = %v, want about 1", got)
	}
	if !strings.Contains(offered.Name(), "sessions=307") {
		t.Errorf("deriving the WS changed the workload's name: %q", offered.Name())
	}
}

// Without the measured capacity there is no ratio to state, and a workload that
// invented one would put every cell at a WS point nothing measured.
func TestAWorkloadWithNoMeasuredCapacityStatesNoWorkingSet(t *testing.T) {
	offered, err := bench.NewMultiTurn(bench.MultiTurnWorkload{Model: testModel, Sessions: 307})
	if err != nil {
		t.Fatalf("new multi-turn: %v", err)
	}
	if got := bench.OfferedWorkingSet(offered); got != 0 {
		t.Errorf("working set = %v, want 0 — nothing measured the capacity it would be a ratio of", got)
	}
}

// The fixed workload has no session pool and so no working set, and saying zero
// is how the report knows to bin its cells as unstated rather than at WS 0.
func TestTheFixedWorkloadStatesNoWorkingSet(t *testing.T) {
	if got := bench.OfferedWorkingSet(bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel})); got != 0 {
		t.Errorf("working set = %v, want 0", got)
	}
}
