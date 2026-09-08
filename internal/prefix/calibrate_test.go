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

	got, ok := prefix.MeasurePromptBytesPerToken(3800, prefill)
	if !ok {
		t.Fatal("a ratio could not be measured from a complete window")
	}
	if math.Abs(got-3.8) > 1e-9 {
		t.Errorf("bytes per token = %v, want 3.8", got)
	}

	if _, ok := prefix.MeasurePromptBytesPerToken(3800, vllmmetrics.Prefill{}); ok {
		t.Error("a ratio was measured against an unread prefill reading")
	}
	if _, ok := prefix.MeasurePromptBytesPerToken(0, prefill); ok {
		t.Error("a ratio was measured over a window in which nothing was sent")
	}
}

// The acceptance criterion names both residency families. Idle-before-evict
// derives the TTL; block lifetime is the check on it, because a TTL longer than
// blocks live at all is a calibration that passed its own test while believing
// in blocks the fleet could never have been holding.
func TestATTLLongerThanBlocksLiveIsReportedAgainstTheLifetimeHistogram(t *testing.T) {
	// Blocks live a median of 5s here, against an idle tail whose p90 is 20s.
	shortLived := vllmmetrics.Distribution{
		Bounds:     []float64{1, 5, 20},
		Cumulative: []float64{10, 50, 100},
		Sum:        400,
		Count:      100,
		Read:       true,
	}
	c := prefix.Calibration{
		FleetTokens:         629_760,
		PromptBytesPerToken: 3.8,
		BlockIdle:           idle(),
		BlockLifetime:       shortLived,
	}

	cfg, err := c.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	warning, disagrees := c.CheckAgainstLifetime(cfg.TTL)
	if !disagrees {
		t.Fatalf("a TTL of %v against a median lifetime of 5s was not reported", cfg.TTL)
	}
	if !strings.Contains(warning, "median block lifetime") {
		t.Errorf("the warning does not name what it checked against: %q", warning)
	}
}

// The two families can legitimately disagree, so this is a warning and never a
// refusal — and a fleet that published no lifetime reading is simply not checked
// rather than failed.
func TestTheLifetimeCheckNeitherRefusesNorInventsAVerdict(t *testing.T) {
	consistent := prefix.Calibration{
		FleetTokens:         629_760,
		PromptBytesPerToken: 3.8,
		BlockIdle:           idle(),
		BlockLifetime: vllmmetrics.Distribution{
			Bounds:     []float64{60, 300},
			Cumulative: []float64{50, 100},
			Sum:        9000,
			Count:      100,
			Read:       true,
		},
	}
	cfg, err := consistent.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if _, disagrees := consistent.CheckAgainstLifetime(cfg.TTL); disagrees {
		t.Error("a TTL well inside the median block lifetime was reported as inconsistent")
	}

	// No lifetime reading at all: no check, and still a usable calibration.
	unread := prefix.Calibration{FleetTokens: 629_760, PromptBytesPerToken: 3.8, BlockIdle: idle()}
	if _, err := unread.Config(); err != nil {
		t.Errorf("a calibration without the lifetime reading was refused: %v", err)
	}
	if _, disagrees := unread.CheckAgainstLifetime(time.Hour); disagrees {
		t.Error("a verdict was invented from a lifetime histogram nobody read")
	}
}

// A chosen TTL is usable where a derived one is impossible — the residency
// histograms are empty until the fleet has evicted something, and a sweep may
// have to start against a fleet that has just come up. What it must never do is
// pass itself off as a measurement.
func TestAChosenTTLIsUsableAndSaysThatItWasChosen(t *testing.T) {
	c := prefix.Calibration{FleetTokens: 629_760, PromptBytesPerToken: 1.59, ChosenTTL: 20 * time.Second}

	cfg, err := c.Config()
	if err != nil {
		t.Fatalf("Config with a chosen TTL and no histogram: %v", err)
	}
	if cfg.TTL != 20*time.Second {
		t.Errorf("TTL = %v, want the chosen 20s", cfg.TTL)
	}
	if c.TTLSource() != "chosen" {
		t.Errorf("TTLSource = %q, want it to say the figure was chosen", c.TTLSource())
	}

	// And a derived one still says so, so the two can never be confused in a
	// write-up.
	derived := prefix.Calibration{FleetTokens: 629_760, PromptBytesPerToken: 1.59, BlockIdle: idle()}
	if !strings.Contains(derived.TTLSource(), "measured") {
		t.Errorf("TTLSource = %q, want it to name the measurement", derived.TTLSource())
	}
}

// The choice covers the TTL and nothing else. Capacity and the ratio are still
// required, because those are measurable on a fleet that has just come up.
func TestAChosenTTLDoesNotExcuseTheOtherMeasurements(t *testing.T) {
	if _, err := (prefix.Calibration{ChosenTTL: 20 * time.Second}).Config(); err == nil {
		t.Error("a chosen TTL was accepted as a substitute for fleet capacity")
	}
	if _, err := (prefix.Calibration{FleetTokens: 629_760, ChosenTTL: 20 * time.Second}).Config(); err == nil {
		t.Error("a chosen TTL was accepted as a substitute for the bytes-per-token ratio")
	}
}
