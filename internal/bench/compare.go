package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yuchia329/kvroute/internal/policy"
)

// Comparison is what two or more policies did to the same fleet, at the same
// points of the same load axis, judged against one SLO.
//
// It is the deliverable's shape: the project's claim is comparative, so the unit
// that gets published is not a policy's cells but the difference between two
// policies' cells. Everything that would make such a difference meaningless is
// therefore refused here rather than rendered — a different SLO on either side, a
// different workload, or a policy that only ran at some of the load points.
type Comparison struct {
	// SLO is the threshold every cell was judged against. Goodput is defined by
	// it, so a comparison across two of them is not a comparison.
	SLO SLO
	// Workload is the generator every cell sent from. Both policies have to see
	// the same bytes, or the difference between them is a difference in prompts.
	Workload string
	// Policies are the policies compared, baseline first.
	Policies []string
	Rows     []ComparisonRow
	// Excluded is every cell left out of the figures, with the reason. Flagged and
	// unclean cells are never averaged in, and a comparison that dropped them
	// silently would be the same table with the evidence removed.
	Excluded []string
}

// ComparisonRow is one point of the load axis, across policies.
type ComparisonRow struct {
	Load Load
	// Goodput is keyed by policy name. A policy with no usable cell at this load
	// point is absent rather than zero: it did not score nothing, it did not run.
	Goodput map[string]PolicyGoodput
}

// PolicyGoodput is one policy's goodput at one load point, pooled over the
// repetitions that were fit to pool.
//
// The median is the figure and the range is published beside it, because with
// three repetitions the interesting question about any difference between two
// policies is whether it is bigger than the difference between one policy and
// itself.
type PolicyGoodput struct {
	Policy      string
	Load        Load
	Repetitions int
	MedianRPS   float64
	MinRPS      float64
	MaxRPS      float64
}

// String is the figure as a table cell: the median, and the range it came from
// when there is more than one repetition to have a range.
//
// A single repetition prints no range rather than a range of zero. A spread of
// zero is a claim about reproducibility that one run cannot make.
func (g PolicyGoodput) String() string {
	if g.Repetitions <= 1 {
		return fmt.Sprintf("%.2f (n=1)", g.MedianRPS)
	}
	return fmt.Sprintf("%.2f (%.2f–%.2f, n=%d)", g.MedianRPS, g.MinRPS, g.MaxRPS, g.Repetitions)
}

// overlaps reports whether two policies' repetition ranges overlap, which is when
// the difference between their medians is inside the spread of either.
func (g PolicyGoodput) overlaps(other PolicyGoodput) bool {
	return g.MinRPS <= other.MaxRPS && other.MinRPS <= g.MaxRPS
}

// Compare reduces cells to the comparison between their policies.
//
// It refuses rather than renders when the cells cannot be compared: goodput
// against two different SLOs, or over two different workloads, is two numbers
// about two experiments. Cells that are flagged or unclean are excluded from the
// figures and listed, because §6 discards them rather than averaging them in.
func Compare(cells []Cell) (Comparison, error) {
	if len(cells) == 0 {
		return Comparison{}, fmt.Errorf("bench: no cells to compare")
	}

	c := Comparison{}
	if err := checkComparable(cells); err != nil {
		return Comparison{}, err
	}
	c.SLO = SLO{
		TTFT: time.Duration(cells[0].SLOTTFTNs),
		ITL:  time.Duration(cells[0].SLOITLNs),
	}
	c.Workload = cells[0].Workload

	// Usable cells make the figures; the rest are named. Both passes see every
	// cell, so a load point at which every repetition was thrown away shows up as
	// a row with a gap in it rather than as no row at all.
	usable := map[string]map[Load][]Cell{}
	loads := map[Load]bool{}
	policies := map[string]bool{}
	for _, cell := range cells {
		policies[cell.Policy] = true
		loads[cell.Load()] = true
		if reasons := excludedFor(cell); len(reasons) > 0 {
			for _, reason := range reasons {
				c.Excluded = append(c.Excluded, fmt.Sprintf("`%s`: %s", cell.ID, reason))
			}
			continue
		}
		if usable[cell.Policy] == nil {
			usable[cell.Policy] = map[Load][]Cell{}
		}
		usable[cell.Policy][cell.Load()] = append(usable[cell.Policy][cell.Load()], cell)
	}
	if len(policies) < 2 {
		return Comparison{}, fmt.Errorf("bench: %d policy in these cells (%s), and a comparison needs at least two",
			len(policies), strings.Join(sortedKeys(policies), ", "))
	}
	c.Policies = comparisonOrder(policies)

	for _, load := range sortedLoads(loads) {
		row := ComparisonRow{Load: load, Goodput: map[string]PolicyGoodput{}}
		for _, name := range c.Policies {
			if pooled, ok := pool(name, load, usable[name][load]); ok {
				row.Goodput[name] = pooled
			}
		}
		c.Rows = append(c.Rows, row)
	}
	return c, nil
}

// checkComparable rejects cells that cannot be put in one table.
func checkComparable(cells []Cell) error {
	var unjudged []string
	slos := map[SLO][]string{}
	workloads := map[string][]string{}
	for _, cell := range cells {
		if !cell.SLOApplied {
			unjudged = append(unjudged, cell.ID)
			continue
		}
		slo := SLO{TTFT: time.Duration(cell.SLOTTFTNs), ITL: time.Duration(cell.SLOITLNs)}
		slos[slo] = append(slos[slo], cell.ID)
		workloads[cell.Workload] = append(workloads[cell.Workload], cell.ID)
	}

	if len(unjudged) > 0 {
		// Goodput is requests per second that met the SLO. Without one there is no
		// goodput, and reporting throughput in its column is how a fleet with a
		// dead tail is made to look fast.
		return fmt.Errorf("bench: %d cells were run without an SLO, so they have no goodput to compare (%s). Re-run them with the SLO the characterization derived",
			len(unjudged), strings.Join(truncate(unjudged, 4), ", "))
	}
	if len(slos) > 1 {
		return fmt.Errorf("bench: these cells were judged against %d different SLOs, so their goodput figures are not comparable: %s",
			len(slos), describeSLOs(slos))
	}
	if len(workloads) > 1 {
		var described []string
		for name, ids := range workloads {
			described = append(described, fmt.Sprintf("%s (%d cells, e.g. %s)", name, len(ids), ids[0]))
		}
		sort.Strings(described)
		return fmt.Errorf("bench: these cells sent %d different workloads, so the difference between the policies would include a difference in prompts: %s",
			len(workloads), strings.Join(described, "; "))
	}
	return nil
}

// excludedFor is why this cell's figures are not pooled with the others.
func excludedFor(cell Cell) []string {
	if !cell.Flagged {
		return nil
	}
	if len(cell.FlagReasons) > 0 {
		return cell.FlagReasons
	}
	return []string{"flagged, with no reason recorded"}
}

// pool reduces a policy's repetitions at one load point to one figure.
func pool(name string, load Load, cells []Cell) (PolicyGoodput, bool) {
	if len(cells) == 0 {
		return PolicyGoodput{}, false
	}
	rates := make([]float64, 0, len(cells))
	for _, cell := range cells {
		rates = append(rates, cell.GoodputRPS)
	}
	slices.Sort(rates)
	return PolicyGoodput{
		Policy:      name,
		Load:        load,
		Repetitions: len(rates),
		MedianRPS:   median(rates),
		MinRPS:      rates[0],
		MaxRPS:      rates[len(rates)-1],
	}, true
}

// median of a sorted slice. The middle value, or the mean of the two middles: with
// three repetitions the median is the one that is not the best or the worst run,
// which is what a repetition count of three is for.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// comparisonOrder puts the policies in the order idea.md §5 numbers them, with
// any policy that order does not know about after them, so the baseline is the
// baseline whichever order the runs happened in.
func comparisonOrder(present map[string]bool) []string {
	ordered := make([]string, 0, len(present))
	for _, name := range policy.Order {
		if present[name] {
			ordered = append(ordered, name)
		}
	}
	var rest []string
	for name := range present {
		if !slices.Contains(policy.Order, name) {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(ordered, rest...)
}

// sortedLoads orders the load points the way a sweep climbs them: the closed-loop
// axis first, then the open-loop one, each ascending. Two axes in one table are
// two blocks of rows rather than an interleaving, because a concurrency and an
// arrival rate are not points on one scale.
func sortedLoads(present map[Load]bool) []Load {
	loads := make([]Load, 0, len(present))
	for load := range present {
		loads = append(loads, load)
	}
	slices.SortFunc(loads, func(a, b Load) int {
		if a.Driver != b.Driver {
			if a.Driver == OpenLoopDriver {
				return 1
			}
			return -1
		}
		if a.Concurrency != b.Concurrency {
			return a.Concurrency - b.Concurrency
		}
		switch {
		case a.ArrivalRate < b.ArrivalRate:
			return -1
		case a.ArrivalRate > b.ArrivalRate:
			return 1
		}
		return 0
	})
	return loads
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func truncate(ids []string, n int) []string {
	if len(ids) <= n {
		return ids
	}
	return append(ids[:n:n], fmt.Sprintf("and %d more", len(ids)-n))
}

func describeSLOs(slos map[SLO][]string) string {
	described := make([]string, 0, len(slos))
	for slo, ids := range slos {
		described = append(described, fmt.Sprintf("TTFT < %v, ITL < %v (%d cells, e.g. %s)",
			slo.TTFT, slo.ITL, len(ids), ids[0]))
	}
	sort.Strings(described)
	return strings.Join(described, "; ")
}

// Report renders the comparison: what was held constant, the table, and what was
// left out of it.
//
// The SLO leads because goodput is defined by it, and the driver is named on
// every row because a closed-loop goodput and an open-loop one are answers to
// different questions.
func (c Comparison) Report() string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Policy comparison — goodput against the derived SLO\n\n")
	fmt.Fprintf(&b, "SLO: TTFT < %v, inter-token p50 < %v. Goodput is requests per second that met it,\n", c.SLO.TTFT, c.SLO.ITL)
	fmt.Fprintf(&b, "so a policy that completed more requests can still score lower.\n\n")
	fmt.Fprintf(&b, "Workload: %s. Both policies sent the same bytes at the\n", c.Workload)
	fmt.Fprintf(&b, "same load point, which is what makes them comparable at all.\n\n")
	fmt.Fprintf(&b, "Each figure is the median of that cell's repetitions, with the range across them. A\n")
	fmt.Fprintf(&b, "difference smaller than those ranges is a difference between a policy and itself.\n\n")

	if !c.usable() {
		// Otherwise the reader is handed a table of em dashes and left to work out
		// whether the sweep never ran or every cell was thrown away.
		fmt.Fprintf(&b, "⚠️ **Nothing in this table rests on a usable cell: every one was excluded, for the\n")
		fmt.Fprintf(&b, "reasons listed underneath. There is no comparison here until they are re-run.**\n\n")
	}

	baseline := ""
	if len(c.Policies) > 0 {
		baseline = c.Policies[0]
	}

	fmt.Fprintf(&b, "| driver | load |")
	for _, name := range c.Policies {
		fmt.Fprintf(&b, " %s |", name)
	}
	for _, name := range c.Policies[1:] {
		fmt.Fprintf(&b, " Δ %s vs %s |", name, baseline)
	}
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "|---|---:|")
	for range c.Policies {
		fmt.Fprintf(&b, "---:|")
	}
	for range c.Policies[1:] {
		fmt.Fprintf(&b, "---:|")
	}
	fmt.Fprintln(&b)

	for _, row := range c.Rows {
		fmt.Fprintf(&b, "| %s | %s |", row.Load.Driver.Name(), row.Load)
		for _, name := range c.Policies {
			fmt.Fprintf(&b, " %s |", figure(row.Goodput, name))
		}
		for _, name := range c.Policies[1:] {
			fmt.Fprintf(&b, " %s |", delta(row.Goodput[baseline], row.Goodput[name],
				row.has(baseline), row.has(name)))
		}
		fmt.Fprintln(&b)
	}

	if len(c.Excluded) > 0 {
		fmt.Fprintf(&b, "\nExcluded from every figure above — §6 discards these rather than averaging them in:\n\n")
		for _, reason := range c.Excluded {
			fmt.Fprintf(&b, "- %s\n", reason)
		}
	}
	return b.String()
}

// usable reports whether any figure in the comparison rests on a cell that was
// kept.
func (c Comparison) usable() bool {
	for _, row := range c.Rows {
		if len(row.Goodput) > 0 {
			return true
		}
	}
	return false
}

// has reports whether a policy produced a usable figure at this load point.
func (r ComparisonRow) has(policy string) bool {
	_, ok := r.Goodput[policy]
	return ok
}

// figure is one policy's cell in the table, or an em dash where it has no usable
// cell. Absent is not zero: a policy that did not run at this load point did not
// score nothing there.
func figure(goodput map[string]PolicyGoodput, policy string) string {
	g, ok := goodput[policy]
	if !ok {
		return "—"
	}
	return g.String()
}

// delta is the difference between the baseline and a challenger, as a percentage
// of the baseline, qualified by what the repetitions can support.
//
// A difference inside the two ranges' overlap is reported as such rather than as a
// win: run-to-run spread on a shared box is the thing most likely to be mistaken
// for a result.
func delta(base, challenger PolicyGoodput, hasBase, hasChallenger bool) string {
	if !hasBase || !hasChallenger {
		return "—"
	}
	if base.MedianRPS == 0 {
		// A baseline of zero has no percentage. It is a real reading — the fleet
		// met the SLO for nothing at this load — so it is described rather than
		// divided by.
		return fmt.Sprintf("+%.2f/s over a baseline of zero", challenger.MedianRPS)
	}
	change := (challenger.MedianRPS - base.MedianRPS) / base.MedianRPS * 100
	switch {
	case base.Repetitions < 2 || challenger.Repetitions < 2:
		return fmt.Sprintf("%+.1f%% (unreplicated)", change)
	case base.overlaps(challenger):
		return fmt.Sprintf("%+.1f%% (within spread)", change)
	default:
		return fmt.Sprintf("%+.1f%%", change)
	}
}

// LoadCells reads every completed cell of a sweep directory.
//
// It reads the cell records rather than the rows: the rows are the system of
// record and the cell record is the arithmetic over them, and re-deriving a
// hundred cells' summaries to render one table would apply thresholds that the
// runs themselves have already recorded. A cell record that will not parse is an
// error rather than a skip — this is the reporting path, where a missing cell is a
// missing row.
func LoadCells(dir string) ([]Cell, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "cells", "*.json"))
	if err != nil {
		return nil, fmt.Errorf("bench: %s: %w", dir, err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("bench: no cells in %s: nothing has been run there, or it is not a sweep directory", dir)
	}
	sort.Strings(paths)

	cells := make([]Cell, 0, len(paths))
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("bench: %w", err)
		}
		var cell Cell
		if err := json.Unmarshal(contents, &cell); err != nil {
			return nil, fmt.Errorf("bench: %s is not a cell record: %w", path, err)
		}
		cells = append(cells, cell)
	}
	return cells, nil
}
