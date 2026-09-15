package bench_test

import (
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// A cell has to say how its arrivals were mapped onto conversations, because the
// workload name cannot. The workload says what bytes a (user, turn) pair renders
// to; the arrival plan says which pair each arrival took. Two cells can share the
// first and differ in the second.
func TestAnOpenLoopCellRecordsHowItsArrivalsWereMapped(t *testing.T) {
	if bench.ArrivalPlan == "" {
		t.Fatal("the arrival plan is unnamed, so a cell cannot record it")
	}
	c := cell(policy.RoundRobinName, bench.OpenLoopAt(8), 1, 5.0)
	c.ArrivalPlan = bench.ArrivalPlan
	if c.ArrivalPlan == "" {
		t.Error("a cell cannot carry an arrival plan")
	}
}

// The reason it is recorded: cells from before the rotation was shuffled and
// cells from after it send different traffic under one workload name, and a
// comparison that pooled them would report the difference between two drivers as
// a difference between two policies. On 2026-09-08 the fixed rotation handed
// round-robin every turn of a conversation on one replica and a prefix cache hit
// rate it had not earned, so this is the exact pair that must not be pooled.
func TestCellsFromTwoArrivalPlansAreRefusedRatherThanPooled(t *testing.T) {
	at8 := bench.OpenLoopAt(8)
	fixed := cell(policy.RoundRobinName, at8, 1, 5.0)
	fixed.ArrivalPlan = "" // a cell recorded before the plan was named
	shuffled := cell(policy.LeastOutstandingName, at8, 1, 9.0)
	shuffled.ArrivalPlan = bench.ArrivalPlan

	_, err := bench.Compare([]bench.Cell{fixed, shuffled})
	if err == nil {
		t.Fatal("two arrival plans were pooled into one comparison")
	}
	if !strings.Contains(err.Error(), "arrival plan") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// Cells sharing a plan compare as they always did.
func TestOneArrivalPlanComparesNormally(t *testing.T) {
	at8 := bench.OpenLoopAt(8)
	a := cell(policy.RoundRobinName, at8, 1, 5.0)
	b := cell(policy.LeastOutstandingName, at8, 1, 9.0)
	a.ArrivalPlan, b.ArrivalPlan = bench.ArrivalPlan, bench.ArrivalPlan

	if _, err := bench.Compare([]bench.Cell{a, b}); err != nil {
		t.Fatalf("cells of one arrival plan were refused: %v", err)
	}
}

// Closed-loop cells have no pool and no rotation — their virtual users walk the
// workload directly — so they carry no plan, and a table of them must not be
// refused for it.
func TestClosedLoopCellsCarryNoPlanAndStillCompare(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	a := cell(policy.RoundRobinName, at8, 1, 5.0)
	b := cell(policy.LeastOutstandingName, at8, 1, 9.0)

	if a.ArrivalPlan != "" || b.ArrivalPlan != "" {
		t.Fatal("a closed-loop cell was given an arrival plan")
	}
	if _, err := bench.Compare([]bench.Cell{a, b}); err != nil {
		t.Fatalf("closed-loop cells were refused: %v", err)
	}
}
