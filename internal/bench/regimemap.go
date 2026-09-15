package bench

import (
	"fmt"
	"slices"
	"sort"

	"github.com/yuchia329/kvroute/internal/policy"
)

// The regime map is a view over a comparison already built, not a new
// reduction of cells: it answers "which policy won here" the way the
// pressure map answers "how much did the challenger gain here", by reading
// the same pooled goodput the comparison already produced. Pooling,
// exclusion, surfaced-cell handling and the one-SLO check all happen once,
// in Compare and BuildPressureMap, and this package never repeats them.
//
// Two comparisons feed it. The pressure grid crosses working set against
// skew at one concurrency, so PressureMap.Regime lays the verdict out on
// those two axes. A sweep crosses one load axis under one driver, so
// Comparison.Regime lays it out on driver against load instead — the
// load-axis companion to the grid view.

// RegimeAxis is one axis of a regime map: its name for a script, its label
// for a reader, and the values it actually has tiles for, in the order the
// map reads them.
type RegimeAxis struct {
	Name   string   `json:"name"`
	Label  string   `json:"label"`
	Values []string `json:"values"`
}

// RegimeTile is one recorded point's verdict: which policy won, by how much,
// and whether the win survives the run-to-run spread.
//
// It is read off one ComparisonRow's already-pooled goodput rather than off
// cells, for the reason the package doc says: pooling and exclusion happen
// once, in Compare, and a tile that recomputed either would be a second
// definition of them.
type RegimeTile struct {
	// X and Y are the tile's coordinates, already formatted the way its axis
	// reads them: formatAxis for a grid point's skew and working set, or a
	// load's own String for the load-axis companion.
	X, Y string
	// LoadLabel is the load rung this tile was measured at, e.g. "32 users" or
	// "6 req/s". Carried on every tile because a grid tile's X and Y never
	// mention its load, and the load-axis companion's own axis already is one.
	LoadLabel string
	// Winner and RunnerUp are the two highest-goodput policies present at this
	// point, in that order. Ties go to the earlier name in policy.Order.
	Winner, RunnerUp string
	// MarginPercent is the winner's median goodput over the runner-up's, as a
	// percentage of the runner-up's. Meaningless when RunnerUpZero.
	MarginPercent float64
	// RunnerUpZero is whether the runner-up's median was zero, in which case
	// MarginPercent is not defined — the same reason GoodputDelta carries
	// BaselineZero rather than dividing by one.
	RunnerUpZero bool
	// Measured is false when fewer than two policies have a usable cell at
	// this point, in which case Winner is empty and every field below is
	// meaningless. Absent is not zero: a policy that did not run here did not
	// score nothing here.
	Measured bool
	// Replicated is whether both the winner and the runner-up have more than
	// one repetition. A spread of zero across one run is a claim about
	// reproducibility one run cannot make.
	Replicated bool
	// WithinSpread is whether the margin is smaller than the run-to-run
	// spread: true whenever the comparison is unreplicated, or when the two
	// policies' repetition ranges overlap.
	WithinSpread bool
	// Marked is whether the winner or the runner-up rests on a repetition
	// that dropped or failed more of its requests than the threshold allows,
	// or fell behind its offered load.
	Marked bool
	// Label is the margin worded the way GoodputDelta.String() words its own
	// delta — the margin, then "within spread" or "⚠" as applicable — so the
	// two figures cannot disagree about what either phrase means.
	Label string
	// Missing is which of the expected policies had no usable cell at this
	// point.
	Missing []string
	// Goodput is every present policy's pooled goodput at this point, keyed
	// by name, for figureWithSpread to render in the detail table.
	Goodput map[string]PolicyGoodput
	// row is the comparison row this tile was read from, kept so Figure can
	// build each policy's full point — latency, prefix cache, prefill — through
	// the one row.point helper rather than a second reduction of its own.
	row ComparisonRow
}

// rankGoodput reduces one load point's pooled goodput to a verdict: who won,
// by how much, and which of the expected policies had nothing to say here.
//
// expected is the full set of policies the surrounding map covers, not only
// the ones this point measured — so Missing can name the difference. It is
// read in the order given, which is policy.Order restricted to the policies
// present (see comparisonOrder), and that order is what settles a tie: the
// two highest medians are found by a stable sort over that order, so an exact
// tie keeps whichever of the two sorts earlier.
func rankGoodput(row ComparisonRow, expected []string) RegimeTile {
	t := RegimeTile{
		LoadLabel: row.Load.String(),
		Goodput:   row.Goodput,
		row:       row,
	}
	var present []string
	for _, name := range expected {
		if _, ok := row.Goodput[name]; ok {
			present = append(present, name)
		} else {
			t.Missing = append(t.Missing, name)
		}
	}
	if len(present) < 2 {
		t.Label = "—"
		return t
	}

	sort.SliceStable(present, func(i, j int) bool {
		return row.Goodput[present[i]].MedianRPS > row.Goodput[present[j]].MedianRPS
	})
	t.Measured = true
	t.Winner, t.RunnerUp = present[0], present[1]
	winner, runnerUp := row.Goodput[t.Winner], row.Goodput[t.RunnerUp]
	t.Replicated = winner.Repetitions > 1 && runnerUp.Repetitions > 1
	t.WithinSpread = !t.Replicated || winner.overlaps(runnerUp)
	t.Marked = winner.marked() || runnerUp.marked()
	if runnerUp.MedianRPS == 0 {
		t.RunnerUpZero = true
	} else {
		t.MarginPercent = (winner.MedianRPS - runnerUp.MedianRPS) / runnerUp.MedianRPS * 100
	}
	t.Label = t.label()
	return t
}

// label is the margin as a table cell, worded the way GoodputDelta.String()
// words its own delta.
func (t RegimeTile) label() string {
	if !t.Measured {
		return "—"
	}
	var s string
	switch {
	case t.RunnerUpZero:
		s = fmt.Sprintf("+%.2f/s over zero", t.Goodput[t.Winner].MedianRPS)
	case !t.Replicated:
		s = fmt.Sprintf("%+.1f%% (unreplicated)", t.MarginPercent)
	case t.WithinSpread:
		s = fmt.Sprintf("%+.1f%% (within spread)", t.MarginPercent)
	default:
		s = fmt.Sprintf("%+.1f%%", t.MarginPercent)
	}
	if t.Marked {
		s += " ⚠"
	}
	return s
}

// headlineCell is the tile as the headline table prints it: the winner and
// its margin, or an em dash where nothing was measured.
func (t RegimeTile) headlineCell() string {
	if !t.Measured {
		return "—"
	}
	return fmt.Sprintf("%s, %s", t.Winner, t.Label)
}

// RegimeMap is which policy won at each recorded point of a comparison
// already built, laid out on two axes.
//
// It carries nothing a comparison did not already establish: Policies,
// Missing, Refused, Excluded and Surfaced are read off the comparison (or the
// grid of them) that produced it, not recomputed.
type RegimeMap struct {
	XAxis, YAxis RegimeAxis
	// SLO is the threshold every tile's goodput was judged against, carried
	// from the underlying comparison so a figure drawn from the map alone can
	// still say what goodput means.
	SLO SLO
	// Tiles are the map's tiles, in the order their comparison produced them.
	Tiles []RegimeTile
	// Policies is every policy any tile measured, in policy.Order.
	Policies []string
	// Missing, Refused, Excluded and Surfaced are carried from the underlying
	// comparison(s): points never run, points that could not be compared,
	// cells left out of every figure, and cells kept in them marked.
	Missing, Refused, Excluded, Surfaced []string
}

// Regime lays the pressure map's verdict out on its own two axes: skew on X,
// working set on Y — one tile per grid point that produced a comparison.
func (m PressureMap) Regime() RegimeMap {
	r := RegimeMap{
		SLO:      m.SLO,
		Policies: m.Policies,
		Excluded: m.Excluded,
		Surfaced: m.Surfaced,
		XAxis:    RegimeAxis{Name: "skew", Label: "skew", Values: formatAxisValues(m.skewAxis())},
		YAxis:    RegimeAxis{Name: "working_set", Label: "WS", Values: formatAxisValues(m.workingSetAxis())},
	}
	for _, point := range m.Points {
		for _, row := range point.Comparison.Rows {
			tile := rankGoodput(row, m.Policies)
			tile.X = formatAxis(point.At.Skew)
			tile.Y = formatAxis(point.At.WorkingSet)
			r.Tiles = append(r.Tiles, tile)
		}
	}
	for _, at := range m.MissingPoints() {
		r.Missing = append(r.Missing, at.String())
	}
	for _, refused := range m.Refused {
		r.Refused = append(r.Refused, fmt.Sprintf("%s: %s", refused.At, refused.Why))
	}
	return r
}

// Regime lays a comparison's verdict out on the load axis: driver on Y —
// closed-loop or open-loop — and the load rung itself on X. It is the
// load-axis companion to PressureMap.Regime, for a sweep rather than a grid.
func (c Comparison) Regime() RegimeMap {
	r := RegimeMap{
		SLO:      c.SLO,
		Policies: c.Policies,
		Excluded: c.Excluded,
		Surfaced: c.Surfaced,
	}
	var xs, ys []string
	for _, row := range c.Rows {
		xs = appendDistinct(xs, row.Load.String())
		ys = appendDistinct(ys, row.Load.Driver.Name())
	}
	r.XAxis = RegimeAxis{Name: "load", Label: "load", Values: xs}
	r.YAxis = RegimeAxis{Name: "driver", Label: "driver", Values: ys}
	for _, row := range c.Rows {
		tile := rankGoodput(row, c.Policies)
		tile.X = row.Load.String()
		tile.Y = row.Load.Driver.Name()
		r.Tiles = append(r.Tiles, tile)
	}
	return r
}

// Contested is every measured tile whose winner is not prefix affinity by
// more than the run-to-run spread — the points where some other policy beats
// the project's own challenger and the win is not noise.
func (m RegimeMap) Contested() []RegimeTile {
	var out []RegimeTile
	for _, t := range m.Tiles {
		if t.Measured && t.Winner != policy.PrefixAffinityName && !t.WithinSpread {
			out = append(out, t)
		}
	}
	return out
}

// tileAt finds the tile at one point of the map's two axes.
func (m RegimeMap) tileAt(y, x string) (RegimeTile, bool) {
	for _, t := range m.Tiles {
		if t.Y == y && t.X == x {
			return t, true
		}
	}
	return RegimeTile{}, false
}

// formatAxisValues renders a numeric axis the way the pressure grid's own
// tables do, so a regime map's axis labels cannot drift from theirs.
func formatAxisValues(vals []float64) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, formatAxis(v))
	}
	return out
}

// appendDistinct appends v to list unless it is already there, keeping the
// order values were first seen in — which for a comparison's rows is the
// sweep's own climbing order.
func appendDistinct(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}
