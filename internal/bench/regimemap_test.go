package bench_test

import (
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// bothAt builds two policies' repetitions at one load point, the load-axis
// counterpart of bothPolicies for the grid.
func bothAt(load bench.Load, session, prefix []float64) []bench.Cell {
	var out []bench.Cell
	out = append(out, cells(policy.SessionAffinityName, load, session...)...)
	out = append(out, cells(policy.PrefixAffinityName, load, prefix...)...)
	return out
}

// findTile is the tile at one (Y, X) of a regime map, for a test that wants
// to check one point without depending on the order tiles were built in.
func findTile(t *testing.T, tiles []bench.RegimeTile, y, x string) bench.RegimeTile {
	t.Helper()
	for _, tile := range tiles {
		if tile.Y == y && tile.X == x {
			return tile
		}
	}
	t.Fatalf("no tile at (%s, %s) in %+v", y, x, tiles)
	return bench.RegimeTile{}
}

// The plain case: one policy clearly ahead, replicated, not overlapping. The
// regime map is a view over the comparison already built, so this is read
// off Comparison.Regime rather than a second reduction of the cells.
func TestARegimeTileNamesTheWinnerAndItsMargin(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	c := compare(t, bothAt(at8, []float64{8, 8, 8}, []float64{20, 21, 22}))

	tile := findTile(t, c.Regime().Tiles, "closed-loop", "8 users")

	if !tile.Measured || tile.Winner != policy.PrefixAffinityName || tile.RunnerUp != policy.SessionAffinityName {
		t.Fatalf("tile = %+v, want prefix affinity to win over session affinity", tile)
	}
	// stats.Quantile picks the middle of three, so 21 over 8.
	if !strings.Contains(tile.Label, "+162.5%") {
		t.Errorf("label = %q, want the margin of 21 over 8", tile.Label)
	}
	if tile.WithinSpread {
		t.Errorf("21 against 8 with no overlap is reported within spread: %+v", tile)
	}
}

// Overlapping repetition ranges are the difference between a policy and
// itself, and the tile has to say so the same way the comparison's own delta
// does.
func TestARegimeTileIsWithinSpreadWhenTheRangesOverlap(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	// 8.0-9.5 against 8.5-10.0: overlapping.
	c := compare(t, bothAt(at8, []float64{8.0, 8.5, 9.5}, []float64{8.5, 9.0, 10.0}))

	tile := findTile(t, c.Regime().Tiles, "closed-loop", "8 users")

	if !tile.Measured || !tile.Replicated {
		t.Fatalf("tile = %+v, want it measured and replicated", tile)
	}
	if !tile.WithinSpread {
		t.Errorf("overlapping ranges are not reported within spread: %+v", tile)
	}
	if !strings.Contains(tile.Label, "within spread") {
		t.Errorf("label = %q, want it to say within spread", tile.Label)
	}
}

// stats.Quantile picks the lower of a pair for n=2, the same rule goodput is
// pooled by everywhere else in this package, so an n=2 winner's own median is
// its lower repetition rather than an average of the two.
func TestARegimeTileAtNEqualTwoUsesTheLowerMedian(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	c := compare(t, bothAt(at8, []float64{8, 8}, []float64{20, 30}))

	tile := findTile(t, c.Regime().Tiles, "closed-loop", "8 users")

	winner := tile.Goodput[tile.Winner]
	if winner.MedianRPS != 20 {
		t.Errorf("winner's median = %v, want the lower of 20 and 30", winner.MedianRPS)
	}
}

// A policy the map covers but one point never measured is named there, the
// same rule a comparison's own gap follows: absent is not zero.
func TestARegimeTileNamesAPolicyMissingAtOnePoint(t *testing.T) {
	threePolicyPoint := bench.GridPoint{WorkingSet: 3, Skew: 0}
	var cs []bench.Cell
	cs = append(cs, gridCells(highPressure, policy.RoundRobinName, 8, 8, 8)...)
	cs = append(cs, gridCells(highPressure, policy.LeastOutstandingName, 9, 9, 9)...)
	cs = append(cs, gridCells(highPressure, policy.SessionAffinityName, 10, 10, 10)...)
	cs = append(cs, gridCells(highPressure, policy.PrefixAffinityName, 11, 11, 11)...)
	// prefix_affinity never ran at this second point, though the map covers it.
	cs = append(cs, gridCells(threePolicyPoint, policy.RoundRobinName, 8, 8, 8)...)
	cs = append(cs, gridCells(threePolicyPoint, policy.LeastOutstandingName, 9, 9, 9)...)
	cs = append(cs, gridCells(threePolicyPoint, policy.SessionAffinityName, 10, 10, 10)...)

	m := buildMap(t, cs)
	tile := findTile(t, m.Regime().Tiles, "3", "0")

	if len(tile.Missing) != 1 || tile.Missing[0] != policy.PrefixAffinityName {
		t.Errorf("missing = %v, want just %q", tile.Missing, policy.PrefixAffinityName)
	}
}

// A cell that failed too many requests marks the tile the way it marks the
// comparison's own goodput figure — this is what the policy did to the
// fleet, not a broken measurement.
func TestARegimeTileMarksAFailingRepetition(t *testing.T) {
	at128 := bench.ClosedLoopAt(128)
	var cs []bench.Cell
	cs = append(cs, cells(policy.RoundRobinName, at128, 8, 8, 8)...)
	cs = append(cs,
		cell(policy.LeastOutstandingName, at128, 1, 20),
		failing(policy.LeastOutstandingName, at128, 2, 3),
		cell(policy.LeastOutstandingName, at128, 3, 22))
	c := compare(t, cs)

	tile := findTile(t, c.Regime().Tiles, "closed-loop", "128 users")

	if !tile.Marked {
		t.Errorf("tile resting on a failing repetition is not marked: %+v", tile)
	}
	if !strings.Contains(tile.Label, "⚠") {
		t.Errorf("label = %q, want the ⚠ the goodput figure carries", tile.Label)
	}
}

// Fewer than two usable policies at a point is not a comparison, so nothing
// is measured there — the same gap TestALoadPointOnePolicyNeverReachedIsAGapNotAZero
// reports for the comparison table itself.
func TestARegimeTileIsNotMeasuredWhereOnlyOnePolicyRan(t *testing.T) {
	at8, at256 := bench.ClosedLoopAt(8), bench.ClosedLoopAt(256)
	var cs []bench.Cell
	cs = append(cs, cells(policy.RoundRobinName, at8, 8.0)...)
	cs = append(cs, cells(policy.LeastOutstandingName, at8, 9.0)...)
	// The sweep stopped before least-outstanding reached the top rung.
	cs = append(cs, cells(policy.RoundRobinName, at256, 4.0)...)
	c := compare(t, cs)

	tile := findTile(t, c.Regime().Tiles, "closed-loop", "256 users")

	if tile.Measured || tile.Winner != "" {
		t.Errorf("tile = %+v, want unmeasured with no winner", tile)
	}
	if tile.Label != "—" {
		t.Errorf("label = %q, want an em dash", tile.Label)
	}
}

// The grid view lays skew on X and working set on Y, one tile per grid
// point that produced a comparison.
func TestPressureMapRegimeLaysSkewOnXAndWorkingSetOnY(t *testing.T) {
	m := buildMap(t, bothPolicies(highSkew, []float64{6, 7, 8}, []float64{20, 21, 22}))
	r := m.Regime()

	if r.XAxis.Name != "skew" || r.YAxis.Name != "working_set" {
		t.Fatalf("axes = %s x %s, want skew x working_set", r.XAxis.Name, r.YAxis.Name)
	}
	if len(r.Tiles) != 1 {
		t.Fatalf("%d tiles, want one for the one grid point", len(r.Tiles))
	}
	tile := r.Tiles[0]
	if tile.X != "1.4" || tile.Y != "1" {
		t.Errorf("tile at (%s, %s), want skew 1.4 and WS 1", tile.X, tile.Y)
	}
	if tile.Winner != policy.PrefixAffinityName {
		t.Errorf("winner = %q, want prefix affinity, clearly ahead", tile.Winner)
	}
}

// The load-axis companion lays driver on Y and the load rung on X. Two
// drivers give two Y rows.
func TestComparisonRegimeGivesTwoYRowsForTwoDrivers(t *testing.T) {
	var cs []bench.Cell
	cs = append(cs, bothAt(bench.ClosedLoopAt(8), []float64{8, 8, 8}, []float64{9, 9, 9})...)
	cs = append(cs, bothAt(bench.OpenLoopAt(16), []float64{8, 8, 8}, []float64{9, 9, 9})...)

	r := compare(t, cs).Regime()

	if r.YAxis.Name != "driver" || r.XAxis.Name != "load" {
		t.Fatalf("axes = %s x %s, want driver x load", r.YAxis.Name, r.XAxis.Name)
	}
	if len(r.YAxis.Values) != 2 {
		t.Fatalf("Y axis = %v, want two drivers", r.YAxis.Values)
	}
	if len(r.Tiles) != 2 {
		t.Fatalf("%d tiles, want one per row of the comparison", len(r.Tiles))
	}
}

// Two loads under one driver give two X columns in one Y row.
func TestComparisonRegimeGivesTwoXColumnsForOneDriver(t *testing.T) {
	var cs []bench.Cell
	cs = append(cs, bothAt(bench.ClosedLoopAt(8), []float64{8, 8, 8}, []float64{9, 9, 9})...)
	cs = append(cs, bothAt(bench.ClosedLoopAt(16), []float64{8, 8, 8}, []float64{9, 9, 9})...)

	r := compare(t, cs).Regime()

	if len(r.YAxis.Values) != 1 {
		t.Fatalf("Y axis = %v, want the one driver", r.YAxis.Values)
	}
	if len(r.XAxis.Values) != 2 {
		t.Errorf("X axis = %v, want the two load rungs", r.XAxis.Values)
	}
}

// Contested is the one tile where a policy other than prefix affinity beat
// the runner-up beyond the spread — here least-outstanding, clearly ahead of
// both affinities at one grid point among two.
func TestContestedReturnsTheOnePolicyThatBeatsPrefixAffinityBeyondSpread(t *testing.T) {
	var cs []bench.Cell
	cs = append(cs, bothPolicies(lowPressure, []float64{10, 10, 10}, []float64{10, 10, 10})...)
	contested := bothPolicies(highPressure, []float64{6, 6, 6}, []float64{7, 7, 7})
	contested = append(contested, gridCells(highPressure, policy.LeastOutstandingName, 20, 21, 22)...)
	cs = append(cs, contested...)

	got := buildMap(t, cs).Regime().Contested()

	if len(got) != 1 {
		t.Fatalf("%d contested tiles, want one: %+v", len(got), got)
	}
	if got[0].Y != "8" || got[0].Winner != policy.LeastOutstandingName {
		t.Errorf("contested tile = %+v, want WS 8 won by least_outstanding", got[0])
	}
}

// Where prefix affinity wins everywhere, nothing contests it.
func TestContestedIsEmptyWhenPrefixAffinityWinsEverywhere(t *testing.T) {
	m := buildMap(t, bothPolicies(highPressure, []float64{8, 8, 8}, []float64{20, 21, 22}))
	if got := m.Regime().Contested(); len(got) != 0 {
		t.Errorf("contested = %+v, want none: prefix affinity wins the only point", got)
	}
}

// The report's headline table names the winner and its margin. The heading
// itself is followed by a blank line before the table, the way every heading
// in this report is, so the assertion reads the whole report rather than
// cutting at section()'s first blank line — the same reason the pressure
// map's own headline tests do.
func TestRegimeMapReportHeadlineNamesTheWinnerAndMargin(t *testing.T) {
	m := buildMap(t, bothPolicies(highPressure, []float64{8, 8, 8}, []float64{20, 21, 22}))
	report := m.Regime().Report()

	if !strings.Contains(report, policy.PrefixAffinityName+", +162.5%") {
		t.Errorf("the headline does not name the winner and margin:\n%s", report)
	}
	// The table row itself, so the figure is checked in place rather than
	// merely present somewhere in the document.
	if !strings.Contains(section(report, "| **8** |"), policy.PrefixAffinityName+", +162.5%") {
		t.Errorf("the WS 8 row does not carry the winner and margin:\n%s", section(report, "| **8** |"))
	}
}

// The Contested section lists the tiles Contested() returns, worded with
// each one's own winner.
func TestRegimeMapReportListsContestedTiles(t *testing.T) {
	var cs []bench.Cell
	cs = append(cs, bothPolicies(lowPressure, []float64{10, 10, 10}, []float64{10, 10, 10})...)
	contested := bothPolicies(highPressure, []float64{6, 6, 6}, []float64{7, 7, 7})
	contested = append(contested, gridCells(highPressure, policy.LeastOutstandingName, 20, 21, 22)...)
	cs = append(cs, contested...)

	report := buildMap(t, cs).Regime().Report()

	if !strings.Contains(report, "## Contested") || !strings.Contains(report, policy.LeastOutstandingName) {
		t.Errorf("the report does not have a Contested section naming least_outstanding:\n%s", report)
	}
	if !strings.Contains(report, policy.LeastOutstandingName+" beats the runner-up") {
		t.Errorf("the contested bullet does not name least_outstanding as the winner:\n%s", section(report, "## Contested"))
	}
}

// Where nothing contests the challenger, the report says so in words rather
// than printing an empty section.
func TestRegimeMapReportSaysNoContestWhenPrefixAffinityWinsEverywhere(t *testing.T) {
	m := buildMap(t, bothPolicies(highPressure, []float64{8, 8, 8}, []float64{20, 21, 22}))
	report := m.Regime().Report()

	if !strings.Contains(report, "No point contests prefix affinity beyond the spread.") {
		t.Errorf("the report does not say prefix affinity is uncontested:\n%s", report)
	}
}
