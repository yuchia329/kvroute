package bench

import "github.com/yuchia329/kvroute/internal/policy"

// The load imbalance factor's denominator is measured open-loop, at the rung
// the degeneracy was found at, and it is measured in that order: observe, cut,
// then sweep.
//
// #16 swept the factor at one closed-loop rung, 32 virtual users, and #18
// settled it at 2 there. Both are sound at that rung and neither says anything
// about another one, because the quantity the rule divides by is a property of
// the fleet rather than of the policy: at c32 the quietest replica is carrying
// work, and at 6 requests per second open-loop it is carrying nothing or one
// request on most decisions. #31 measured what that does — the rule declined
// 19–20% of later turns against 0.674% at c32, and 42% of the declined turns
// missed the SLO — and the cause is that a multiple of a small integer is not a
// ratio. See policy's loaddenominator.go for the arithmetic.
//
// So this axis is two candidate denominators rather than a second factor
// ladder. The factor is settled; what is not settled is what it is a multiple
// of, and the two answers differ only where the fleet is quiet.
//
// The order matters and is the discipline #28 arrived at for the residency
// mark and ADR-0006 applies to the index's bounds. First an observing pass with
// the rule off, whose rows say what the comparison would have been made against
// at this rung and which points could fire there at all — that is
// MeasureLoadComparison, and it costs no fleet time beyond the one cell. Only
// then are the points written into LoadDenominatorGrid, naming the run they
// were cut from, and swept for what they are worth. A point chosen before that
// would be #16's mistake in a new unit: a threshold nobody has seen the signal's
// range for is not a grid point, it is a guess with a decimal place.

const (
	// LoadDenominatorRate is the arrival rate the denominator is measured at, in
	// requests per second offered to the whole fleet.
	//
	// Open-loop, and 6, because that is where the degeneracy is: it is the rung
	// #19's chaos runs drove policy 4 at, the one that produced #31's rows, and
	// a rung of ArrivalRateSweep so the cells sit beside the goodput ladder's own.
	// The closed-loop driver cannot show this at all — it holds concurrency
	// fixed, so the fleet never empties between arrivals and the minimum never
	// spends its time at zero.
	//
	// One rung rather than a ladder, for the reason idea.md §6 budgets the
	// tunable sweep at a single concurrency: every point is a cell of the full
	// geometry on a shared box. The rung is chosen where the two denominators are
	// furthest apart, which is where a comparison between them is decidable.
	LoadDenominatorRate = 6.0

	// SettledRungMinimumWasZero is the share of decisions at the rung the factor
	// was settled at whose fleet minimum was genuinely zero: 0.16, over the
	// 10,469 spill-off decisions #16 recorded at c32 (see Chosen's note).
	//
	// It is the one figure the settled rung has on record for this, and it is
	// what any later run's own share is read against. A run far above it is not
	// running the rule the factor was chosen for, whatever its cells are
	// labelled.
	SettledRungMinimumWasZero = 0.16
)

// LoadDenominatorGrid is the points the denominator axis is swept at, and it is
// deliberately empty until an observing pass has been taken to cut it against.
//
// The candidates are not a mystery — the factor is settled at 2, and the two
// denominators are min and mean — but which of them can produce a decision at
// this rung is a question about a fleet nobody has recorded the load of. The
// observing pass answers it in one cell: every row carries what the comparison
// would have been made against, so ProjectedPoint.Reaches says which points fire
// before a run is spent on one that cannot. #16 spent two cells of three
// discovering that the other way round.
//
// When it is written, the run it was cut from is named here, as the load axis's
// second pass named its own.
var LoadDenominatorGrid []policy.Spill

// LoadDenominatorSweep is the points the denominator is measured at, led by the
// spill-off reference.
//
// The reference leads it for the reason the other two sweeps' does: it is policy
// 4 with no spill rule at all, at this rung and this workload, and every point
// is read against it. The four-policy comparison's own prefix_affinity cells
// cannot serve — they run closed-loop at 32 users, and a row measured under a
// different driver is not a baseline for this one.
//
// While LoadDenominatorGrid is empty it returns that reference alone, which is
// the observing pass: see LoadDenominatorGrid.
func LoadDenominatorSweep() []policy.Spill {
	points := []policy.Spill{{}}
	return append(points, LoadDenominatorGrid...)
}

// LoadDenominatorCandidates is the points the observing pass projects, so that
// the run which cuts the grid and the report that reads it are looking at the
// same list.
//
// The settled factor against both denominators, and the factors either side of
// it against the mean. The settled point is in it because the projection has to
// reproduce the rule that actually ran before anything it says about the others
// is worth reading, and the neighbours are there because the mean is a larger
// denominator: if the mean at 2 declines almost nothing at this rung, the point
// that recovers the rule's intent is a lower factor rather than the same one.
func LoadDenominatorCandidates() []policy.Spill {
	// Chosen's load half exactly, denominator included, with the residency mark
	// dropped because this axis sweeps one condition at a time. Copied rather
	// than rebuilt from the factor: if Chosen ever moves to the mean, a rebuilt
	// point would quietly become the rule that is no longer running, and the
	// projection's one fixed reference would be the wrong one.
	settled := Chosen
	settled.HitRateLowWater = 0
	points := []policy.Spill{settled}
	for _, factor := range []float64{1.5, 2, 4, 8} {
		points = append(points, policy.Spill{LoadImbalanceFactor: factor, MeanInflightDenominator: true})
	}
	return points
}

// CellSpill is the grid point a cell was measured at, rebuilt from the columns
// it carries.
//
// One function rather than a struct literal at each reading site, because a
// reader that forgot a column would report a cell as having run a rule it did
// not run — and the columns are exactly the fields that are added to over time.
// A cell whose policy has no spill rule carries zeros, which is the off point,
// and callers that need to tell "off" from "no valve" ask policy.HasSpillRule.
func CellSpill(cell Cell) policy.Spill {
	return policy.Spill{
		HitRateLowWater:         cell.HitRateLowWater,
		LoadImbalanceFactor:     cell.LoadImbalanceFactor,
		MeanInflightDenominator: cell.MeanInflightDenominator,
	}
}

// OffItsSettledRung reports whether a run is about to drive the load condition
// against the fleet minimum under the open-loop driver.
//
// That is the combination #31 is about, and it is worth saying out loud at the
// start of a run rather than discovering in the rows: the factor is settled at a
// closed-loop rung where the minimum is a real quantity, and the open-loop
// driver empties the fleet between arrivals, so the same number is asking a
// different question. No rate threshold, because there is no measured rate at
// which it stops — the observing pass is what says where this run sat.
//
// It takes a point rather than a config because both open-loop runners have to
// ask it. The one that found this was the chaos runner, not the sweep: #19 drove
// policy 4 at 6 requests per second, the rule declined a fifth of later turns,
// and that arm had to be run again with the rule off.
func OffItsSettledRung(point policy.Spill) bool {
	return point.LoadImbalanceFactor > 0 && !point.MeanInflightDenominator
}

// SettledRungWarning is what such a run is told, once, at its start.
const SettledRungWarning = "the load condition's factor was settled closed-loop at 32 users, against the fleet's inflight minimum; " +
	"open-loop the minimum spends its time at zero or one, where a multiple of it is an absolute inflight threshold rather than a ratio (#31). " +
	"Read this run's router rows with cmd/loadcomparison before reporting its spill column"
