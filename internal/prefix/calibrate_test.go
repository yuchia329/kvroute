package prefix_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// idle is an idle-before-evict reading whose p90 falls at a known point: 90 of
// 100 observations are at or below 20s, so the quantile lands on that boundary.
func idle() vllmmetrics.Distribution {
	return vllmmetrics.Distribution{
		Bounds:     []float64{1, 5, 20, 60},
		Cumulative: []float64{10, 40, 90, 100},
		Sum:        1500,
		Count:      100,
		Read:       true,
	}
}

// The cap models the fleet: its capacity in tokens, converted to prompt bytes,
// divided into blocks. Sized this way it is a modelling decision that can be
// re-derived, which is the whole difference between it and a constant.
func TestTheNodeCapIsTheFleetsCapacityConvertedToBlocks(t *testing.T) {
	// The measured figures for this host: 125,952 tokens a replica across the
	// five the fleet runs on, GPU 3 having been left out for thermal throttling.
	c := prefix.Calibration{FleetTokens: 629_760, PromptBytesPerToken: 3.8, BlockIdle: idle()}

	want := int(math.Round(629_760 * 3.8 / prefix.BlockBytes))
	if got := c.NodeCap(); got != want {
		t.Errorf("node cap = %d, want %d", got, want)
	}
	// Sanity on the order of magnitude: an index of tens of thousands of nodes,
	// not hundreds or millions.
	if got := c.NodeCap(); got < 10_000 || got > 1_000_000 {
		t.Errorf("node cap = %d, which does not look like a fleet-sized index", got)
	}
}

// The TTL comes off the engine's own distribution rather than out of the air.
func TestTheTTLIsTakenFromTheEnginesIdleBeforeEvictTail(t *testing.T) {
	c := prefix.Calibration{FleetTokens: 629_760, PromptBytesPerToken: 3.8, BlockIdle: idle()}

	ttl, err := c.TTL()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl != 20*time.Second {
		t.Errorf("TTL = %v, want 20s — the p90 of the reading", ttl)
	}
}

// The refusal that matters. Without --kv-cache-metrics the family is absent
// entirely, and the temptation is to enable it and re-scrape — which changes the
// engine configuration every cell is supposed to share. The error has to say so,
// because the person reading it is mid-experiment.
func TestAnUncalibratedIndexIsRefusedRatherThanGivenADefault(t *testing.T) {
	c := prefix.Calibration{FleetTokens: 629_760, PromptBytesPerToken: 3.8}

	_, err := c.Config()
	if err == nil {
		t.Fatal("an index was built with no idle-before-evict reading at all")
	}
	for _, want := range []string{"--kv-cache-metrics", "invalidates every completed cell"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// A published but empty histogram is a different failure from an absent one, and
// says something different to whoever is reading it: the fleet has not evicted a
// block yet.
func TestAnEmptyHistogramIsRefusedAsItsOwnFailure(t *testing.T) {
	c := prefix.Calibration{
		FleetTokens:         629_760,
		PromptBytesPerToken: 3.8,
		BlockIdle:           vllmmetrics.Distribution{Bounds: []float64{1}, Cumulative: []float64{0}, Read: true},
	}

	_, err := c.Config()
	if err == nil {
		t.Fatal("an index was calibrated against a histogram with nothing in it")
	}
	if !strings.Contains(err.Error(), "no block has been evicted yet") {
		t.Errorf("the refusal does not say the fleet has evicted nothing: %v", err)
	}
}

// Each missing measurement is named, so the person holding the error knows which
// pass to go and run.
func TestEachMissingMeasurementIsNamedInTheRefusal(t *testing.T) {
	for _, tc := range []struct {
		name        string
		calibration prefix.Calibration
		mentions    string
	}{
		{"no capacity", prefix.Calibration{PromptBytesPerToken: 3.8, BlockIdle: idle()}, "fleet KV capacity"},
		{"no ratio", prefix.Calibration{FleetTokens: 1000, BlockIdle: idle()}, "bytes-per-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.calibration.Config()
			if err == nil {
				t.Fatal("an index was built without a measurement it needs")
			}
			if !strings.Contains(err.Error(), tc.mentions) {
				t.Errorf("the refusal does not mention %q: %v", tc.mentions, err)
			}
		})
	}
}

// A calibration that has everything produces an index that will actually build.
func TestACompleteCalibrationBuildsAnIndex(t *testing.T) {
	c := prefix.Calibration{FleetTokens: 629_760, PromptBytesPerToken: 3.8, BlockIdle: idle()}

	cfg, err := c.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if _, err := prefix.New(cfg); err != nil {
		t.Fatalf("New(%+v): %v", cfg, err)
	}
}

// The ratio is measured from two independently observed quantities: the bytes
// the harness sent, and the tokens the engines said they processed.
func TestTheBytesPerTokenRatioIsMeasuredFromBothSides(t *testing.T) {
	prefill := vllmmetrics.Prefill{PromptTokens: 1000, CachedTokens: 250, Read: true}

	got, ok := prefix.MeasureBytesPerToken(3800, prefill)
	if !ok {
		t.Fatal("a ratio could not be measured from a complete window")
	}
	if math.Abs(got-3.8) > 1e-9 {
		t.Errorf("bytes per token = %v, want 3.8", got)
	}

	if _, ok := prefix.MeasureBytesPerToken(3800, vllmmetrics.Prefill{}); ok {
		t.Error("a ratio was measured against an unread prefill reading")
	}
	if _, ok := prefix.MeasureBytesPerToken(0, prefill); ok {
		t.Error("a ratio was measured over a window in which nothing was sent")
	}
}
