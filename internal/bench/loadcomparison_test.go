package bench_test

import (
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
)

// kept is a decision that took the best match, on a replica holding this much
// and a fleet in this state.
func kept(inflight, minimum int, mean float64) record.Request {
	return record.Request{
		DecisionReason:    string(policy.ReasonPrefixAffinity),
		Inflight:          inflight,
		FleetMinInflight:  minimum,
		FleetMeanInflight: mean,
		FleetLoadRead:     true,
	}
}

// spilled is a decision the load condition declined: the request went to a
// quieter replica, and the inflight the condition fired on is the declined
// replica's.
func spilled(declinedInflight, servedInflight, minimum int, mean float64) record.Request {
	row := kept(servedInflight, minimum, mean)
	row.DecisionReason = string(policy.ReasonSpillLoad)
	row.DeclinedInflight = declinedInflight
	return row
}

// The rung #31 found the degeneracy at: twelve requests over five replicas, so
// the quietest replica holds nothing or one and the average holds 2.4.
func quietRung() []record.Request {
	return []record.Request{
		kept(3, 0, 2.4), kept(2, 0, 2.4), kept(4, 1, 2.4),
		kept(6, 1, 2.6), kept(1, 0, 2.2), kept(3, 1, 2.4),
	}
}

// What the rows say the comparison was made against. The distribution of the
// fleet minimum is the whole mechanism: a denominator of one request makes
// "twice the fleet minimum" mean "more than two requests".
func TestTheComparisonReportsWhatItWasMadeAgainst(t *testing.T) {
	got := bench.MeasureLoadComparison(quietRung(), nil)

	if got.Weighed != 6 {
		t.Errorf("weighed %d decisions, want 6", got.Weighed)
	}
	if got.MinimumWasZero != 3 {
		t.Errorf("minimum was zero on %d decisions, want 3", got.MinimumWasZero)
	}
	if got.DenominatorWasOne != 6 {
		t.Errorf("denominator was one request on %d decisions, want all 6", got.DenominatorWasOne)
	}
	if !got.AnAbsoluteThreshold() {
		t.Error("a run whose every decision compared against a single request is reported as a ratio")
	}
}

// And the same report on a fleet loaded enough for the comparison to be the
// ratio it is written as.
func TestABusyFleetIsNotReportedAsAnAbsoluteThreshold(t *testing.T) {
	rows := []record.Request{
		kept(8, 3, 6.4), kept(11, 4, 6.4), kept(6, 3, 6.0),
		kept(9, 2, 6.2), kept(7, 5, 6.6), kept(12, 4, 6.4),
	}

	got := bench.MeasureLoadComparison(rows, nil)

	if got.DenominatorWasOne != 0 {
		t.Errorf("denominator was one request on %d decisions, want none", got.DenominatorWasOne)
	}
	if got.AnAbsoluteThreshold() {
		t.Error("a run whose quietest replica was always carrying work is reported as an absolute threshold")
	}
}

// A decision nobody held anything for had no match to decline, so the load
// condition was never evaluated on it and it is not evidence about the
// comparison. Counting cold requests in would report the rule as tested far
// more often than it was.
func TestARequestWithNoMatchIsNotADecisionTheConditionWeighed(t *testing.T) {
	rows := quietRung()
	cold := kept(0, 0, 2.4)
	cold.DecisionReason = string(policy.ReasonCold)

	got := bench.MeasureLoadComparison(append(rows, cold), nil)

	if got.Weighed != 6 {
		t.Errorf("weighed %d decisions, want the 6 that had a match on the table", got.Weighed)
	}
}

// Rows from a run that predates the columns are counted as unread rather than
// folded in as a fleet that was idle for every decision it made.
func TestRowsWithoutTheFleetsLoadAreUnreadRatherThanIdle(t *testing.T) {
	rows := append(quietRung(), record.Request{DecisionReason: string(policy.ReasonPrefixAffinity)})

	got := bench.MeasureLoadComparison(rows, nil)

	if got.Unread != 1 {
		t.Errorf("unread = %d, want 1", got.Unread)
	}
	if got.Weighed != 6 {
		t.Errorf("weighed %d decisions, want 6: a row with no fleet on it is not a decision made against an idle one", got.Weighed)
	}
	if got.MinimumWasZero != 3 {
		t.Errorf("minimum was zero on %d decisions, want 3", got.MinimumWasZero)
	}
}

// The projection the grid is cut from: what each candidate point would have
// declined over the decisions this run actually made.
func TestTheProjectionSaysWhatEachPointWouldHaveDeclined(t *testing.T) {
	points := []policy.Spill{
		{LoadImbalanceFactor: 2},
		{LoadImbalanceFactor: 2, MeanInflightDenominator: true},
	}

	got := bench.MeasureLoadComparison(quietRung(), points)

	if len(got.Projected) != 2 {
		t.Fatalf("projected %d points, want 2", len(got.Projected))
	}
	// Against the minimum, the threshold is 2 on every one of these decisions:
	// the replicas holding 3, 4, 6 and 3 are over it.
	if got.Projected[0].Declined != 4 {
		t.Errorf("the minimum denominator would have declined %d, want 4", got.Projected[0].Declined)
	}
	// Against the mean it is 4.4 to 5.2, and only the replica holding 6 is over
	// its own row's.
	if got.Projected[1].Declined != 1 {
		t.Errorf("the mean denominator would have declined %d, want 1", got.Projected[1].Declined)
	}
}

// A point that could not fire at this rung is not a grid point, it is a cell
// that produces no decision — which is what two of #16's three residency levels
// turned out to be. The projection is what says so before a run is spent.
func TestAPointThatCannotFireAtThisRungIsReportedAsUnreachable(t *testing.T) {
	points := []policy.Spill{{LoadImbalanceFactor: 32}}

	got := bench.MeasureLoadComparison(quietRung(), points)

	if got.Projected[0].Reaches() {
		t.Error("a factor of 32 is reported as reachable on a fleet whose busiest replica held 6")
	}
	if got.Projected[0].Declined != 0 {
		t.Errorf("declined %d, want none", got.Projected[0].Declined)
	}
}

// A run made with the rule already on records the inflight the condition fired
// on in the declined column, because the row's own inflight is the replica the
// request was moved to. Projecting off the wrong column would report the rule
// as barely firing on the very rows where it fired.
func TestTheProjectionReadsTheReplicaTheConditionWasEvaluatedAgainst(t *testing.T) {
	rows := []record.Request{
		spilled(6, 0, 0, 2.4),
		spilled(7, 1, 0, 2.4),
	}
	points := []policy.Spill{{LoadImbalanceFactor: 2}}

	got := bench.MeasureLoadComparison(rows, points)

	if got.Projected[0].Declined != 2 {
		t.Errorf("the point that produced these two spills projects %d, want 2", got.Projected[0].Declined)
	}
}

// The report is what a run is read off, and the figure it exists to put in
// front of a reader is how often the ratio was not one.
func TestTheReportNamesTheDenominatorAndWhatItCostTheRule(t *testing.T) {
	points := []policy.Spill{{LoadImbalanceFactor: 2}, {LoadImbalanceFactor: 2, MeanInflightDenominator: true}}

	report := bench.MeasureLoadComparison(quietRung(), points).Report()

	for _, want := range []string{"min", "mean", "6 decisions", "2.0x"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not mention %q:\n%s", want, report)
		}
	}
}

// Nothing to read is its own answer, and it is not a fleet that was idle.
func TestAReportOverNoWeighedDecisionsSaysSo(t *testing.T) {
	got := bench.MeasureLoadComparison(nil, []policy.Spill{{LoadImbalanceFactor: 2}})

	if got.AnAbsoluteThreshold() {
		t.Error("a report over no decisions claims the rule was an absolute threshold")
	}
	if !strings.Contains(got.Report(), "no ") {
		t.Errorf("the report does not say that nothing was read:\n%s", got.Report())
	}
}
