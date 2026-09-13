package bench

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RescoreReport renders a re-score as the markdown a measurement is published
// as.
//
// A transition table first, because the question the re-score exists to answer
// is how many recorded verdicts move and in which direction, and then the cells,
// named — every one of them when everyCell, and otherwise only those that moved.
func RescoreReport(scored []Rescored, threshold float64, everyCell bool) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Re-scored %d cells against a %.0f%% drift threshold.\n\n", len(scored), threshold*100)

	var open, closed []Rescored
	for _, r := range scored {
		if r.Driver == OpenLoopDriver {
			open = append(open, r)
		} else {
			closed = append(closed, r)
		}
	}
	writeDriverSection(&b, "Open-loop cells", open)
	writeDriverSection(&b, "Closed-loop cells", closed)
	writeTransitions(&b, scored)
	writeCells(&b, scored, everyCell)
	writeArithmeticCheck(&b, scored)

	return b.String()
}

func writeDriverSection(b *strings.Builder, title string, scored []Rescored) {
	if len(scored) == 0 {
		return
	}
	var wasFlagged, nowFlagged, changed int
	for _, r := range scored {
		if r.Was.WarmupDriftFlagged() {
			wasFlagged++
		}
		if r.Now.WarmupDriftFlagged() {
			nowFlagged++
		}
		if r.Changed() {
			changed++
		}
	}
	fmt.Fprintf(b, "## %s\n\n", title)
	fmt.Fprintf(b, "%d cells: %d were flagged by the drift check, %d are now, %d change verdict.\n\n",
		len(scored), wasFlagged, nowFlagged, changed)
}

// writeTransitions counts every (recorded verdict, current verdict) pair. The
// pair is the unit rather than a flagged/clear bit, because a cell that stays
// flagged for a different cause has still had its remedy changed — which is the
// half of this the acceptance criteria are about.
func writeTransitions(b *strings.Builder, scored []Rescored) {
	counts := map[[2]string]int{}
	for _, r := range scored {
		counts[[2]string{r.Was.WarmupDriftVerdict(), r.Now.WarmupDriftVerdict()}]++
	}
	keys := make([][2]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i][0]+keys[i][1] < keys[j][0]+keys[j][1]
	})

	fmt.Fprintln(b, "## Verdict transitions")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| recorded verdict | current verdict | cells |")
	fmt.Fprintln(b, "|---|---|---:|")
	for _, k := range keys {
		fmt.Fprintf(b, "| %s | %s | %d |\n", k[0], k[1], counts[k])
	}
	fmt.Fprintln(b)
}

// writeCells lists the cells one row each. Only the cells whose verdict moved,
// unless everyCell: 216 rows of "unchanged" would bury the twenty-odd that are
// the result, and a measurement that needs all of them says so.
func writeCells(b *strings.Builder, scored []Rescored, everyCell bool) {
	if everyCell {
		fmt.Fprintln(b, "## Every cell")
	} else {
		fmt.Fprintln(b, "## Cells whose verdict changed")
	}
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| run | cell | rate | think | periods | recorded drift | current drift | basis | compared | confined | recorded verdict | current verdict |")
	fmt.Fprintln(b, "|---|---|---:|---:|---:|---:|---:|---|---:|---:|---|---|")
	var changed int
	for _, r := range scored {
		if r.Changed() {
			changed++
		} else if !everyCell {
			continue
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %+.3f | %+.3f | %s | %d | %d | %s | %s |\n",
			r.Dir, r.Cell, rateOf(r), thinkOf(r), periodsOf(r),
			r.Was.WarmupDrift, r.Now.WarmupDrift,
			basisOf(r.Now), r.Now.WarmupDriftTurnsCompared, r.Now.WarmupDriftTurnsConfined,
			r.Was.WarmupDriftVerdict(), r.Now.WarmupDriftVerdict())
	}
	if changed == 0 && !everyCell {
		fmt.Fprintln(b, "| — | no cell changed verdict | | | | | | | | | | |")
	}
	fmt.Fprintf(b, "\n%d of %d cells changed verdict.\n\n", changed, len(scored))
}

// writeArithmeticCheck is the re-score auditing itself. Everything but the drift
// fields is recomputed from the same rows by the same code that first wrote it,
// so it must come back identical; a cell where it does not is a row file that
// does not match its record, and no verdict read off it means anything.
func writeArithmeticCheck(b *strings.Builder, scored []Rescored) {
	var mismatched []Rescored
	for _, r := range scored {
		if !r.Reproduces() {
			mismatched = append(mismatched, r)
		}
	}
	fmt.Fprintln(b, "## Re-score integrity")
	fmt.Fprintln(b)
	if len(mismatched) == 0 {
		fmt.Fprintf(b, "Every one of the %d cells reproduced its recorded requests, successes, window, goodput and TTFT percentiles from its own rows. Only the drift fields moved.\n\n", len(scored))
		return
	}
	fmt.Fprintf(b, "**%d of %d cells did not reproduce their recorded summary from their rows**, so their verdicts are not comparable:\n\n", len(mismatched), len(scored))
	for _, r := range mismatched {
		fmt.Fprintf(b, "- `%s` in %s: recorded %d requests / %d successes / window %v / TTFT p50 %s, rows give %d / %d / %v / %s\n",
			r.Cell, r.Dir,
			r.Was.Requests, r.Was.Successes, time.Duration(r.Was.WindowNs).Round(time.Millisecond), ms(r.Was.TTFTP50Ns),
			r.Now.Requests, r.Now.Successes, time.Duration(r.Now.WindowNs).Round(time.Millisecond), ms(r.Now.TTFTP50Ns))
	}
	fmt.Fprintln(b)
}

func rateOf(r Rescored) string {
	if r.ArrivalRate == 0 {
		return "—"
	}
	return fmt.Sprintf("%g/s", r.ArrivalRate)
}

func thinkOf(r Rescored) string {
	if r.ThinkTime == 0 {
		return "—"
	}
	return r.ThinkTime.String()
}

func periodsOf(r Rescored) string {
	if r.VisitPeriod == 0 {
		return "—"
	}
	return fmt.Sprintf("%.2f", r.PeriodsMeasured)
}

func basisOf(s Summary) string {
	if s.WarmupDriftBasis == "" {
		return "—"
	}
	return s.WarmupDriftBasis
}
