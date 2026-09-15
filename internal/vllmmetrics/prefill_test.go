package vllmmetrics_test

import (
	"testing"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

const prefillExposition = `
# TYPE vllm:prompt_tokens_total counter
vllm:prompt_tokens_total{model_name="llama"} 10000
# TYPE vllm:prompt_tokens_cached_total counter
vllm:prompt_tokens_cached_total{model_name="llama"} 4000
`

func TestRecomputedPrefillIsPromptTokensThatMissedTheCache(t *testing.T) {
	p := vllmmetrics.ReadPrefillFrom(prefillExposition)

	if !p.Evidenced() {
		t.Fatalf("prefill not evidenced: %+v", p)
	}
	if got := p.Recomputed(); got != 6000 {
		t.Errorf("recomputed = %v, want 6000", got)
	}
	if got := p.CacheRate(); got != 0.4 {
		t.Errorf("cache rate = %v, want 0.4", got)
	}
}

// The counter this project needs was removed from vLLM, so both halves of the
// derivation are required. Half a pair is not a smaller reading — nothing can be
// derived from it — and it must not read as a fleet that computed everything.
func TestPrefillNeedsBothCountersOrNeither(t *testing.T) {
	if p := vllmmetrics.ReadPrefillFrom(`vllm:prompt_tokens_total 10000`); p.Read {
		t.Errorf("a reading with only the prompt counter read as present: %+v", p)
	}
	if p := vllmmetrics.ReadPrefillFrom(""); p.Read {
		t.Errorf("a reading of nothing read as present: %+v", p)
	}
}

// A window is the difference between two readings, and a replica that restarted
// inside one did not measure a window at all.
func TestAWindowIsTheDifferenceAndARestartVoidsIt(t *testing.T) {
	before := vllmmetrics.Prefill{PromptTokens: 1000, CachedTokens: 400, Read: true}
	after := vllmmetrics.Prefill{PromptTokens: 3000, CachedTokens: 1400, Read: true}

	window := after.Since(before)
	if window.PromptTokens != 2000 || window.CachedTokens != 1000 {
		t.Errorf("window = %+v, want 2000 prompt and 1000 cached", window)
	}

	restarted := vllmmetrics.Prefill{PromptTokens: 100, CachedTokens: 10, Read: true}.Since(before)
	if restarted.Read {
		t.Errorf("a counter that went backwards produced a window: %+v", restarted)
	}
}

// A fleet-wide figure needs every replica in it. One unread replica does not
// make the sum smaller, it makes it a different quantity.
func TestPoolingPrefillNeedsEveryReplica(t *testing.T) {
	pooled := vllmmetrics.PoolPrefill([]vllmmetrics.Prefill{
		{PromptTokens: 100, CachedTokens: 40, Read: true},
		{PromptTokens: 300, CachedTokens: 60, Read: true},
	})
	if pooled.PromptTokens != 400 || pooled.CachedTokens != 100 {
		t.Errorf("pooled = %+v, want 400 prompt and 100 cached", pooled)
	}

	partial := vllmmetrics.PoolPrefill([]vllmmetrics.Prefill{
		{PromptTokens: 100, CachedTokens: 40, Read: true},
		{},
	})
	if partial.Read {
		t.Errorf("a pool missing a replica read as a fleet measurement: %+v", partial)
	}
}

// Both counters are declared, so the contract test asserts them against a live
// replica rather than this project discovering a rename inside a sweep.
func TestTheCountersRedundantPrefillNeedsAreDeclaredInTheContract(t *testing.T) {
	declared := map[string]bool{}
	for _, f := range vllmmetrics.Required {
		declared[f.Name] = true
	}
	for _, name := range []string{vllmmetrics.PromptTokens, vllmmetrics.PromptTokensCached} {
		if !declared[name] {
			t.Errorf("%s is scraped but not in Required, so no contract test checks it", name)
		}
	}
}
