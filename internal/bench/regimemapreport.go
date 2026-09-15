package bench

import (
	"fmt"
	"strings"
)

// Report renders the regime map as markdown: the headline Y×X grid of
// winners, the full goodput behind it, what was surfaced or left out, and the
// points that contest the project's own challenger.
//
// It reuses the pressure map's own words and helpers — figureWithSpread and
// reportSurfaced — rather than a second rendering of them, for the reason the
// pressure map's own report gives: two renderings of one rule are two places
// for "within spread" to come to mean something slightly different.
func (m RegimeMap) Report() string {
	var b strings.Builder

	fmt.Fprintf(&b, "# The regime map — which policy wins at each recorded point\n\n")
	fmt.Fprintf(&b, "SLO: TTFT < %v, inter-token p50 < %v. Goodput is requests per second that met it.\n\n", m.SLO.TTFT, m.SLO.ITL)
	fmt.Fprintf(&b, "Each tile names the policy with the highest pooled goodput at that point and its margin\n")
	fmt.Fprintf(&b, "over the runner-up, qualified the way the pressure map qualifies its own deltas: a margin\n")
	fmt.Fprintf(&b, "marked *within spread* is smaller than the run-to-run range behind it, which is a\n")
	fmt.Fprintf(&b, "difference between a policy and itself. Nothing here is a new reduction of cells — every\n")
	fmt.Fprintf(&b, "figure is read off the comparison already built, so pooling, exclusion and the one-SLO\n")
	fmt.Fprintf(&b, "check are inherited rather than repeated.\n\n")

	m.reportHeadline(&b)
	m.reportDetail(&b)
	reportSurfaced(&b, m.Surfaced)
	m.reportGaps(&b)
	m.reportContested(&b)
	return b.String()
}

// reportHeadline renders the Y×X grid of winners: one tile per cell, the
// winner's name and its margin over the runner-up.
func (m RegimeMap) reportHeadline(b *strings.Builder) {
	fmt.Fprintf(b, "## The map — winner and margin\n\n")

	fmt.Fprintf(b, "| %s \\ %s |", m.YAxis.Label, m.XAxis.Label)
	for _, x := range m.XAxis.Values {
		fmt.Fprintf(b, " %s |", x)
	}
	fmt.Fprintln(b)
	fmt.Fprintf(b, "|---|")
	for range m.XAxis.Values {
		fmt.Fprintf(b, "---|")
	}
	fmt.Fprintln(b)

	for _, y := range m.YAxis.Values {
		fmt.Fprintf(b, "| **%s** |", y)
		for _, x := range m.XAxis.Values {
			tile, found := m.tileAt(y, x)
			if !found {
				fmt.Fprintf(b, " not run |")
				continue
			}
			fmt.Fprintf(b, " %s |", tile.headlineCell())
		}
		fmt.Fprintln(b)
	}
	fmt.Fprintln(b)
}

// reportDetail renders the goodput each policy actually scored behind the
// headline, the way the pressure map's own detail table does — one row per
// tile, one column per policy, with the repetition spread beneath the median.
func (m RegimeMap) reportDetail(b *strings.Builder) {
	fmt.Fprintf(b, "## Goodput behind the map\n\n")
	fmt.Fprintf(b, "Each figure is the median of that point's repetitions with the range across them, and the\n")
	fmt.Fprintf(b, "spread beneath it as a share of that median. An em dash is a policy with no usable cell at\n")
	fmt.Fprintf(b, "that point, which is not a zero.\n\n")

	fmt.Fprintf(b, "| %s | %s | load |", m.YAxis.Label, m.XAxis.Label)
	for _, name := range m.Policies {
		fmt.Fprintf(b, " %s |", name)
	}
	fmt.Fprintln(b)
	fmt.Fprintf(b, "|---|---|---|")
	for range m.Policies {
		fmt.Fprintf(b, "---:|")
	}
	fmt.Fprintln(b)

	for _, t := range m.Tiles {
		fmt.Fprintf(b, "| %s | %s | %s |", t.Y, t.X, t.LoadLabel)
		for _, name := range m.Policies {
			fmt.Fprintf(b, " %s |", figureWithSpread(t.Goodput, name))
		}
		fmt.Fprintln(b)
	}
	fmt.Fprintln(b)
}

// reportGaps lists everything the map does not rest on: the pressure map's
// own four categories, carried onto the regime map rather than re-derived.
func (m RegimeMap) reportGaps(b *strings.Builder) {
	if len(m.Missing) == 0 && len(m.Refused) == 0 && len(m.Excluded) == 0 {
		return
	}
	fmt.Fprintf(b, "## What this map does not rest on\n\n")

	if len(m.Missing) > 0 {
		fmt.Fprintf(b, "Points with no cells at all — the run is this much short of complete:\n\n")
		for _, s := range m.Missing {
			fmt.Fprintf(b, "- %s\n", s)
		}
		fmt.Fprintln(b)
	}
	if len(m.Refused) > 0 {
		fmt.Fprintf(b, "Points that ran but could not be compared:\n\n")
		for _, s := range m.Refused {
			fmt.Fprintf(b, "- %s\n", s)
		}
		fmt.Fprintln(b)
	}
	if len(m.Excluded) > 0 {
		fmt.Fprintf(b, "Cells excluded from every figure above — §6 discards these rather than averaging them in:\n\n")
		for _, s := range m.Excluded {
			fmt.Fprintf(b, "- %s\n", s)
		}
		fmt.Fprintln(b)
	}
}

// reportContested names the points where a policy other than prefix affinity
// won by more than the run-to-run spread — the tiles that argue against the
// project's own challenger rather than for it.
func (m RegimeMap) reportContested(b *strings.Builder) {
	fmt.Fprintf(b, "## Contested — where a policy beats prefix affinity beyond the spread\n\n")
	contested := m.Contested()
	if len(contested) == 0 {
		fmt.Fprintf(b, "No point contests prefix affinity beyond the spread.\n")
		return
	}
	for _, t := range contested {
		fmt.Fprintf(b, "- %s %s, %s %s: %s beats the runner-up %s by %s\n", m.YAxis.Label, t.Y, m.XAxis.Label, t.X, t.Winner, t.RunnerUp, t.Label)
	}
}
