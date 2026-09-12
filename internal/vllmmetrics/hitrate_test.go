package vllmmetrics_test

import (
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

func served(hits, queries float64) vllmmetrics.PrefixCache {
	return vllmmetrics.PrefixCache{Hits: hits, Queries: queries, Read: true}
}

// The rate is what the replica served over the window, not over its lifetime.
// The counters are cumulative, so a lifetime ratio barely moves once a run is
// under way — which is the whole reason this type exists rather than
// PrefixCache.HitRate being read directly.
func TestTheRateIsWhatTheWindowSawRatherThanTheLifetime(t *testing.T) {
	now := time.Now()
	w := vllmmetrics.NewHitRateWindow(vllmmetrics.HitRateSpan{Window: 10 * time.Second, MinQueries: 10})

	// A long, healthy history: 9,000 of 10,000 blocks hit.
	w.Observe(now, served(9000, 10000))
	// Then the window's worth of eviction: 100 hits in the next 1,000 queries.
	w.Observe(now.Add(2*time.Second), served(9100, 11000))

	got := w.Rate(now.Add(2 * time.Second))
	if !got.Read {
		t.Fatal("two readings a window apart produced no rate")
	}
	if got.Fraction != 0.1 {
		t.Errorf("rate = %v, want 0.1: the window saw 100 hits in 1000 queries, whatever the lifetime says",
			got.Fraction)
	}
	if got.Queries != 1000 {
		t.Errorf("queries = %v, want the window's 1000", got.Queries)
	}
}

// A single reading is a point on a cumulative counter and says nothing about a
// rate. Reporting the lifetime ratio there would make a replica look healthy for
// the whole first window of a run.
func TestOneReadingIsNotARate(t *testing.T) {
	now := time.Now()
	w := vllmmetrics.NewHitRateWindow(vllmmetrics.HitRateSpan{Window: 10 * time.Second, MinQueries: 10})
	w.Observe(now, served(9000, 10000))

	if got := w.Rate(now); got.Read {
		t.Errorf("a single cumulative reading produced a rate of %v", got)
	}
}

// Too little traffic through the window is no evidence. A replica that served
// three blocks in ten seconds has a rate that is arithmetic rather than
// measurement, and thresholding it would spill on noise.
func TestARateOverTooFewQueriesIsUnread(t *testing.T) {
	now := time.Now()
	w := vllmmetrics.NewHitRateWindow(vllmmetrics.HitRateSpan{Window: 10 * time.Second, MinQueries: 500})
	w.Observe(now, served(0, 0))
	w.Observe(now.Add(time.Second), served(1, 3))

	if got := w.Rate(now.Add(time.Second)); got.Read {
		t.Errorf("a rate over 3 queries was read as %v", got)
	}
}

// The graceful degradation every signal in this project shares, in the one place
// a policy cannot forget it.
func TestAnUnreadRateIsNeverUnderAnyLowWaterMark(t *testing.T) {
	var unread vllmmetrics.HitRate
	for _, mark := range []float64{0.01, 0.5, 0.99, 1} {
		if unread.Under(mark) {
			t.Errorf("an unread rate is under %v", mark)
		}
	}
}

// Strictly under, as every other threshold in the spill rule is.
func TestUnderIsStrict(t *testing.T) {
	exact := vllmmetrics.HitRate{Fraction: 0.5, Read: true, Queries: 1000}
	if exact.Under(0.5) {
		t.Error("a rate exactly at the mark is under it")
	}
	if !exact.Under(0.51) {
		t.Error("a rate below the mark is not under it")
	}
}

// The window has to forget, or a replica that was evicting and has stopped
// carries its worst stretch for the rest of the run.
func TestReadingsOlderThanTheWindowAreForgotten(t *testing.T) {
	now := time.Now()
	w := vllmmetrics.NewHitRateWindow(vllmmetrics.HitRateSpan{Window: 10 * time.Second, MinQueries: 10})

	w.Observe(now, served(0, 0))
	w.Observe(now.Add(1*time.Second), served(0, 1000)) // a bad stretch: nothing hit
	if got := w.Rate(now.Add(1 * time.Second)); got.Fraction != 0 {
		t.Fatalf("rate = %v, want 0 while the window holds the bad stretch", got.Fraction)
	}

	// Twenty seconds later, everything hits. The bad stretch is out of the window.
	w.Observe(now.Add(20*time.Second), served(0, 1000))
	w.Observe(now.Add(25*time.Second), served(1000, 2000))
	if got := w.Rate(now.Add(25 * time.Second)); got.Fraction != 1 {
		t.Errorf("rate = %v, want 1: the bad stretch aged out of the window", got.Fraction)
	}
}

// A replica that restarted has counters that went backwards, and a negative
// delta is not a rate. Reporting one would publish a fraction of a lifetime the
// window never measured — the same rule PrefixCache.Since applies.
func TestACounterThatWentBackwardsIsNotARate(t *testing.T) {
	now := time.Now()
	w := vllmmetrics.NewHitRateWindow(vllmmetrics.HitRateSpan{Window: 10 * time.Second, MinQueries: 10})
	w.Observe(now, served(9000, 10000))
	w.Observe(now.Add(time.Second), served(5, 20)) // restarted

	if got := w.Rate(now.Add(time.Second)); got.Read {
		t.Errorf("a restarted replica produced a rate of %v", got)
	}
}

// A scrape that did not answer is not evidence and must not enter the window: a
// replica nobody could reach has no rate, rather than the rate it last had.
func TestAnUnreadScrapeIsNotFolded(t *testing.T) {
	now := time.Now()
	w := vllmmetrics.NewHitRateWindow(vllmmetrics.HitRateSpan{Window: 10 * time.Second, MinQueries: 10})
	w.Observe(now, served(0, 0))
	w.Observe(now.Add(time.Second), vllmmetrics.PrefixCache{}) // scrape failed
	w.Observe(now.Add(2*time.Second), served(500, 1000))

	got := w.Rate(now.Add(2 * time.Second))
	if !got.Read || got.Fraction != 0.5 {
		t.Errorf("rate = %v, want 0.5 across the failed scrape", got)
	}
}

// Unread and zero print differently, because a replica hitting nothing and a
// replica nobody measured are the two states this type exists to separate.
func TestUnreadAndZeroDoNotPrintTheSame(t *testing.T) {
	unread := vllmmetrics.HitRate{}.String()
	zero := vllmmetrics.HitRate{Read: true, Queries: 100}.String()
	if unread == zero {
		t.Errorf("an unread rate and a rate of zero both print %q", unread)
	}
	if unread != "unread" {
		t.Errorf("an unread rate prints %q", unread)
	}
}

// A span that could never produce a reading is refused at startup rather than
// silently disabling the condition for a run, which is #28's whole lesson.
func TestASpanThatCannotProduceAReadingIsRefused(t *testing.T) {
	for _, s := range []vllmmetrics.HitRateSpan{
		{Window: 0, MinQueries: 10},
		{Window: 10 * time.Second, MinQueries: 0},
		{Window: -time.Second, MinQueries: 10},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v was accepted", s)
		}
	}
	if err := vllmmetrics.DefaultHitRateSpan.Validate(); err != nil {
		t.Errorf("the default span is invalid: %v", err)
	}
}
