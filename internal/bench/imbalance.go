package bench

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yuchia329/kvroute/internal/stats"
)

// PolicyImbalance is how unevenly one policy loaded the fleet at one load point,
// pooled over the repetitions that were fit to pool.
//
// It is the half of idea.md §5 the goodput table cannot show. Goodput says which
// policy won; the mechanism table says whether it won by keeping conversations
// warm; this says whether it lost by piling them onto one card. Those are the
// two things a consistent hash trades between, and a comparison that published
// only the first two would be unable to state the trade it exists to measure.
type PolicyImbalance struct {
	Policy      string
	Load        Load
	Repetitions int
	// BusiestShare and Spread are each the median across repetitions of that
	// repetition's own figure — the way the TTFT percentiles beside them are
	// pooled, and for the same reason. What the table is read for is what a
	// typical repetition did, and pooling two columns of one row two different
	// ways would stop them being commensurable.
	//
	// Not the busiest replica of the pooled repetitions, which is a different
	// and weaker quantity: three repetitions that each pinned a third of their
	// traffic to a different card would pool to a fleet that looks evenly
	// loaded, when what happened is that the fleet was badly loaded three times.
	//
	// Both medians are over every pooled repetition or over none. A repetition
	// left out of one of them and not the other would make the two columns of a
	// row describe different sets of runs, and the one that would be left out is
	// never a middling one: a repetition that served everything from a single
	// replica has no finite spread, so dropping it would take the most
	// imbalanced run out of the spread median and quietly pull it down.
	BusiestShare float64
	Spread       float64
	// SpreadDefined is false when some pooled repetition served everything from
	// one replica, and Spread is then not reported at all.
	//
	// There is no ratio for that repetition — a replica over itself is not a
	// measure of spread — and no median of the others would represent it. The
	// case is not lost, because it is the extreme of the column beside it: a
	// policy that put everything on one card reads 100% against its fair share,
	// which says more than any ratio could.
	SpreadDefined bool
	// Replicas is the most replicas any pooled repetition placed on, and
	// FairShare is one over it.
	//
	// The most rather than the median, because a replica that served nothing at
	// all appears in no row and cannot be counted (see Placement): the fleet is
	// at least as large as the largest number of replicas anything here reached.
	// Taking a repetition that reached fewer would raise the fair share and so
	// flatter every busiest share measured against it, which is the one
	// direction this figure must not err in.
	Replicas  int
	FairShare float64
	// Counted is false when the pooled figures cannot be read, for either of the
	// two reasons Uncounted tells apart. One spoiled repetition spoils the pool,
	// the way one unscraped repetition spoils the prefix cache hit rate: a
	// median over cells where some counted and some did not is a number nobody
	// can say what is behind.
	Counted bool
	// Uncounted is how many pooled repetitions predate the placement count
	// (#27), as against having counted and placed nothing.
	//
	// The two are different facts about a policy and the report states whichever
	// is true: cells recorded before 9311e68 have rows that were never counted
	// and can still be counted now, where a repetition that placed nothing
	// measured a fleet that refused every request. Collapsing them would have
	// the report tell a reader with perfectly good data to go and recount it.
	Uncounted int
}

// String is the policy's imbalance as a table cell.
func (i PolicyImbalance) String() string {
	if !i.Counted {
		return "—"
	}
	s := fmt.Sprintf("%.1f%% (fair %.1f%%)", i.BusiestShare*100, i.FairShare*100)
	if i.SpreadDefined {
		s += fmt.Sprintf(", %.2f×", i.Spread)
	}
	return s
}

// poolImbalance reduces a policy's repetitions at one load point to the figures
// the table publishes.
func poolImbalance(name string, load Load, cells []Cell) (PolicyImbalance, bool) {
	if len(cells) == 0 {
		return PolicyImbalance{}, false
	}
	// Reported even when unread. Returning nothing would leave the policy out of
	// the table as though it had never run at this load point, which is a
	// different statement from "its rows were never counted", and nothing
	// accumulated before the spoiling repetition is kept: a partly filled struct
	// that says Counted false would still read as a figure to anyone who printed
	// it.
	unread := func(uncounted int) PolicyImbalance {
		return PolicyImbalance{Policy: name, Load: load, Repetitions: len(cells), Uncounted: uncounted}
	}

	i := PolicyImbalance{Policy: name, Load: load, Repetitions: len(cells), SpreadDefined: true}
	shares := make([]float64, 0, len(cells))
	spreads := make([]float64, 0, len(cells))
	uncounted := 0
	for _, cell := range cells {
		if !cell.Placement.Counted {
			uncounted++
			continue
		}
		share, ok := cell.Placement.BusiestShare()
		if !ok {
			// Counted, and placed nothing. The pool has no typical repetition
			// once one of its runs served no request at all.
			return unread(0), true
		}
		shares = append(shares, share)
		if spread, ok := cell.Placement.Spread(); ok {
			spreads = append(spreads, spread)
		} else {
			i.SpreadDefined = false
		}
		i.Replicas = max(i.Replicas, cell.Placement.Replicas)
	}
	if uncounted > 0 {
		return unread(uncounted), true
	}

	slices.Sort(shares)
	slices.Sort(spreads)
	i.Counted = true
	i.BusiestShare = stats.Quantile(shares, 0.50)
	if i.SpreadDefined {
		i.Spread = stats.Quantile(spreads, 0.50)
	}
	if i.Replicas > 0 {
		i.FairShare = 1 / float64(i.Replicas)
	}
	return i, true
}

// reportImbalance renders the third table: how evenly each policy left the fleet
// loaded, counted from the rows.
//
// Its own section rather than more columns on the mechanism table, which already
// carries six figures per row. It is also a different mechanism: that table is
// about keeping conversations warm, and this is about not piling them onto one
// card. idea.md §5 predicts a policy can do the first and fail the second, which
// is the whole reason consistent hashing is the baseline worth beating, so the
// two belong beside each other and not in one another's columns.
func (c Comparison) reportImbalance(b *strings.Builder) {
	fmt.Fprintf(b, "\n## How evenly it loaded the fleet\n\n")
	fmt.Fprintf(b, "Counted from the rows — how many measured requests each replica served — and not read\n")
	fmt.Fprintf(b, "off the router's per-decision inflight column, which recorded zero on every\n")
	fmt.Fprintf(b, "session-affinity row ever written before 9311e68 (#27). Every row is counted whatever\n")
	fmt.Fprintf(b, "its outcome: a request a replica accepted and then failed still occupied it.\n\n")

	if !c.countedImbalance() {
		c.reportNothingCounted(b)
		return
	}

	fmt.Fprintf(b, "The busiest replica's share of the placed requests, against the fair share one replica\n")
	fmt.Fprintf(b, "would carry if the load were even, and the spread between the busiest and the quietest.\n")
	fmt.Fprintf(b, "Each is the median across a cell's repetitions of that repetition's own figure, pooled\n")
	fmt.Fprintf(b, "the way the percentiles above are. An em dash in the first two columns is a policy with\n")
	fmt.Fprintf(b, "a repetition that predates the count or that placed nothing, either of which makes the\n")
	fmt.Fprintf(b, "whole pooled figure unread; an em dash in the spread alone is a repetition that served\n")
	fmt.Fprintf(b, "everything from one replica, which has no ratio and is already the extreme of the column\n")
	fmt.Fprintf(b, "beside it.\n\n")
	fmt.Fprintf(b, "The replica count is the replicas that served at least one request, not the fleet: one\n")
	fmt.Fprintf(b, "that served nothing appears in no row and cannot be counted, so a row with fewer\n")
	fmt.Fprintf(b, "replicas than the fleet has understates its own imbalance and is a row to read twice.\n\n")

	fmt.Fprintln(b, "| driver | load | policy | busiest replica's share | fair share | busiest ÷ quietest | replicas |")
	fmt.Fprintln(b, "|---|---:|---|---:|---:|---:|---:|")
	for _, row := range c.Rows {
		for _, name := range c.Policies {
			// A policy with no usable cell here gets a gap row, not a missing
			// one. The section is read down the load axis beside the tables
			// above it, which print their own gaps, and a table that silently
			// dropped rows would stop lining up with them.
			imbalance, measured := row.Imbalance[name]
			if !measured || !imbalance.Counted {
				fmt.Fprintf(b, "| %s | %s | %s | — | — | — | — |\n", row.Load.Driver.Name(), row.Load, name)
				continue
			}
			spread := "—"
			if imbalance.SpreadDefined {
				spread = fmt.Sprintf("%.2f×", imbalance.Spread)
			}
			fmt.Fprintf(b, "| %s | %s | %s | %.1f%% | %.1f%% | %s | %d |\n",
				row.Load.Driver.Name(), row.Load, name,
				imbalance.BusiestShare*100, imbalance.FairShare*100, spread, imbalance.Replicas)
		}
	}
}

// reportNothingCounted says why there is no table, which is not always the same
// reason.
//
// Three things produce an empty section and they call for three different next
// steps: rows that were never counted can be counted now from the records on
// disk, a fleet that placed nothing needs re-running, and a load point where
// every cell was thrown away has already been listed as excluded. A single
// message would tell two of those three readers something false — and the one it
// would tell to go and recount already-counted rows is the reader whose data is
// fine.
func (c Comparison) reportNothingCounted(b *strings.Builder) {
	switch {
	case c.imbalancePredatesTheCount():
		fmt.Fprintf(b, "**Nothing here counted its placements.** Every policy in this table has a repetition\n")
		fmt.Fprintf(b, "recorded before 9311e68, so no imbalance figure can be pooled from it — and the inflight\n")
		fmt.Fprintf(b, "column those cells do carry reads zero throughout, which means \"not recorded\" and not\n")
		fmt.Fprintf(b, "\"idle\". Imbalance for these is still recoverable, by counting the per-request rows kept\n")
		fmt.Fprintf(b, "beside them.\n\n")
	case !c.anyImbalance():
		fmt.Fprintf(b, "**No usable cell reached this table.** Every cell was excluded or flagged, so there are\n")
		fmt.Fprintf(b, "no placements to pool. The exclusions are listed at the foot of this report.\n\n")
	default:
		fmt.Fprintf(b, "**Nothing here placed a request.** The placements were counted and came to nothing:\n")
		fmt.Fprintf(b, "these cells measured a fleet that served no request at all, which the goodput table\n")
		fmt.Fprintf(b, "above states directly. There is no imbalance to report in a fleet that took no load.\n\n")
	}
}

// countedImbalance reports whether anything in the comparison counted its
// placements, which is what separates a table worth drawing from a heading over
// a grid of dashes.
func (c Comparison) countedImbalance() bool {
	for _, row := range c.Rows {
		for _, imbalance := range row.Imbalance {
			if imbalance.Counted {
				return true
			}
		}
	}
	return false
}

// anyImbalance reports whether any policy was pooled here at all, counted or
// not. False means every cell was excluded, which is a different absence from
// cells that were read and had nothing to say.
func (c Comparison) anyImbalance() bool {
	for _, row := range c.Rows {
		if len(row.Imbalance) > 0 {
			return true
		}
	}
	return false
}

// imbalancePredatesTheCount reports whether any policy here was left unread by a
// repetition recorded before the placement count existed.
func (c Comparison) imbalancePredatesTheCount() bool {
	for _, row := range c.Rows {
		for _, imbalance := range row.Imbalance {
			if imbalance.Uncounted > 0 {
				return true
			}
		}
	}
	return false
}
