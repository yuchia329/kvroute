package bench_test

import (
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/record"
)

// slo is the pair of thresholds the summary evaluates against. Derived from the
// concurrency-1 floor in the real sweep; fixed here so the arithmetic is
// checkable by hand.
var slo = bench.SLO{TTFT: 3 * time.Second, ITL: 100 * time.Millisecond}

// success builds a successful result that took the given TTFT, with an ITL
// comfortably inside the SLO.
func success(startedAt time.Time, ttft time.Duration) bench.Result {
	return bench.Result{
		StartedAtNs: startedAt.UnixNano(),
		Outcome:     record.OutcomeSuccess,
		TTFTNs:      ttft.Nanoseconds(),
		ITLP50Ns:    (20 * time.Millisecond).Nanoseconds(),
		TotalNs:     (ttft + time.Second).Nanoseconds(),
	}
}

func outcome(startedAt time.Time, o record.Outcome) bench.Result {
	// Failures come back fast — that is the whole reason they are counted
	// apart. A 2 ms failure folded into the latency distribution makes an
	// overloaded fleet look quicker than a healthy one.
	return bench.Result{
		StartedAtNs: startedAt.UnixNano(),
		Outcome:     o,
		TTFTNs:      (2 * time.Millisecond).Nanoseconds(),
		TotalNs:     (2 * time.Millisecond).Nanoseconds(),
	}
}

func TestDroppedFailedAndSLOViolationsAreCountedInThreeSeparateColumns(t *testing.T) {
	start := time.Unix(1757000000, 0)
	results := []bench.Result{
		success(start, time.Second),
		success(start, 9*time.Second), // completed, missed the SLO
		outcome(start, record.OutcomeFailed),
		outcome(start, record.OutcomeFailed),
		outcome(start, record.OutcomeDropped),
	}

	got := bench.Summarize(results, bench.SummaryOptions{SLO: slo, FailureThreshold: 0.01})

	if got.Requests != 5 {
		t.Errorf("counted %d requests, want 5", got.Requests)
	}
	if got.Successes != 2 {
		t.Errorf("counted %d successes, want 2", got.Successes)
	}
	if got.Failed != 2 {
		t.Errorf("counted %d failed, want 2", got.Failed)
	}
	if got.Dropped != 1 {
		t.Errorf("counted %d dropped, want 1", got.Dropped)
	}
	if got.SLOViolations != 1 {
		t.Errorf("counted %d SLO violations, want 1", got.SLOViolations)
	}
}

func TestAnSLOViolationIsStillASuccessAndNeverAFailure(t *testing.T) {
	start := time.Unix(1757000000, 0)

	got := bench.Summarize([]bench.Result{success(start, 9*time.Second)}, bench.SummaryOptions{SLO: slo, FailureThreshold: 0.01})

	if got.Successes != 1 {
		t.Errorf("counted %d successes, want 1: a slow response still completed", got.Successes)
	}
	if got.Failed != 0 || got.Dropped != 0 {
		t.Errorf("an SLO violation landed in the failure columns: failed=%d dropped=%d", got.Failed, got.Dropped)
	}
	if got.SLOViolations != 1 {
		t.Errorf("counted %d SLO violations, want 1", got.SLOViolations)
	}
}

func TestAnInterTokenLatencyMissViolatesTheSLOOnItsOwn(t *testing.T) {
	start := time.Unix(1757000000, 0)
	slowTokens := success(start, time.Second)
	slowTokens.ITLP50Ns = (400 * time.Millisecond).Nanoseconds()

	got := bench.Summarize([]bench.Result{slowTokens}, bench.SummaryOptions{SLO: slo, FailureThreshold: 0.01})

	if got.SLOViolations != 1 {
		t.Errorf("a fast first token with 400ms between the rest passed the SLO; violations=%d", got.SLOViolations)
	}
}

func TestPercentilesAreComputedOverSuccessfulResponsesOnly(t *testing.T) {
	start := time.Unix(1757000000, 0)
	results := []bench.Result{
		success(start, 1000*time.Millisecond),
		success(start, 2000*time.Millisecond),
		success(start, 3000*time.Millisecond),
	}
	// Nine 2 ms failures. If they reached the distribution they would drag the
	// median from 2 s to single-digit milliseconds.
	for range 9 {
		results = append(results, outcome(start, record.OutcomeFailed))
	}

	got := bench.Summarize(results, bench.SummaryOptions{SLO: slo, FailureThreshold: 1.0})

	if want := (2 * time.Second).Nanoseconds(); got.TTFTP50Ns != want {
		t.Errorf("TTFT p50 is %dns, want %dns: failures reached the distribution", got.TTFTP50Ns, want)
	}
}

func TestACellExceedingTheFailureThresholdIsFlagged(t *testing.T) {
	start := time.Unix(1757000000, 0)
	var results []bench.Result
	for range 98 {
		results = append(results, success(start, time.Second))
	}
	results = append(results, outcome(start, record.OutcomeFailed), outcome(start, record.OutcomeDropped))

	got := bench.Summarize(results, bench.SummaryOptions{SLO: slo, FailureThreshold: 0.01})

	if got.FailureRate != 0.02 {
		t.Errorf("failure rate is %v, want 0.02", got.FailureRate)
	}
	if !got.Flagged {
		t.Error("a cell with 2% failures against a 1% threshold was not flagged")
	}
	if len(got.FlagReasons) == 0 {
		t.Error("the cell was flagged with no reason recorded")
	}
}

func TestACellWithinTheFailureThresholdIsNotFlagged(t *testing.T) {
	start := time.Unix(1757000000, 0)
	var results []bench.Result
	for range 100 {
		results = append(results, success(start, time.Second))
	}

	if got := bench.Summarize(results, bench.SummaryOptions{SLO: slo, FailureThreshold: 0.01}); got.Flagged {
		t.Errorf("a clean cell was flagged: %v", got.FlagReasons)
	}
}

func TestWarmupRequestsAreExcludedFromTheSummary(t *testing.T) {
	start := time.Unix(1757000000, 0)
	warm := success(start, 30*time.Second)
	warm.Warmup = true

	got := bench.Summarize([]bench.Result{warm, success(start, time.Second)}, bench.SummaryOptions{SLO: slo, FailureThreshold: 0.01})

	if got.Requests != 1 {
		t.Errorf("summarised %d requests, want 1: the warm-up row was counted", got.Requests)
	}
	if got.Warmup != 1 {
		t.Errorf("recorded %d warm-up rows, want 1", got.Warmup)
	}
	if got.SLOViolations != 0 {
		t.Errorf("a warm-up request was counted as an SLO violation")
	}
}

func TestGoodputCountsOnlyTheRequestsThatMetTheSLO(t *testing.T) {
	start := time.Unix(1757000000, 0)
	// Four requests spread over a two-second window, one of them missing the
	// SLO on inter-token latency rather than on TTFT, so that the violation
	// does not also stretch the window it is measured over.
	slowTokens := success(start.Add(time.Second), time.Second)
	slowTokens.ITLP50Ns = (400 * time.Millisecond).Nanoseconds()
	results := []bench.Result{
		success(start, time.Second),
		success(start.Add(500*time.Millisecond), time.Second),
		slowTokens,
		success(start.Add(2*time.Second), time.Second),
	}

	got := bench.Summarize(results, bench.SummaryOptions{SLO: slo, FailureThreshold: 0.01})

	// The window runs from the first start to the last completion: 2s + 1s
	// TTFT + 1s of tokens = 4s. Three of the four met the SLO.
	if want := 4 * time.Second; got.WindowNs != want.Nanoseconds() {
		t.Fatalf("measured window is %dns, want %dns", got.WindowNs, want.Nanoseconds())
	}
	if got.GoodputRPS != 0.75 {
		t.Errorf("goodput is %v rps, want 0.75", got.GoodputRPS)
	}
	if got.ThroughputRPS != 1.0 {
		t.Errorf("throughput is %v rps, want 1.0", got.ThroughputRPS)
	}
}

func TestWithoutAnSLOTheCellRecordsThatNoneWasApplied(t *testing.T) {
	start := time.Unix(1757000000, 0)

	// The concurrency-1 cell is what the SLO is derived from, so it necessarily
	// runs before one exists. A zero in the violations column would read as
	// "nothing violated" rather than "nothing was checked".
	got := bench.Summarize([]bench.Result{success(start, 30*time.Second)}, bench.SummaryOptions{SLO: bench.SLO{}, FailureThreshold: 0.01})

	if got.SLOApplied {
		t.Error("a cell with no thresholds reported an SLO as applied")
	}
	if got.GoodputRPS != 0 {
		t.Errorf("goodput is %v with no SLO to measure it against, want 0", got.GoodputRPS)
	}
	if got.Successes != 1 {
		t.Errorf("counted %d successes, want 1", got.Successes)
	}
}

func TestACancelledRequestIsNeitherASuccessNorAFailure(t *testing.T) {
	start := time.Unix(1757000000, 0)
	results := []bench.Result{
		success(start, time.Second),
		outcome(start, record.OutcomeCancelled),
	}

	got := bench.Summarize(results, bench.SummaryOptions{SLO: slo, FailureThreshold: 0.01})

	if got.Cancelled != 1 {
		t.Errorf("counted %d cancelled, want 1", got.Cancelled)
	}
	if got.Failed != 0 || got.Dropped != 0 || got.Successes != 1 {
		t.Errorf("a cancelled request leaked into another column: %+v", got)
	}
	// A cell that was interrupted is not comparable with one that ran to the
	// end, so it is flagged rather than quietly averaged in.
	if !got.Flagged {
		t.Error("a cell containing a cancelled request was not flagged")
	}
}

func TestACellStillSpeedingUpIsFlaggedAsUnderWarmed(t *testing.T) {
	start := time.Unix(1757000000, 0)
	// Twelve successes across a 12-second window. The first half answers in 4s,
	// the second in 1s — the fleet was still warming when measurement began, so
	// this cell's numbers are a transient, not its steady state.
	var results []bench.Result
	for i := range 12 {
		ttft := time.Second
		if i < 6 {
			ttft = 4 * time.Second
		}
		r := success(start.Add(time.Duration(i)*time.Second), ttft)
		r.TotalNs = ttft.Nanoseconds()
		results = append(results, r)
	}

	got := bench.Summarize(results, bench.SummaryOptions{FailureThreshold: 0.01})

	if got.WarmupDrift < 2.5 {
		t.Errorf("drift is %.2f, want ~3.0 (4s against 1s)", got.WarmupDrift)
	}
	if !got.Flagged {
		t.Fatal("a cell whose first half was 4x slower than its second was not flagged")
	}
	if !strings.Contains(strings.Join(got.FlagReasons, " "), "still warming up") {
		t.Errorf("the flag does not say the cell was under-warmed: %v", got.FlagReasons)
	}
}

func TestASteadyCellIsNotFlaggedAsUnderWarmed(t *testing.T) {
	start := time.Unix(1757000000, 0)
	var results []bench.Result
	for i := range 12 {
		r := success(start.Add(time.Duration(i)*time.Second), time.Second)
		r.TotalNs = time.Second.Nanoseconds()
		results = append(results, r)
	}

	got := bench.Summarize(results, bench.SummaryOptions{FailureThreshold: 0.01})

	if got.Flagged {
		t.Errorf("a steady cell was flagged: %v", got.FlagReasons)
	}
}

func TestDriftIsNotJudgedOnTooFewRequestsToCompare(t *testing.T) {
	start := time.Unix(1757000000, 0)
	// Four requests, wildly uneven. At concurrency 1 a short cell produces
	// exactly this, and two medians over two requests each is noise, not
	// evidence that anything was cold.
	results := []bench.Result{
		success(start, 9*time.Second),
		success(start.Add(time.Second), 9*time.Second),
		success(start.Add(2*time.Second), time.Second),
		success(start.Add(3*time.Second), time.Second),
	}

	got := bench.Summarize(results, bench.SummaryOptions{FailureThreshold: 0.01})

	if got.WarmupDrift != 0 {
		t.Errorf("drift is %.2f over 4 requests, want 0: too few to compare", got.WarmupDrift)
	}
}
