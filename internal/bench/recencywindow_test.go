package bench

import (
	"testing"
	"time"
)

// Why #29 re-runs the recency axis at a different cell LENGTH and not only a
// longer warm-up.
//
// Every cell of #17's recency run is flagged as still warming up: the first half
// of the measured window was 51% to 425% slower than the second by TTFT p50,
// against a 25% threshold. #29's first acceptance criterion reads that as a
// warm-up that was too short and asks for the same point re-run with a longer
// one. That is half right, and the other half decides the geometry.
//
// TTFT in those cells is not simply drifting down towards a steady state. It is
// a SAWTOOTH, and its period is the workload's own. The rotation maps the k-th
// arrival to turn k/pool, so the turn index is the round: every conversation in
// the pool advances together, and every TurnsPerSession rounds the whole pool
// rolls over to freshly drawn sessions at once. One round takes a think time, so
// the pool walks a whole session every TurnsPerSession x think time — the visit
// period below — and TTFT climbs across each period as prompts grow with their
// history, then drops back when the rollover returns every slot to a short first
// turn.
//
// Measured off the recorded rows of runs/recency on the box, TTFT p50 per visit
// period, three repetitions each:
//
//	think 30s (period 120s)   p0 288ms  p1  81ms  p2 61ms
//	                          p0 346ms  p1 252ms  p2 62ms
//	                          p0 340ms  p1 254ms  p2 65ms
//	think 75s (period 300s)   p0 291ms  p1  60ms
//	                          p0 292ms  p1  59ms
//	                          p0 296ms  p1 64ms
//
// So there are two defects in the as-run geometry, and each configuration is
// dominated by a different one — which is why a single blanket fix has to
// address both:
//
//   - think 30s: its window happens to draw every turn index evenly, so the
//     sawtooth cancels. What flags it is the genuinely cold first period. The
//     window opens 75s into a 120s period, so 45s of the coldest traffic in the
//     cell lands in the first half and none of it in the second. A longer
//     warm-up is the whole fix here, exactly as #29 assumed.
//   - think 75s: one period is 300s and the whole cell is 420s, so the window
//     cannot hold even one. Its halves share no turn index at all — index 0 is
//     600 requests to nil across the split, index 2 nil to 600. No warm-up
//     clears that, because it never decays: the check is comparing two different
//     workloads and would fire on a fleet that had been running for a week.
//
// The fix that covers both is a measured window of two whole visit periods that
// opens on a period boundary, with the warm-up covering the periods the rows
// show were still settling. Each half of the drift check is then exactly one
// visit — the same turn indices in the same proportions — so what is left in the
// comparison is the fleet, which is the only thing the check is entitled to
// speak about.
//
// These tests pin that against the generator with no fleet running, in the way
// #17's own design was checked before any GPU time was spent.

// visitPeriod is how long an open-loop cell takes to walk a conversation from
// its first turn to its last: one round per turn, and a round is the pool
// divided by the arrival rate, which is the think time.
//
// It is the period of everything that follows from prompt length — TTFT, prefill
// tokens, KV held per session — because the rotation advances every conversation
// in the pool through the same turn at the same time.
func visitPeriod(rate float64, think time.Duration, turnsPerSession int) (time.Duration, error) {
	pool, err := conversationPool(rate, think)
	if err != nil {
		return 0, err
	}
	round := time.Duration(float64(pool) / rate * float64(time.Second))
	return time.Duration(turnsPerSession) * round, nil
}

// indexMix walks the arrival schedule a cell would fire and reports how many of
// each turn index land in the first half of the measured window and how many in
// the second.
//
// It splits where warmupDrift splits — the midpoint of the measured window — so
// what it reports is the composition of the two populations whose TTFT medians
// the drift check divides. Nothing here contacts a fleet: the schedule is
// arithmetic and the turn a request carries is a pure function of (user, turn).
func (d recencyDesign) indexMix(t *testing.T, warm time.Duration) (early, late map[int]int) {
	t.Helper()
	pool, err := conversationPool(d.rate, d.think)
	if err != nil {
		t.Fatalf("pool for rate %g think %v: %v", d.rate, d.think, err)
	}
	mt, err := NewMultiTurn(MultiTurnWorkload{
		Model:                "m",
		Sessions:             922,
		TurnsPerSession:      designTurnsPerVisit,
		PromptTokens:         designPromptTokens,
		OutputTokens:         designOutputTokens,
		Skew:                 d.skew,
		SystemPromptFraction: 0.3,
		BranchFraction:       0.3,
		Seed:                 1,
	})
	if err != nil {
		t.Fatalf("generator: %v", err)
	}

	turns := newRotation(pool, 7)
	interval := time.Duration(float64(time.Second) / d.rate)
	midpoint := warm + (d.cell-warm)/2
	early, late = map[int]int{}, map[int]int{}

	for k := range int(d.cell.Seconds() * d.rate) {
		due := time.Duration(k) * interval
		if due < warm {
			continue
		}
		user, turn := turns.at(k)
		index := mt.Next(user, turn).Index
		if due < midpoint {
			early[index]++
		} else {
			late[index]++
		}
	}
	return early, late
}

// indexImbalance reports how far the worst turn index is from falling half in
// each half of the measured window. Zero is a window whose two medians are taken
// over the same workload.
func indexImbalance(early, late map[int]int) (worst int, gap float64) {
	for index := range designTurnsPerVisit {
		total := early[index] + late[index]
		if total == 0 {
			continue
		}
		off := float64(early[index])/float64(total) - 0.5
		if off < 0 {
			off = -off
		}
		if off > gap {
			gap, worst = off, index
		}
	}
	return worst, gap
}

// The two defects in #17's recency geometry, each in the configuration it
// dominates.
//
// Neither window opens on a visit boundary, which is the shared root: it is what
// lets the cold opening period sit in one half of think 30s's window, and it is
// why think 75s's halves hold disjoint turn indices. The per-configuration
// assertions below are what the fix has to clear.
func TestTheAsRunRecencyWindowsDoNotOpenOnAVisitBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    recencyDesign
		warm time.Duration
		// wantBalanced is whether this window happens to draw every turn index
		// evenly despite opening mid-visit.
		wantBalanced bool
	}{
		{"think30", recencyDesign{rate: 8, think: 30 * time.Second, cell: 300 * time.Second, workingSet: 3, skew: 1.0}, 75 * time.Second, true},
		{"think75", recencyDesign{rate: 8, think: 75 * time.Second, cell: 420 * time.Second, workingSet: 3, skew: 1.0}, 105 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			period, err := visitPeriod(tc.d.rate, tc.d.think, designTurnsPerVisit)
			if err != nil {
				t.Fatalf("visit period: %v", err)
			}
			t.Logf("visit period %v, window %v-%v = %.3f periods, opens %v into a period",
				period, tc.warm, tc.d.cell, float64(tc.d.cell-tc.warm)/float64(period), tc.warm%period)

			// The shared root defect. Both windows open part-way through a
			// visit, so the traffic either side of the split is not the same
			// traffic however long the fleet has been up.
			if tc.warm%period == 0 {
				t.Errorf("this window already opens on a visit boundary, so the re-run's premise needs rechecking")
			}
			// The cold opening period is in the window at all, which is what a
			// longer warm-up is for.
			if tc.warm >= period {
				t.Errorf("the warm-up already covers the opening visit period, so the cold-start half of the diagnosis needs rechecking")
			}

			early, late := tc.d.indexMix(t, tc.warm)
			for index := range designTurnsPerVisit {
				t.Logf("  turn index %d: first half %4d, second half %4d", index, early[index], late[index])
			}
			worst, gap := indexImbalance(early, late)
			switch balanced := gap < 0.01; {
			case balanced != tc.wantBalanced && tc.wantBalanced:
				t.Errorf("turn index %d lands %.0f%%/%.0f%% across the split; this configuration was diagnosed as flagging on the cold opening period alone, with the sawtooth cancelling",
					worst, (0.5+gap)*100, (0.5-gap)*100)
			case balanced != tc.wantBalanced:
				t.Errorf("every turn index splits evenly here, so the sawtooth cannot be what flagged this configuration and the diagnosis needs rechecking")
			case !balanced:
				t.Logf("worst index %d sits %.0f%%/%.0f%% across the split", worst, (0.5+gap)*100, (0.5-gap)*100)
			}
		})
	}
}

// The property the re-run's geometry is chosen for.
//
// A measured window of two whole visit periods, beginning on a period boundary,
// hands the drift check two halves of exactly one visit each. Every turn index
// appears the same number of times in both, and the cold opening periods are
// behind the warm-up rather than inside one half of the measurement.
func TestTheRerunRecencyWindowsSplitTheWorkloadEvenly(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    recencyDesign
		warm time.Duration
	}{
		{"think30", recencyDesign{rate: 8, think: 30 * time.Second, cell: 480 * time.Second, workingSet: 3, skew: 1.0}, 240 * time.Second},
		{"think75", recencyDesign{rate: 8, think: 75 * time.Second, cell: 900 * time.Second, workingSet: 3, skew: 1.0}, 300 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			early, late := tc.d.indexMix(t, tc.warm)
			for index := range designTurnsPerVisit {
				t.Logf("  turn index %d: first half %4d, second half %4d", index, early[index], late[index])
			}
			if worst, gap := indexImbalance(early, late); gap >= 0.01 {
				t.Errorf("turn index %d lands %.0f%% in the first half of the measured window against %.0f%% in the second, so the drift check is still comparing two different workloads",
					worst, (0.5+gap)*100, (0.5-gap)*100)
			}
		})
	}
}

// The geometries #29 runs, and the arithmetic behind each number.
//
// Held here rather than only in the box driver because the driver is a shell
// script the box runs and this is the reason it runs those values. A cell length
// that stops being a whole number of visit periods is the defect this whole file
// exists to catch, and it is silent: the run completes, the cells flag, and the
// curve stays unpublished for a second time.
func TestTheRerunGeometryIsWholeVisitPeriods(t *testing.T) {
	for _, tc := range []struct {
		name string
		// warmPeriods is how many visit periods are spent warming up: the
		// periods the recorded rows show the fleet was still settling through.
		warmPeriods int
		think       time.Duration
		wantPeriod  time.Duration
		wantWarm    time.Duration
		wantCell    time.Duration
	}{
		// think 30s settled at p2 (61-65ms) with p1 still at 81-254ms across
		// the three repetitions, so two periods are warm-up.
		{"think30", 2, 30 * time.Second, 120 * time.Second, 240 * time.Second, 480 * time.Second},
		// think 75s was already at 59-64ms by p1, so one period clears it — and
		// at this think time a period is 300s, so a second would cost five
		// minutes a cell to re-measure something already steady.
		{"think75", 1, 75 * time.Second, 300 * time.Second, 300 * time.Second, 900 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const rate = 8
			period, err := visitPeriod(rate, tc.think, designTurnsPerVisit)
			if err != nil {
				t.Fatalf("visit period: %v", err)
			}
			if period != tc.wantPeriod {
				t.Fatalf("visit period is %v, not the %v the geometry was derived from", period, tc.wantPeriod)
			}
			warm := time.Duration(tc.warmPeriods) * period
			// Two periods measured, so each half of the drift check's split is
			// one whole visit.
			cell := warm + 2*period
			if warm != tc.wantWarm || cell != tc.wantCell {
				t.Errorf("geometry is %v warm-up in a %v cell, want %v in %v", warm, cell, tc.wantWarm, tc.wantCell)
			}
			// The window opens on a boundary and spans an even number of
			// periods: the two conditions that make its halves like for like.
			if warm%period != 0 {
				t.Errorf("warm-up %v does not end on a %v visit boundary, so the window opens mid-visit", warm, period)
			}
			if measured := cell - warm; measured%period != 0 || (measured/period)%2 != 0 {
				t.Errorf("measured window %v is not an even number of %v visit periods, so its halves hold different turn indices", measured, period)
			}
			t.Logf("%s: period %v, warm-up %v (%d periods), cell %v, measured %v (%d periods)",
				tc.name, period, warm, tc.warmPeriods, cell, cell-warm, (cell-warm)/period)
		})
	}
}

// The re-run's longer cells must still offer the axis they are re-running.
//
// #17 chose its two configurations so that every recency bucket up to the
// derived 57s TTL was occupied and none of them swallowed the cell, and so that
// the second reached past the TTL. #29 lengthens both cells and their warm-ups
// to settle the drift check, and a geometry that fixed the flag by flattening
// the axis would have bought nothing: the curve is the deliverable. So the same
// two properties are re-asserted here against the geometry that will actually
// run.
func TestTheRerunStillSpreadsTheRecencyAxis(t *testing.T) {
	const derivedTTL = 57 * time.Second

	think30 := recencyDesign{rate: 8, think: 30 * time.Second, cell: 480 * time.Second, workingSet: 3, skew: 1.0}
	buckets, measured, live := think30.offer(t, 240*time.Second)
	t.Logf("think30: pool tokens %d = %.2fx fleet KV over %d measured arrivals", live, float64(live)/designFleetKV, measured)
	var occupied int
	for _, label := range recencyLabels {
		share := 100 * float64(buckets[label]) / float64(measured)
		t.Logf("  %-12s %6d %5.1f%%", label, buckets[label], share)
		if buckets[label] > 0 {
			occupied++
		}
		if share > 60 {
			t.Errorf("bucket %s holds %.1f%% of the re-run's think30 cell, so the axis is one point and a tail", label, share)
		}
	}
	if want := len(recencyLabels) - 1; occupied < want {
		t.Errorf("the re-run's think30 cell occupies only %d of %d recency buckets, so the published plot would have gaps", occupied, want)
	}

	think75 := recencyDesign{rate: 8, think: 75 * time.Second, cell: 900 * time.Second, workingSet: 3, skew: 1.0}
	buckets, measured, _ = think75.offer(t, 300*time.Second)
	var beyond int
	for i, bound := range recencyBuckets {
		if bound > derivedTTL {
			beyond += buckets[recencyLabels[i+1]]
		}
	}
	beyond += buckets[recencyLabels[len(recencyLabels)-1]]
	for _, label := range recencyLabels {
		t.Logf("  %-12s %6d %5.1f%%", label, buckets[label], 100*float64(buckets[label])/float64(measured))
	}
	if share := 100 * float64(beyond) / float64(measured); share < 10 {
		t.Errorf("only %.1f%% of the re-run's think75 cell is older than the %v TTL, so the half of the curve where the index has already forgotten would rest on too few requests", share, derivedTTL)
	}
}
