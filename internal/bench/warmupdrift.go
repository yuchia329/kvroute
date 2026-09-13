package bench

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/stats"
)

// Warm-up drift: whether a cell's latencies moved across the window it was
// measured over, and if so which of three things moved them.
//
// The check exists because a warm-up length cannot be chosen in advance and
// then trusted. What it must not do is fire on a cell that was perfectly steady,
// because a flag that fires on healthy cells stops being read — and the first
// version of it did, on all six of #17's recency cells, for a reason no warm-up
// could have fixed.
//
// The three things it separates:
//
//   - A still-cold opening period. The fleet was genuinely faster later than
//     earlier; the warm-up was too short. This is the one "lengthen the warm-up
//     and re-run" is the fix for.
//   - A fleet genuinely degrading. The second half is the slower one. A cell
//     whose TTFT p50 doubled across its window is as unpoolable as one that
//     halved, and a check that tests only one direction publishes it unflagged.
//   - A fractional number of visit periods. Under the open-loop rotation the
//     turn index is the round, so the whole conversation pool advances together
//     and rolls over every TurnsPerSession rounds. TTFT is therefore periodic
//     with period TurnsPerSession x think time, and a window holding a
//     fractional number of those periods draws the turn indices unevenly across
//     its split. The two medians are then over two different workloads, for as
//     long as the cell runs and however warm the fleet is.
//
// The third is cancelled rather than detected-and-excused: drift is compared
// within each turn index, so the composition of the halves falls out of the
// arithmetic for any cell length, any warm-up and any rate, with no
// per-configuration geometry to derive. What remains detectable — and is
// reported, because it makes the cell's own percentiles a mix nothing else
// shares — is a split that left one of the workload's turn indices off one side
// of it, or out of the window altogether.

// minHalfForDrift is how many successes each side of the split needs before
// their medians are worth comparing. Below this the comparison is noise.
const minHalfForDrift = 5

// driftMinPerTurn is how many successes a cell needs per turn index before it is
// judged one index at a time.
//
// Each index needs minHalfForDrift successes on each side of the split, so a
// cell with fewer than that many per index has nothing to compare whatever its
// geometry, and stratifying it would report every index as missing rather than
// the cell as short. Below this the check pools, which is the comparison a short
// cell can support.
const driftMinPerTurn = 2 * minHalfForDrift

// driftBasis names the population a drift figure was computed over, so a
// recorded cell says what its number means rather than leaving it to be assumed.
type driftBasis string

const (
	// driftNotMeasured is a cell with too few successes to compare.
	driftNotMeasured driftBasis = ""
	// driftPooled compared every success as one population. It is what a
	// workload that never returns to a turn index leaves available.
	driftPooled driftBasis = "pooled"
	// driftPerTurn compared each turn index against itself and combined the
	// results weighted by how many requests each carried.
	driftPerTurn driftBasis = "turn"
)

// driftReading is what the check found: the size and direction of the move, what
// it was measured over, and how the split divided the workload.
type driftReading struct {
	// drift keeps its sign, and its sign keeps its old meaning: positive is an
	// early half slower than the late one. Two hundred and sixteen recorded
	// cells carry the number under that convention and re-scoring them has to
	// compare like with like.
	drift float64
	basis driftBasis
	// compared is how many populations the figure is an average over: the turn
	// indices that appeared on both sides of the split, or one for a pooled
	// reading.
	compared int
	// confined is how many of the workload's turn indices did not appear on both
	// sides of the split with enough successes to have a median: absent from the
	// window altogether, offered only early, or only late. Any at all means the
	// measured window is not a whole number of visit periods, and the cell's
	// percentiles are over a mix a cell of another length does not share.
	confined int
}

// arrivalAt is when the schedule asked for this request, falling back to when it
// was actually sent for a closed-loop row that nothing scheduled.
//
// The distinction is the whole of the second defect. A cell's rows are split by
// where they sit in the window, and a request's place in the window is when it
// arrived, not when its response finished: at and past saturation the two differ
// by the drain, which on this fleet's think-75s cells ran to 71 seconds.
func arrivalAt(r Result) time.Time {
	if r.ScheduledAtNs != 0 {
		return time.Unix(0, r.ScheduledAtNs)
	}
	return time.Unix(0, r.StartedAtNs)
}

// driftRow is one measured success reduced to what the check needs. The rows
// are the system of record; this is the three columns of one that the check
// reads.
type driftRow struct {
	turn    int
	arrived time.Time
	ttft    time.Duration
}

// measureDrift compares TTFT p50 early in the measured window against late,
// within each turn index where the workload returns to one.
//
// TTFT rather than total latency because it is what queueing and cold caches
// move first, and it is not diluted by however many tokens each response
// happened to generate.
//
// turnsPerSession is how many turns a session sends before the workload draws a
// fresh one, and so the period the turn index cycles with. Zero from a workload
// that never returns to a turn index, where the index is a counter rather than a
// position and the check pools instead.
func measureDrift(results []Result, w cellWindow, turnsPerSession int) driftReading {
	opens, closes := w.arrivals()
	if !closes.After(opens) {
		return driftReading{basis: driftNotMeasured}
	}
	split := opens.Add(closes.Sub(opens) / 2)

	rows := make([]driftRow, 0, len(results))
	for _, r := range results {
		if r.Warmup || r.Outcome != record.OutcomeSuccess {
			continue
		}
		rows = append(rows, driftRow{turn: r.Turn, arrived: arrivalAt(r), ttft: time.Duration(r.TTFTNs)})
	}
	if turnsPerSession > 1 && len(rows) >= turnsPerSession*driftMinPerTurn {
		return perTurnDrift(rows, split, turnsPerSession)
	}
	if drift, ok := halfDrift(rows, split); ok {
		return driftReading{drift: drift, basis: driftPooled, compared: 1}
	}
	return driftReading{basis: driftNotMeasured}
}

// perTurnDrift compares each turn index with itself and combines the results,
// weighted by how many requests each index contributed.
//
// Request-weighted rather than the largest of them: the maximum would hand the
// cell's verdict to whichever index happened to carry the fewest requests, and
// the thin strata are exactly the noisy ones.
func perTurnDrift(rows []driftRow, split time.Time, turnsPerSession int) driftReading {
	byTurn := map[int][]driftRow{}
	for _, r := range rows {
		byTurn[r.turn] = append(byTurn[r.turn], r)
	}

	reading := driftReading{basis: driftPerTurn}
	var weighted float64
	var weight int
	// Over the workload's indices rather than the ones that turned up, so an
	// index the window never offered at all counts as missing from the split
	// instead of vanishing from the arithmetic. A window that holds three
	// quarters of a visit is as uneven as one that holds one index twice.
	for turn := range turnsPerSession {
		of := byTurn[turn]
		drift, ok := halfDrift(of, split)
		if !ok {
			reading.confined++
			continue
		}
		reading.compared++
		weighted += drift * float64(len(of))
		weight += len(of)
	}
	if weight > 0 {
		reading.drift = weighted / float64(weight)
	}
	return reading
}

// halfDrift is the one comparison the whole check is built out of: the TTFT p50
// of the rows that arrived before the split against those that arrived after.
//
// It reports false rather than zero when either side is too thin to have a
// median worth taking, so a caller can tell "these two are the same" from "there
// was nothing to compare".
func halfDrift(rows []driftRow, split time.Time) (float64, bool) {
	var early, late []time.Duration
	for _, r := range rows {
		if r.arrived.Before(split) {
			early = append(early, r.ttft)
		} else {
			late = append(late, r.ttft)
		}
	}
	if len(early) < minHalfForDrift || len(late) < minHalfForDrift {
		return 0, false
	}
	slices.Sort(early)
	slices.Sort(late)
	lateP50 := stats.Quantile(late, 0.50)
	if lateP50 <= 0 {
		return 0, false
	}
	return float64(stats.Quantile(early, 0.50)-lateP50) / float64(lateP50), true
}

// WarmupDriftCause names why the drift check flagged a cell, or the empty string
// when it did not. The three have three different fixes, which is the whole
// reason the check separates them.
type WarmupDriftCause string

const (
	// WarmupDriftCold is a still-cold opening period: the early part of the
	// window was slower. The one cause a longer warm-up fixes.
	WarmupDriftCold WarmupDriftCause = "cold opening"
	// WarmupDriftDegrading is a fleet that slowed as the cell ran. Invisible to
	// the one-sided check this replaced.
	WarmupDriftDegrading WarmupDriftCause = "fleet degrading"
	// WarmupDriftFractional is a measured window holding a fractional number of
	// visit periods, so the halves held different turn indices.
	WarmupDriftFractional WarmupDriftCause = "fractional visit periods"
)

// warmupDriftOpenings is the sentence each cause's flag opens with.
//
// One table, read by the flag that writes the message and by the reader that
// recovers the cause from a recorded one. The 216 cells already measured predate
// any field that could have held the cause, so the text is the only record of
// it; an opening that drifted from the cause it names would silently re-score
// every one of them as unflagged, and nothing would fail.
var warmupDriftOpenings = map[WarmupDriftCause]string{
	WarmupDriftCold:       "still warming up",
	WarmupDriftDegrading:  "the fleet slowed across the measured window",
	WarmupDriftFractional: "the measured window is not a whole number of visit periods",
}

// warmupDriftCauseOrder is the order causes are read and reported in, so a cell
// flagged for two of them renders the same way every time.
var warmupDriftCauseOrder = []WarmupDriftCause{WarmupDriftFractional, WarmupDriftCold, WarmupDriftDegrading}

// WarmupDriftCauses is every drift cause this summary was flagged for.
//
// More than one is possible: a cell can have both an uneven split and a real
// move inside the turn indices that did appear on both sides of it. Matched on
// the sentence a flag opens with rather than on a substring anywhere in it, so a
// message that merely mentions warming up cannot be read as one that flagged
// for it.
func (s Summary) WarmupDriftCauses() []WarmupDriftCause {
	var causes []WarmupDriftCause
	for _, reason := range s.FlagReasons {
		for _, cause := range warmupDriftCauseOrder {
			if strings.HasPrefix(reason, warmupDriftOpenings[cause]) {
				causes = append(causes, cause)
			}
		}
	}
	return causes
}

// WarmupDriftFlagged reports whether the drift check is what flagged this cell.
func (s Summary) WarmupDriftFlagged() bool { return len(s.WarmupDriftCauses()) > 0 }

// WarmupDriftVerdict renders the drift check's verdict for a table cell.
func (s Summary) WarmupDriftVerdict() string {
	causes := s.WarmupDriftCauses()
	if len(causes) == 0 {
		return "clear"
	}
	parts := make([]string, len(causes))
	for i, cause := range causes {
		parts[i] = string(cause)
	}
	return strings.Join(parts, " + ")
}

// flagDrift records what the drift check found, naming the cause rather than
// prescribing one remedy for all of them.
//
// The old message ended "Lengthen the warm-up and re-run", which is the one fix
// that cannot work for a window holding a fractional number of visit periods —
// and that sentence is what shaped #29's acceptance criteria, which #29 then
// spent its first design pass undoing. The three causes have three different
// fixes and the flag says which it is looking at.
func (s *Summary) flagDrift(threshold float64) {
	if threshold <= 0 || s.WarmupDriftBasis == "" {
		return
	}

	if s.WarmupDriftTurnsConfined > 0 {
		// Reported whatever the drift came to, because it is not a statement
		// about the drift. Under the open-loop rotation the turn index is the
		// round, so a window that does not hold a whole number of visit periods
		// offers some turn indices more often than others — and every percentile
		// in the summary, not only the drift, is then over a mix that a cell of
		// a different length does not share.
		rest := fmt.Sprintf("the other %d %s compared like for like", s.WarmupDriftTurnsCompared,
			plural(s.WarmupDriftTurnsCompared, "was", "were"))
		if s.WarmupDriftTurnsCompared == 0 {
			rest = "no turn index appears on both sides of the split at all, so there was nothing like for like to compare"
		}
		s.flagDriftCause(WarmupDriftFractional,
			"%d turn %s not on both sides of the split with enough requests to compare, so the two halves are different workloads — %s. Re-run the cell over a whole, even number of visit periods; a longer warm-up cannot fix this",
			s.WarmupDriftTurnsConfined, plural(s.WarmupDriftTurnsConfined, "index is", "indices are"), rest)
	}

	if s.WarmupDrift > threshold {
		s.flagDriftCause(WarmupDriftCold,
			"TTFT p50 was %.0f%% slower early in the measured window than late, %s, over a %.0f%% threshold. Lengthen the warm-up and re-run",
			s.WarmupDrift*100, s.driftBasisPhrase(), threshold*100)
	} else if -s.WarmupDrift > threshold {
		// The direction the old check could not see. A cell that got slower as
		// it ran was not under-warmed — it was degrading, and re-running it with
		// a longer warm-up would measure the same decline from a worse start.
		s.flagDriftCause(WarmupDriftDegrading,
			"TTFT p50 was %.0f%% slower late than early, %s, over a %.0f%% threshold. A longer warm-up is not the fix: this cell's latency percentiles are a transient rather than a steady state. Look for a queue that never settled — an open-loop cell offered more than the fleet can serve never reaches one — or a throttled card, a replica lost, or a cache growing",
			-s.WarmupDrift*100, s.driftBasisPhrase(), threshold*100)
	}
}

// flagDriftCause writes one drift flag: the cause's own opening sentence, then
// what this cell showed. Going through here rather than formatting the whole
// string at each call site is what keeps the opening and the cause the same
// fact, so WarmupDriftCauses can read one back off the other.
func (s *Summary) flagDriftCause(cause WarmupDriftCause, format string, args ...any) {
	s.Flag(warmupDriftOpenings[cause] + ": " + fmt.Sprintf(format, args...))
}

// driftBasisPhrase says which comparison produced the number, inside the flag
// that reports it. A reader deciding what to do about a flagged cell needs to
// know whether the composition of the halves was held fixed or pooled over.
//
// It says what was compared and not why, because a cell pools for two different
// reasons — a workload that never returns to a turn index, and one that does but
// sent too few requests to judge each index separately — and a flag that
// asserted the first would be stating something false about the second.
func (s Summary) driftBasisPhrase() string {
	if driftBasis(s.WarmupDriftBasis) == driftPerTurn {
		return fmt.Sprintf("compared within each of %d turn %s", s.WarmupDriftTurnsCompared,
			plural(s.WarmupDriftTurnsCompared, "index", "indices"))
	}
	return "compared over every success rather than within each turn index"
}

// plural picks between two forms. Flag text is read by people deciding whether
// to re-run a cell, and "1 turn indices are" reads as a bug in the check.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
