package stats

import (
	"testing"
	"time"
)

// TestPercentilesAreNotBiasedTowardTheWarmUp: past capacity the recorder must
// keep a sample of the whole run, not the first observations. A recorder that
// kept the first N would report the warm-up's percentiles for a long run.
func TestPercentilesAreNotBiasedTowardTheWarmUp(t *testing.T) {
	r := NewRecorder(1000)

	// A slow warm-up followed by ten times as many fast requests: percentiles
	// over the whole run are fast, percentiles over the warm-up are slow.
	for range 1000 {
		r.Observe(100 * time.Millisecond)
	}
	for range 10_000 {
		r.Observe(time.Millisecond)
	}

	got := r.Summary()
	if got.Observed != 11_000 {
		t.Errorf("observed = %d, want 11000", got.Observed)
	}
	if got.Sampled != 1000 {
		t.Errorf("sampled = %d, want the capacity of 1000", got.Sampled)
	}
	if got.P50 > 10*time.Millisecond {
		t.Errorf("p50 = %v: the summary is dominated by the warm-up it should have aged out", got.P50)
	}
}

func TestSummaryOfNothingIsEmpty(t *testing.T) {
	got := NewRecorder(0).Summary()
	if got.Observed != 0 || got.Sampled != 0 || got.P50 != 0 {
		t.Errorf("empty recorder summarised as %+v", got)
	}
}

func TestQuantilesUseNearestRank(t *testing.T) {
	r := NewRecorder(0)
	for i := 1; i <= 100; i++ {
		r.Observe(time.Duration(i) * time.Millisecond)
	}
	got := r.Summary()
	if got.P50 != 50*time.Millisecond {
		t.Errorf("p50 = %v, want 50ms", got.P50)
	}
	if got.P99 != 99*time.Millisecond {
		t.Errorf("p99 = %v, want 99ms", got.P99)
	}
	if got.Max != 100*time.Millisecond {
		t.Errorf("max = %v, want 100ms", got.Max)
	}
}
