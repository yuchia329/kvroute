package bench

import (
	"fmt"
	"strings"

	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
)

// What the spill rule's load condition was really testing over a run, and what
// each candidate grid point would have done there.
//
// The condition is written as a ratio — decline the best match when its replica
// is carrying more than a factor times the fleet's minimum inflight — and a
// ratio is what lets one factor settled at one rung describe the rule at
// another. #31 found that it is not always a ratio. At 6 requests per second
// over five replicas the fleet holds six requests at the median and fourteen at
// its busiest, the quietest replica holds nothing or one, and a factor of 2
// becomes "more than two requests":
// 19–20% of later turns were declined, against 0.674% at the 32-user rung the
// factor was settled at, and the declined turns found 11.9% of their prompt
// cached where the turns that stayed found 73.9%.
//
// Nothing in a cell said so, and nothing could have: the rows carried the chosen
// replica's inflight and not the fleet's, so the denominator of every comparison
// the rule made was the one number a run did not record. It does now
// (record.Request.FleetMinInflight), and this reads it back.
//
// Two things come off it, and they are the two halves of choosing a threshold:
//
//  1. WHAT THE COMPARISON WAS. How often the denominator was a single request,
//     so the factor was an absolute inflight count rather than a multiple of
//     anything. A run where that is most decisions was not running the rule the
//     factor describes, whatever its cells are labelled.
//
//  2. WHAT EACH POINT WOULD HAVE DONE. Every weighed decision carries the
//     inflight the condition was evaluated against and both candidate
//     denominators, so the share a point would have declined is arithmetic over
//     rows rather than another run. This is how a grid gets cut against an
//     observed range instead of a plausible one — HitRateLowWaterGrid's
//     discipline, applied to the axis that already has levels.
//
// The projection is a projection and not a prediction, and the distinction
// matters enough to state: a spill that had happened would have moved the
// request elsewhere and changed the fleet every later decision saw. It says what
// the rule would have declined over the fleet this run actually produced, which
// is the right question for reachability — can this point fire at this rung at
// all — and the wrong one for goodput, which only a run at the point can answer.

// LoadComparison is what one set of router rows says about the comparison the
// spill rule's load condition made, and about the points it could be made at.
type LoadComparison struct {
	// Rows is how many rows were read, and Unread how many carried no fleet load
	// at all — a run from before the columns existed, whose zeros are absences
	// rather than an idle fleet.
	Rows   int `json:"rows"`
	Unread int `json:"unread"`
	// Weighed is the decisions the load condition was evaluated on: the ones
	// with a match on the table to decline. A cold request had none, so the
	// condition never ran on it and it is not evidence about the comparison.
	Weighed int `json:"weighed"`
	// MinimumWasZero is how many of those saw a fleet with an idle replica in
	// it, and DenominatorWasOne how many compared against a single request —
	// whether because the floor bound or because the quietest replica genuinely
	// held one. The second is the larger number and the one that matters: at a
	// denominator of one, "factor times the minimum" is "factor requests".
	//
	// MinimumWasZero is kept beside it because it is the figure the settled rung
	// has on record — SettledRungMinimumWasZero, from #16's c32 rows — and a
	// comparison needs the two numbers to be the same number.
	//
	// Both count the rule as it stands, against the fleet minimum. What the mean
	// denominator would have compared against is a different number on the same
	// rows, and it is read off Projected rather than here.
	MinimumWasZero    int `json:"minimum_was_zero"`
	DenominatorWasOne int `json:"denominator_was_one"`
	// Busiest is the largest inflight any weighed decision was evaluated
	// against. A factor whose threshold is never reached cannot fire, and this
	// is what a reader checks a grid's top end against.
	Busiest int `json:"busiest"`
	// Projected is what each candidate point would have declined over these
	// rows, in the order the points were given.
	Projected []ProjectedPoint `json:"projected"`
}

// ProjectedPoint is one grid point held against a run that did not run it.
type ProjectedPoint struct {
	Point policy.Spill `json:"point"`
	// Declined is how many weighed decisions this point's load condition would
	// have declined, and Weighed how many it was held against.
	Declined int `json:"declined"`
	Weighed  int `json:"weighed"`
}

// Share is the fraction of weighed decisions this point would have declined.
func (p ProjectedPoint) Share() float64 {
	if p.Weighed == 0 {
		return 0
	}
	return float64(p.Declined) / float64(p.Weighed)
}

// Reaches reports whether this point can fire at this rung at all.
//
// The same criterion Range.Reaches applies to the residency mark, and it exists
// for the same reason: #16 swept three levels of which two could not fire at the
// concurrency they ran at, and the sweep read that as three measurements. A
// point that declines nothing is not a conservative setting, it is a cell with
// no decision in it — as a factor of 32 turned out to be at c32, where the ratio
// never exceeded 29 and the cell was indistinguishable from having no rule.
func (p ProjectedPoint) Reaches() bool { return p.Declined > 0 }

// AnAbsoluteThreshold reports whether the comparison these rows made was mostly
// not a ratio: more than half the weighed decisions compared against a single
// request.
//
// A majority rather than a tuned share, because there is nothing to tune it
// against and the claim is qualitative: past this line, what the run measured is
// mostly "decline any replica holding more than <factor> requests", and the
// factor a cell is labelled with describes a different rule from the one that
// ran. A run under the line still has floored decisions in it — the settled rung
// had SettledRungMinimumWasZero of them — and the count is what says how many.
//
// False over no decisions. Nothing was measured, which is not evidence that the
// comparison held.
func (c LoadComparison) AnAbsoluteThreshold() bool {
	return c.Weighed > 0 && c.DenominatorWasOne*2 > c.Weighed
}

// Share is the fraction of weighed decisions that compared against a single
// request.
func (c LoadComparison) Share() float64 {
	if c.Weighed == 0 {
		return 0
	}
	return float64(c.DenominatorWasOne) / float64(c.Weighed)
}

// MeasureLoadComparison reads the load condition's comparison off a run's rows,
// and projects each of the given points over them. Pass no points to read the
// comparison alone.
func MeasureLoadComparison(rows []record.Request, points []policy.Spill) LoadComparison {
	c := LoadComparison{Rows: len(rows), Projected: make([]ProjectedPoint, len(points))}
	for i, point := range points {
		c.Projected[i].Point = point
	}

	for _, row := range rows {
		if !row.FleetLoadRead {
			c.Unread++
			continue
		}
		inflight, weighed := weighedInflight(row)
		if !weighed {
			continue
		}
		c.Weighed++
		if row.FleetMinInflight == 0 {
			c.MinimumWasZero++
		}
		// The denominator of the rule as it stands — the minimum, floored — and
		// only that one. The mean is floored the same way but is a different
		// number on the same row, and how often it came to one is read off the
		// projection rather than counted here: this column is about the rule the
		// run was making decisions with.
		if max(row.FleetMinInflight, policy.MinimumInflightFloor) == policy.MinimumInflightFloor {
			c.DenominatorWasOne++
		}
		c.Busiest = max(c.Busiest, inflight)

		for i := range c.Projected {
			c.Projected[i].Weighed++
			if c.Projected[i].Point.DeclinesLoad(inflight, row.FleetMinInflight, row.FleetMeanInflight) {
				c.Projected[i].Declined++
			}
		}
	}
	return c
}

// weighedInflight is the inflight the load condition was evaluated against on
// this row, and whether the condition was evaluated at all.
//
// Two columns, because the answer moves depending on what the rule did with it.
// A kept match records the chosen replica's inflight, and that replica is the
// one the condition weighed. A spill records the inflight of the replica it
// moved the request *to* — the declined one's is in its own column, and it is
// the number the condition actually fired on. Reading the wrong column would
// report a point as barely firing on the very rows where it fired.
//
// A cold request is not weighed: no replica held anything, so there was no match
// to decline and the condition never ran. Neither is a decision made by a policy
// with no spill rule, which reaches here only if rows from several policies were
// read at once.
func weighedInflight(row record.Request) (int, bool) {
	switch policy.Reason(row.DecisionReason) {
	case policy.ReasonPrefixAffinity:
		return row.Inflight, true
	case policy.ReasonSpillLoad, policy.ReasonSpillHitRate:
		return row.DeclinedInflight, true
	default:
		return 0, false
	}
}

// Report renders what the rows said, in the form #31 is read off: what the rule
// was comparing against, and what each candidate point would have declined.
func (c LoadComparison) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# What the load condition compared against\n\n")

	if c.Weighed == 0 {
		switch {
		case c.Rows > 0 && c.Unread == c.Rows:
			fmt.Fprintf(&b, "None of these %d rows carries the fleet it was decided against, so nothing can\n"+
				"be said about what the load condition was comparing. That is a run from before\n"+
				"the columns existed (#31) rather than a fleet that was idle: the figure the rule\n"+
				"divided by is exactly the one those runs did not record, which is why the\n"+
				"degeneracy had to be inferred from its consequences. Re-run the rung to read it.\n",
				c.Rows)
		default:
			fmt.Fprintf(&b, "%d rows, and no decision among them had a match on the table for the load\n"+
				"condition to decline — so either these rows are a policy with no spill rule, or\n"+
				"the index found nothing for the whole run. %d of them carried no fleet load at\n"+
				"all.\n", c.Rows, c.Unread)
		}
		return b.String()
	}

	fmt.Fprintf(&b, "%d rows, %d decisions the condition was evaluated on", c.Rows, c.Weighed)
	if c.Unread > 0 {
		fmt.Fprintf(&b, ", %d carrying no fleet load", c.Unread)
	}
	fmt.Fprintf(&b, ".\n\n")

	fmt.Fprintf(&b, "| what the comparison was made against | decisions | share |\n")
	fmt.Fprintf(&b, "|---|---:|---:|\n")
	fmt.Fprintf(&b, "| a single request (the factor is an absolute count) | %d | %.1f%% |\n",
		c.DenominatorWasOne, 100*c.Share())
	fmt.Fprintf(&b, "| an idle replica, floored to one | %d | %.1f%% |\n",
		c.MinimumWasZero, 100*float64(c.MinimumWasZero)/float64(c.Weighed))
	fmt.Fprintf(&b, "\nThe busiest replica any of these decisions weighed held %d requests.\n\n", c.Busiest)

	if c.AnAbsoluteThreshold() {
		fmt.Fprintf(&b, "**Most of this run's comparisons were not ratios.** Past half the decisions, a\n"+
			"cell labelled with a factor is measuring \"decline any replica holding more than\n"+
			"that many requests\", which is a different rule at every rung — and the factor was\n"+
			"settled at one rung (#18, c32, where the minimum was genuinely 0 in %.0f%% of\n"+
			"decisions). See #31.\n\n", 100*SettledRungMinimumWasZero)
	} else {
		fmt.Fprintf(&b, "Most of this run's comparisons were ratios: the quietest replica was carrying\n"+
			"work, so the factor asked for a multiple of something. The settled rung had the\n"+
			"minimum genuinely 0 in %.0f%% of decisions (#18, c32) for comparison.\n\n",
			100*SettledRungMinimumWasZero)
	}

	if len(c.Projected) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "## What each point would have declined\n\n")
	fmt.Fprintf(&b, "| factor | against | would decline | share | reaches |\n")
	fmt.Fprintf(&b, "|---|---|---:|---:|---|\n")
	for _, p := range c.Projected {
		if p.Point.LoadImbalanceFactor <= 0 {
			// The spill-off reference, which every sweep is led by. It declines
			// nothing because it is not a threshold, and rendering that as a
			// point that could not fire would read as a finding about the rung.
			fmt.Fprintf(&b, "| off | — | 0 | 0.0%% | the reference the others are read against |\n")
			continue
		}
		reaches := "yes"
		if !p.Reaches() {
			reaches = "**no — cannot fire at this rung**"
		}
		fmt.Fprintf(&b, "| %.1fx | %s | %d | %.1f%% | %s |\n",
			p.Point.LoadImbalanceFactor, p.Point.LoadDenominatorName(), p.Declined, 100*p.Share(), reaches)
	}
	fmt.Fprintf(&b, "\nProjected over the fleet this run produced, not predicted: a decline that had\n"+
		"happened would have moved the request and changed what every later decision saw.\n"+
		"It answers which points can fire at this rung, which is what the grid is cut\n"+
		"from. What they are worth is a goodput question and needs the run.\n")
	return b.String()
}
