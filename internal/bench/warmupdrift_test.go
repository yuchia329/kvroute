package bench_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/record"
)

// The warm-up drift check, rebuilt for #33.
//
// The old check split a cell's window in half and compared TTFT p50 either
// side. Three things about that were unsound and every one of them is a test
// here: it fired only when the FIRST half was slower, so a cell that got two
// and a half times slower across its window was published carrying no flag; it
// split first-start-to-last-RESPONSE, so its midpoint sat half the cell's drain
// past the arrival midpoint, which on this fleet's think-75s cells was a minute;
// and it compared two populations that the workload's own period had made
// different, so it fired on cells no warm-up of any length could settle.

// arrival builds a scheduled success: the open-loop driver's row, carrying the
// due time the check now splits on and the turn index it now stratifies by.
func arrival(due time.Time, turn int, ttft, drain time.Duration) bench.Result {
	// Started a hair after due — the driver is punctual here, because the
	// schedule lag check is a different flag and mixing the two would make a
	// failure of either read as a failure of both.
	started := due.Add(time.Millisecond)
	return bench.Result{
		Labels:        bench.Labels{Driver: bench.OpenLoopDriver, ArrivalRate: 1},
		Turn:          turn,
		Outcome:       record.OutcomeSuccess,
		ScheduledAtNs: due.UnixNano(),
		StartedAtNs:   started.UnixNano(),
		TTFTNs:        ttft.Nanoseconds(),
		ITLP50Ns:      (20 * time.Millisecond).Nanoseconds(),
		TotalNs:       (ttft + drain).Nanoseconds(),
	}
}

// driftOptions judges drift and nothing else, for a workload of the given turns
// per session. The fixtures below are built to exercise one check, and a cell of
// synthetic rows trips the schedule-lag and failure checks for reasons that have
// nothing to do with what is being tested.
func driftOptions(turnsPerSession int) bench.SummaryOptions {
	return bench.SummaryOptions{FailureThreshold: -1, ScheduleLagThreshold: -1, TurnsPerSession: turnsPerSession}
}

func reasons(s bench.Summary) string { return strings.Join(s.FlagReasons, " | ") }

// #29's think75 cells recorded drift of -0.571, -0.690 and -0.562 — a second
// half more than twice as slow as the first — and were published carrying no
// flag at all, because the old check tested `drift > threshold` rather than the
// size of the move. A fleet that degraded across the window is as unpoolable as
// one that was still warming up, and it is a different fault with a different
// fix, so it is flagged and named rather than passed over.
func TestACellThatGotSlowerAcrossItsWindowIsFlagged(t *testing.T) {
	start := time.Unix(1757000000, 0)
	var rows []bench.Result
	for i := range 40 {
		ttft := time.Second
		if i >= 20 {
			ttft = 3 * time.Second
		}
		rows = append(rows, arrival(start.Add(time.Duration(i)*time.Second), i%4, ttft, time.Second))
	}

	got := bench.Summarize(rows, driftOptions(4))

	if got.WarmupDrift > -0.6 {
		t.Errorf("drift is %.2f, want about -0.67 (1s early against 3s late)", got.WarmupDrift)
	}
	if !got.Flagged {
		t.Fatal("a cell whose second half was three times slower than its first was not flagged")
	}
	if !strings.Contains(reasons(got), "slowed") {
		t.Errorf("the flag does not say the fleet slowed across the window: %v", got.FlagReasons)
	}
	if strings.Contains(reasons(got), "Lengthen the warm-up") {
		t.Errorf("the flag prescribes a longer warm-up for a fleet that degraded: %v", got.FlagReasons)
	}
}

// The composition effect, which is what flagged all six of #17's recency cells
// and what #29 spent a design pass deriving a cell geometry to cancel by hand.
//
// Under the open-loop rotation the turn index is the round, so the whole
// conversation pool advances together and TTFT is periodic with the visit
// period. A window holding a fractional number of periods draws the turn
// indices unevenly across its split, and the two medians are then over two
// different workloads however long the fleet has been up. Comparing each index
// with itself cancels that for any cell length, any warm-up and any rate, with
// no geometry arithmetic.
func TestDriftIsComparedWithinEachTurnIndex(t *testing.T) {
	start := time.Unix(1757000000, 0)
	// Two turn indices, each perfectly steady: index 0 always answers in 1s and
	// index 1 always in 4s. Only the mix moves — the early half is mostly the
	// cheap index and the late half mostly the expensive one, which is what an
	// odd number of visit periods does to a window.
	var rows []bench.Result
	at := func(i int) time.Time { return start.Add(time.Duration(i) * time.Second) }
	for i := range 40 {
		turn, ttft := 0, time.Second
		early := i < 20
		if (early && i%4 == 3) || (!early && i%4 != 3) {
			turn, ttft = 1, 4*time.Second
		}
		rows = append(rows, arrival(at(i), turn, ttft, time.Second))
	}

	got := bench.Summarize(rows, driftOptions(2))

	if got.WarmupDriftBasis != "turn" {
		t.Fatalf("drift basis is %q, want %q: both turn indices span the window", got.WarmupDriftBasis, "turn")
	}
	if got.WarmupDrift != 0 {
		t.Errorf("drift is %.2f, want 0: neither turn index moved, only the mix of them", got.WarmupDrift)
	}
	if got.Flagged {
		t.Errorf("a cell whose every turn index was steady was flagged: %v", got.FlagReasons)
	}
}

// The window the split is taken over.
//
// `last` was the maximum EndedAt over the measured rows — the final RESPONSE,
// not the final arrival — so the midpoint sat half the cell's drain past the
// arrival midpoint. Measured on #29's six cells the drain was 2.8-11.8 s at
// think 30s and 57.4-71.2 s at think 75s, which put the real split about 32 s
// past where the geometry had placed it. Any reasoning about what the two halves
// contain is off by that much, and at think 75s that is a minute of the wrong
// traffic.
func TestTheSplitIsTakenOverTheArrivalWindow(t *testing.T) {
	start := time.Unix(1757000000, 0)
	// Twenty arrivals, one a second. The first sixteen answer in 1s and the last
	// four in 5s, so the arrival midpoint at t=10s puts a steady 1s half against
	// a half that is 1s for six arrivals and 5s for four: slower late, but only
	// mildly.
	//
	// Every response then drains for 10s, which drags the last-response
	// midpoint to about t=15s and moves four of the cheap late arrivals into the
	// early half. Split there, the early half is 1s throughout and the late half
	// is dominated by the 5s tail.
	var rows []bench.Result
	for i := range 20 {
		ttft := time.Second
		if i >= 16 {
			ttft = 5 * time.Second
		}
		rows = append(rows, arrival(start.Add(time.Duration(i)*time.Second), 0, ttft, 10*time.Second))
	}

	got := bench.Summarize(rows, driftOptions(0))

	// Split on arrivals the two medians are both 1s: ten cheap arrivals early,
	// and six cheap against four expensive late, whose median is still 1s.
	if got.WarmupDrift != 0 {
		t.Errorf("drift is %.2f, want 0: split on the arrival window both halves have a 1s TTFT p50, and only the drain puts the tail on one side",
			got.WarmupDrift)
	}
}

// #17's think75 cells, where no warm-up of any length could have cleared the
// flag: one visit period is 300 s and the whole cell was 420 s, so the window
// could not hold even one. Its halves shared no turn index — index 0 was 600
// requests against nil across the split, index 2 nil against 600 — and the check
// was comparing two different workloads rather than two stretches of one.
//
// The remedy is a cell geometry of whole visit periods, which is not what
// "lengthen the warm-up and re-run" asks for, and that sentence is what shaped
// #29's acceptance criteria.
func TestHalvesHoldingDifferentTurnIndexesAreFlaggedAsAFractionalWindow(t *testing.T) {
	start := time.Unix(1757000000, 0)
	// A window of a period and a bit: index 0 opens it and comes round again at
	// the end, so it spans the window and the index is a stratifier; indices 1
	// and 2 each fall wholly inside one half.
	var rows []bench.Result
	at := func(i int) time.Time { return start.Add(time.Duration(i) * time.Second) }
	for i := range 12 {
		rows = append(rows, arrival(at(i), 0, time.Second, time.Second))
	}
	for i := range 12 {
		rows = append(rows, arrival(at(12+i), 1, time.Second, time.Second))
	}
	for i := range 12 {
		rows = append(rows, arrival(at(24+i), 2, time.Second, time.Second))
	}
	for i := range 12 {
		rows = append(rows, arrival(at(36+i), 0, time.Second, time.Second))
	}

	got := bench.Summarize(rows, driftOptions(3))

	if got.WarmupDriftTurnsConfined == 0 {
		t.Fatalf("no turn index was recorded as confined to one half of the split: %+v", got)
	}
	if !got.Flagged {
		t.Fatal("a cell whose halves hold different turn indexes was not flagged")
	}
	if !strings.Contains(reasons(got), "visit period") {
		t.Errorf("the flag does not name the fractional window as the cause: %v", got.FlagReasons)
	}
	if strings.Contains(reasons(got), "Lengthen the warm-up") {
		t.Errorf("the flag prescribes the one fix that cannot work here: %v", got.FlagReasons)
	}
}

// The cause the check was built for, which still has to be named as itself: a
// cell whose opening period was genuinely cold. Every turn index is slower early
// than late, so the composition cancels and what is left is the fleet warming
// up.
func TestAColdOpeningPeriodIsNamedAsAWarmUpFix(t *testing.T) {
	start := time.Unix(1757000000, 0)
	var rows []bench.Result
	for i := range 40 {
		ttft := time.Second
		if i < 20 {
			ttft = 4 * time.Second
		}
		rows = append(rows, arrival(start.Add(time.Duration(i)*time.Second), i%4, ttft, time.Second))
	}

	got := bench.Summarize(rows, driftOptions(4))

	if got.WarmupDrift < 2.5 {
		t.Errorf("drift is %.2f, want about 3.0 (4s early against 1s late)", got.WarmupDrift)
	}
	if !strings.Contains(reasons(got), "still warming up") {
		t.Errorf("the flag does not say the cell was under-warmed: %v", got.FlagReasons)
	}
	if !strings.Contains(reasons(got), "Lengthen the warm-up") {
		t.Errorf("the flag does not prescribe the fix that does work here: %v", got.FlagReasons)
	}
}

// The turn index is only a stratifier where the workload returns to it, and the
// workload is what says so.
//
// Under the multi-turn generator the index is the turn's position in its
// session: the pool rolls over every TurnsPerSession rounds and every index is
// re-offered across the whole cell. Under the fixed workload the index is the
// round counter and never comes back, so stratifying by it would compare each
// round against nothing. There the check pools, which is the right comparison
// for a workload whose turns are all the same size.
//
// The period is stated rather than inferred from the rows, because inside one
// cell the two look alike: a closed-loop fixed cell at concurrency 256 has its
// virtual users spread over half a dozen turn numbers whose spans overlap
// exactly as a four-turn workload's indices do. A rule that guessed from the
// rows read fourteen of those cells as four-turn workloads missing three of
// their visits.
func TestTheTurnIndexIsNotAStratifierWhenTheWorkloadNeverReturnsToIt(t *testing.T) {
	start := time.Unix(1757000000, 0)
	// The fixed workload's index: a round counter, five arrivals to the round,
	// climbing through the cell and never repeating.
	var rows []bench.Result
	for i := range 60 {
		ttft := time.Second
		if i < 30 {
			ttft = 4 * time.Second
		}
		rows = append(rows, arrival(start.Add(time.Duration(i)*time.Second), i/5, ttft, time.Second))
	}

	got := bench.Summarize(rows, driftOptions(0))

	if got.WarmupDriftBasis != "pooled" {
		t.Fatalf("drift basis is %q, want %q: a round counter stratifies nothing", got.WarmupDriftBasis, "pooled")
	}
	if got.WarmupDrift < 2.5 {
		t.Errorf("drift is %.2f, want about 3.0: the pooled comparison is the one available here", got.WarmupDrift)
	}
	if !strings.Contains(reasons(got), "still warming up") {
		t.Errorf("a cold fixed-workload cell was not flagged as under-warmed: %v", got.FlagReasons)
	}
}

// Every flag the check can write has to be readable back as the cause that
// wrote it.
//
// The 216 cells already measured predate any field that could have held the
// cause, so the sentence a flag opens with is the only record of it. An opening
// that drifted from the cause it names would silently re-score every one of them
// as unflagged and nothing else would fail, which is why the two are one table
// and why this walks the round trip for each cause in turn.
func TestEveryDriftFlagReadsBackAsTheCauseThatWroteIt(t *testing.T) {
	start := time.Unix(1757000000, 0)
	at := func(i int) time.Time { return start.Add(time.Duration(i) * time.Second) }

	for _, tc := range []struct {
		name  string
		turns int
		rows  []bench.Result
		want  bench.WarmupDriftCause
	}{
		{
			name: "cold opening", turns: 4, want: bench.WarmupDriftCold,
			rows: func() (rows []bench.Result) {
				for i := range 40 {
					ttft := time.Second
					if i < 20 {
						ttft = 4 * time.Second
					}
					rows = append(rows, arrival(at(i), i%4, ttft, time.Second))
				}
				return rows
			}(),
		},
		{
			name: "fleet degrading", turns: 4, want: bench.WarmupDriftDegrading,
			rows: func() (rows []bench.Result) {
				for i := range 40 {
					ttft := time.Second
					if i >= 20 {
						ttft = 4 * time.Second
					}
					rows = append(rows, arrival(at(i), i%4, ttft, time.Second))
				}
				return rows
			}(),
		},
		{
			name: "fractional visit periods", turns: 3, want: bench.WarmupDriftFractional,
			rows: func() (rows []bench.Result) {
				for _, turn := range []int{0, 1, 2, 0} {
					for range 12 {
						rows = append(rows, arrival(at(len(rows)), turn, time.Second, time.Second))
					}
				}
				return rows
			}(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := bench.Summarize(tc.rows, driftOptions(tc.turns))

			if !got.WarmupDriftFlagged() {
				t.Fatalf("the cell was not flagged at all: %v", got.FlagReasons)
			}
			if !slices.Contains(got.WarmupDriftCauses(), tc.want) {
				t.Errorf("the flag reads back as %v, want %q. Its text was: %v",
					got.WarmupDriftCauses(), tc.want, got.FlagReasons)
			}
			if !strings.Contains(got.WarmupDriftVerdict(), string(tc.want)) {
				t.Errorf("verdict %q does not name %q", got.WarmupDriftVerdict(), tc.want)
			}
		})
	}
}
