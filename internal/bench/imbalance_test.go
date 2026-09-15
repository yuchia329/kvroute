package bench_test

import (
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// placed gives a cell the placements it would have counted had its busiest
// replica taken `busiest` of `total` requests over `replicas` replicas, with the
// quietest taking the rest evenly. The figures are chosen per test so the
// arithmetic is checkable by hand.
func placed(c bench.Cell, replicas, total, busiest, quietest int) bench.Cell {
	c.Placement = bench.Placement{
		Counted:          true,
		Requests:         total,
		Replicas:         replicas,
		Busiest:          "replica-0",
		BusiestRequests:  busiest,
		Quietest:         "replica-4",
		QuietestRequests: quietest,
	}
	return c
}

// The imbalance section is what the inflight column was supposed to be, and the
// comparison is where §5's claim is published. A report that measured which
// policy won but not how evenly it spread the fleet could not say why.
func TestTheComparisonReportsHowEvenlyEachPolicyLoadedTheFleet(t *testing.T) {
	load := bench.ClosedLoopAt(64)
	var cs []bench.Cell
	// Session affinity pins conversations, so its busiest replica takes well
	// over its fair fifth: 338 of 1,000 against a quietest 200, a spread of 1.69.
	for i, busiest := range []int{330, 338, 350} {
		cs = append(cs, placed(cell(policy.SessionAffinityName, load, i+1, 7), 5, 1000, busiest, 200))
	}
	// Prefix affinity spills, so it sits near fair share: 224 against 200.
	for i, busiest := range []int{220, 224, 230} {
		cs = append(cs, placed(cell(policy.PrefixAffinityName, load, i+1, 15), 5, 1000, busiest, 200))
	}

	got := compare(t, cs)
	row := got.Rows[0]

	session := row.Imbalance[policy.SessionAffinityName]
	if !session.Counted {
		t.Fatal("session affinity's imbalance is unread, though every repetition counted its placements")
	}
	if session.BusiestShare != 0.338 {
		t.Errorf("session affinity busiest share = %v, want 0.338: the median of 0.330, 0.338 and 0.350", session.BusiestShare)
	}
	if session.Spread != 1.69 {
		t.Errorf("session affinity spread = %v, want 1.69", session.Spread)
	}
	if session.FairShare != 0.2 || session.Replicas != 5 {
		t.Errorf("fair share = %v over %d replicas, want 0.2 over 5", session.FairShare, session.Replicas)
	}

	prefix := row.Imbalance[policy.PrefixAffinityName]
	if prefix.BusiestShare != 0.224 {
		t.Errorf("prefix affinity busiest share = %v, want 0.224", prefix.BusiestShare)
	}
	if prefix.Spread != 1.12 {
		t.Errorf("prefix affinity spread = %v, want 1.12", prefix.Spread)
	}

	report := got.Report()
	if !strings.Contains(report, "How evenly it loaded the fleet") {
		t.Errorf("the report has no imbalance section:\n%s", report)
	}
	if !strings.Contains(report, "1.69×") || !strings.Contains(report, "1.12×") {
		t.Errorf("the report does not carry both policies' spreads:\n%s", report)
	}
}

// One repetition recorded before #27 makes the whole pooled figure unread, the
// way one unscraped repetition makes the prefix cache hit rate unread. A median
// over cells where some counted their placements and some did not is a number
// nobody can say what is behind.
func TestOneUncountedRepetitionLeavesThePolicysImbalanceUnread(t *testing.T) {
	load := bench.ClosedLoopAt(64)
	cs := []bench.Cell{
		placed(cell(policy.SessionAffinityName, load, 1, 7), 5, 1000, 338, 200),
		placed(cell(policy.SessionAffinityName, load, 2, 7), 5, 1000, 338, 200),
		// Recorded before the count existed: no placement block at all.
		cell(policy.SessionAffinityName, load, 3, 7),
	}
	cs = append(cs, placed(cell(policy.PrefixAffinityName, load, 1, 15), 5, 1000, 224, 200))

	got := compare(t, cs)

	session := got.Rows[0].Imbalance[policy.SessionAffinityName]
	if session.Counted {
		t.Errorf("session affinity's imbalance reads as counted (%+v), though one repetition predates the count", session)
	}
	if !strings.Contains(got.Report(), "| session_affinity | — | — | — | — |") {
		t.Errorf("the uncounted policy's row is not all em dashes:\n%s", got.Report())
	}
	// The policy that did count still reports, so one unread policy does not
	// take the section down with it.
	if !strings.Contains(got.Report(), "| prefix_affinity | 22.4% |") {
		t.Errorf("the counted policy lost its figures to the uncounted one:\n%s", got.Report())
	}
}

// A whole comparison of cells recorded before #27 has no imbalance to report,
// and the section has to say that rather than print a table of dashes over a
// heading that promises a measurement.
func TestAComparisonOfCellsThatPredateTheCountSaysSoInsteadOfDrawingTheTable(t *testing.T) {
	load := bench.ClosedLoopAt(64)
	cs := cells(policy.SessionAffinityName, load, 7, 7, 7)
	cs = append(cs, cells(policy.PrefixAffinityName, load, 15, 15, 15)...)

	report := compare(t, cs).Report()

	if !strings.Contains(report, "How evenly it loaded the fleet") {
		t.Fatalf("the imbalance section is missing entirely:\n%s", report)
	}
	if !strings.Contains(report, "9311e68") {
		t.Errorf("the section does not say these cells predate the count:\n%s", report)
	}
	if strings.Contains(report, "| busiest replica's share |") {
		t.Errorf("a table was drawn over cells that counted nothing:\n%s", report)
	}
}

// A repetition that served everything from one replica has no spread — a
// replica over itself is not one — and the median of the repetitions that do
// would leave the most imbalanced run out of the figure that exists to show
// imbalance. The share column carries that case instead, at its maximum.
func TestOneRepetitionOnASingleReplicaLeavesNoSpreadToPool(t *testing.T) {
	load := bench.ClosedLoopAt(64)
	cs := []bench.Cell{
		placed(cell(policy.SessionAffinityName, load, 1, 7), 5, 1000, 338, 200),
		placed(cell(policy.SessionAffinityName, load, 2, 7), 5, 1000, 338, 200),
		// Everything on one replica: one replica, so no spread.
		placed(cell(policy.SessionAffinityName, load, 3, 7), 1, 1000, 1000, 1000),
	}
	cs = append(cs, placed(cell(policy.PrefixAffinityName, load, 1, 15), 5, 1000, 224, 200))

	got := compare(t, cs)
	session := got.Rows[0].Imbalance[policy.SessionAffinityName]

	if !session.Counted {
		t.Fatal("the policy is unread, though every repetition counted its placements")
	}
	if session.SpreadDefined || session.Spread != 0 {
		t.Errorf("a spread of %v was pooled (defined=%v), though one repetition had no two replicas to spread between",
			session.Spread, session.SpreadDefined)
	}
	// The share is still pooled, and the single-replica repetition is the
	// largest of the three: the median of 0.338, 0.338 and 1.000.
	if session.BusiestShare != 0.338 {
		t.Errorf("busiest share = %v, want the median 0.338", session.BusiestShare)
	}
	if !strings.Contains(got.Report(), "| session_affinity | 33.8% | 20.0% | — | 5 |") {
		t.Errorf("the row does not dash the spread alone:\n%s", got.Report())
	}
}

// A policy that never ran at a load point gets a gap row, the way the tables
// above it print gaps. Dropping the row instead would make this section stop
// lining up with them as the reader goes down the load axis.
func TestAPolicyWithNoUsableCellAtALoadPointStillGetsARow(t *testing.T) {
	var cs []bench.Cell
	for _, load := range []bench.Load{bench.ClosedLoopAt(32), bench.ClosedLoopAt(64)} {
		cs = append(cs, placed(cell(policy.SessionAffinityName, load, 1, 7), 5, 1000, 338, 200))
	}
	// Prefix affinity ran only at 32: it did not score nothing at 64, it did
	// not run there.
	cs = append(cs, placed(cell(policy.PrefixAffinityName, bench.ClosedLoopAt(32), 1, 15), 5, 1000, 224, 200))

	report := compare(t, cs).Report()

	if !strings.Contains(report, "| 64 users | prefix_affinity | — | — | — | — |") {
		t.Errorf("the policy that did not reach 64 users has no gap row:\n%s", report)
	}
}

// The banner says why there is no table, and the two reasons call for opposite
// next steps. A cell whose rows were counted and came to nothing must not be
// reported as one nobody counted, or the reader is sent to recount rows that
// are already counted.
func TestCellsThatCountedAndPlacedNothingAreNotBlamedOnTheCountBeingNew(t *testing.T) {
	load := bench.ClosedLoopAt(64)
	var cs []bench.Cell
	for _, name := range []string{policy.SessionAffinityName, policy.PrefixAffinityName} {
		c := cell(name, load, 1, 0)
		// Counted, and every request refused: the fleet placed nothing.
		c.Placement = bench.Placement{Counted: true}
		cs = append(cs, c)
	}

	report := compare(t, cs).Report()

	if strings.Contains(report, "9311e68, so no imbalance figure") {
		t.Errorf("counted cells are blamed on predating the count:\n%s", report)
	}
	if !strings.Contains(report, "Nothing here placed a request") {
		t.Errorf("the section does not say the fleet took no load:\n%s", report)
	}
}
