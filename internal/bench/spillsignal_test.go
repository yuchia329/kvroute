package bench_test

import (
	"math"
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/record"
)

// rowsWith builds router rows carrying an inflight count and a residency
// reading each, which is the pair the separability claim is made from.
func rowsWith(pairs [][2]float64) []record.Request {
	rows := make([]record.Request, len(pairs))
	for i, p := range pairs {
		rows[i] = record.Request{
			Inflight:    int(p[0]),
			HitRate:     p[1],
			HitRateRead: true,
		}
	}
	return rows
}

// residency is the accessor for the signal the spill rule actually reads.
func hitRateOf(r record.Request) (float64, bool) { return r.HitRate, r.HitRateRead }

// The measurement #28 turns on. A signal that tracks inflight the way the old
// gauge did comes back near 1, and the report is what says so.
func TestASignalThatTracksInflightIsReportedAsTracking(t *testing.T) {
	rows := rowsWith([][2]float64{{0, 0.02}, {4, 0.11}, {8, 0.18}, {16, 0.36}, {24, 0.53}, {27, 0.60}})

	got := bench.CorrelateWithInflight(rows, hitRateOf)
	if !got.Defined {
		t.Fatal("a varying signal reported no correlation")
	}
	if got.R < 0.99 {
		t.Errorf("r = %v, want a near-perfect correlation on a near-perfect line", got.R)
	}
	if got.Pairs != len(rows) {
		t.Errorf("pairs = %d, want %d", got.Pairs, len(rows))
	}
}

// And a signal that does not move with load comes back near zero, which is the
// answer the residency branch has to produce for the pressure grid's
// separability criterion to be evaluable at all.
func TestASignalIndependentOfLoadIsReportedAsIndependent(t *testing.T) {
	rows := rowsWith([][2]float64{{0, 0.9}, {4, 0.2}, {8, 0.9}, {16, 0.2}, {24, 0.9}, {27, 0.2}})

	got := bench.CorrelateWithInflight(rows, hitRateOf)
	if !got.Defined {
		t.Fatal("a varying signal reported no correlation")
	}
	if math.Abs(got.R) > 0.5 {
		t.Errorf("r = %v, want something well away from 1", got.R)
	}
}

// A signal that never moved has no correlation with anything, and reporting
// that as zero would read as perfect independence — which is exactly the
// conclusion this report exists to be sceptical of.
func TestAConstantSignalHasNoCorrelationRatherThanZero(t *testing.T) {
	rows := rowsWith([][2]float64{{0, 0.5}, {8, 0.5}, {24, 0.5}})

	got := bench.CorrelateWithInflight(rows, hitRateOf)
	if got.Defined {
		t.Errorf("a constant signal reported %v", got)
	}
	if !strings.Contains(got.String(), "undefined") {
		t.Errorf("a constant signal prints %q", got)
	}
}

// An unread row is not evidence of anything. Pairing it against a zero would
// invent a reading at the bottom of the signal's range, which is where the
// grid's levels are cut from.
func TestUnreadRowsAreDroppedRatherThanPairedAgainstZero(t *testing.T) {
	rows := append(rowsWith([][2]float64{{0, 0.9}, {8, 0.8}}),
		record.Request{Inflight: 30, HitRate: 0, HitRateRead: false})

	r := bench.RangeOf(rows, hitRateOf)
	if r.Readings != 2 || r.Unread != 1 {
		t.Errorf("range covers %d readings and %d unread, want 2 and 1", r.Readings, r.Unread)
	}
	if r.Min != 0.8 {
		t.Errorf("min = %v, want 0.8: the unread row has no reading to be the minimum", r.Min)
	}
}

// #28's acceptance criterion, as the sweep asks it: a level the signal never
// reached is a cell that cannot produce a decision, which is what two of #16's
// three levels turned out to be.
func TestALevelTheSignalNeverReachedIsNotReachable(t *testing.T) {
	rows := rowsWith([][2]float64{{0, 0.40}, {8, 0.62}, {24, 0.91}})
	r := bench.RangeOf(rows, hitRateOf)

	if r.Reaches(0.40) {
		t.Error("a level exactly at the lowest observed rate reads as reachable: nothing is strictly below it")
	}
	if r.Reaches(0.20) {
		t.Error("a level below everything observed reads as reachable")
	}
	if !r.Reaches(0.50) {
		t.Error("a level with observations below it reads as unreachable")
	}
}

// A run whose rows carry no residency reading routed with the branch disabled
// however it was configured, and the report has to say that rather than print
// a table of zeros.
func TestARunWithNoResidencyReadingsSaysSoRatherThanReportingZeros(t *testing.T) {
	rows := []record.Request{{Inflight: 4}, {Inflight: 9}}

	report := bench.MeasureSpillSignals(rows).Report()
	if !strings.Contains(report, "No row carried a residency reading") {
		t.Errorf("the report does not say the signal was absent:\n%s", report)
	}
}

// The report states the comparison rather than leaving it to the reader: the
// claim is about two numbers and only one of them is new.
func TestTheReportPutsBothSignalsCorrelationsSideBySide(t *testing.T) {
	rows := rowsWith([][2]float64{{0, 0.9}, {4, 0.2}, {8, 0.9}, {16, 0.2}})
	for i := range rows {
		// The batch gauge tracking inflight, as it does on a real fleet.
		rows[i].BatchKVOccupancy = 0.021 + 0.0214*float64(rows[i].Inflight)
		rows[i].BatchKVRead = true
	}

	got := bench.MeasureSpillSignals(rows)
	if !got.BatchKVVLoad.Defined || got.BatchKVVLoad.R < 0.99 {
		t.Errorf("the batch gauge correlates at %v, want it reported as tracking inflight", got.BatchKVVLoad)
	}
	report := got.Report()
	for _, want := range []string{"prefix cache hit rate", "batch KV occupancy", "0.973"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not mention %q:\n%s", want, report)
		}
	}
}
