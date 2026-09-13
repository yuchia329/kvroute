package bench

import (
	"math"
	"testing"
	"time"
)

// How the recency axis is offered, checked against the generator with no fleet
// running.
//
// #17's second criterion plots divergence against how long it had been since a
// session was last served. The overnight cells cannot supply that axis: they were
// closed-loop, where a session's next turn goes out the moment its last response
// lands, so 86% of 62,000 requests fell in the "<1s" bucket and everything past
// ten seconds held 2% between them — put there by the rotation rather than by
// design.
//
// The open-loop driver spaces a session's turns by the think time, so recency
// becomes settable. What is not settable is recency alone. The driver's
// conversation pool is arrival rate x think time by Little's law, so at a fixed
// rate a longer gap means more conversations in flight, and more conversations in
// flight is more live session tokens against the same fleet KV. Recency and
// memory pressure are one lever pulled from two ends, and the two tests below are
// the two halves of that: what a think-time ladder would confound, and what a
// single skewed cell separates.

// recencyDesign is one candidate open-loop configuration.
type recencyDesign struct {
	rate       float64
	think      time.Duration
	cell       time.Duration
	workingSet float64
	skew       float64
}

// The geometry every cell in this project runs, and the fleet it runs against.
const (
	designFleetKV       = 629760 // measured aggregate fleet KV, in tokens
	designTurnsPerVisit = 4
	designPromptTokens  = 448
	designOutputTokens  = 64
	designSessionTokens = designTurnsPerVisit * (designPromptTokens + designOutputTokens)
	designWarmupShare   = 0.25
)

// designWarmup is the quarter-of-the-cell warm-up #17's two configurations ran,
// kept so those designs are still stated in the terms they were chosen under.
// #29 sizes its warm-up from the visit period instead.
func designWarmup(cell time.Duration) time.Duration {
	return time.Duration(designWarmupShare * float64(cell))
}

// offer walks the arrival schedule a cell would fire and reports how its
// requests fall across the recency axis, plus the live session tokens that axis
// costs. Nothing here contacts a fleet: the schedule is arithmetic and the
// session a request lands on is a pure function of (user, turn).
//
// warm is the slice at the front of the cell whose arrivals are excluded, which
// is a parameter rather than a share of the cell because #29 sizes it from the
// visit period instead — see recencywindow_test.go.
func (d recencyDesign) offer(t *testing.T, warm time.Duration) (buckets map[string]int, measured int, liveTokens int) {
	t.Helper()
	sessions := int(math.Round(d.workingSet * designFleetKV / designSessionTokens))
	pool, err := conversationPool(d.rate, d.think)
	if err != nil {
		t.Fatalf("pool for rate %g think %v: %v", d.rate, d.think, err)
	}
	mt, err := NewMultiTurn(MultiTurnWorkload{
		Model:                "m",
		Sessions:             sessions,
		TurnsPerSession:      designTurnsPerVisit,
		PromptTokens:         designPromptTokens,
		OutputTokens:         designOutputTokens,
		Skew:                 d.skew,
		SystemPromptFraction: 0.3,
		BranchFraction:       0.3,
		Seed:                 1,
	})
	if err != nil {
		t.Fatalf("generator: %v", err)
	}

	turns := newRotation(pool, 7)
	interval := time.Duration(float64(time.Second) / d.rate)
	lastSeen := map[string]time.Duration{}
	buckets = map[string]int{}

	for k := range int(d.cell.Seconds() * d.rate) {
		due := time.Duration(k) * interval
		user, turn := turns.at(k)
		session := mt.Next(user, turn).Session
		prior, seen := lastSeen[session]
		lastSeen[session] = due
		if due < warm {
			continue
		}
		measured++
		if !seen {
			buckets[FirstTurnLabel]++
			continue
		}
		buckets[recencyLabel(due-prior)]++
	}
	return buckets, measured, pool * designSessionTokens
}

// The reason the recency axis is not swept as a ladder of think times.
//
// It looks like the obvious design and it is not a single-factor one: holding
// the arrival rate and raising the think time raises the conversation pool in
// exact proportion, so the fleet goes from comfortably holding the live set to
// several times oversubscribed across the rungs. Divergence would rise up such a
// ladder and nothing in the result could say whether beliefs got older or the
// fleet simply got fuller. The multiple is asserted rather than described so that
// this stays a measured objection.
func TestAThinkTimeLadderConfoundsRecencyWithMemoryPressure(t *testing.T) {
	ladder := []recencyDesign{
		{8, 5 * time.Second, 150 * time.Second, 3, 0},
		{8, 20 * time.Second, 150 * time.Second, 3, 0},
		{8, 45 * time.Second, 240 * time.Second, 3, 0},
		{8, 90 * time.Second, 400 * time.Second, 3, 0},
	}
	var lowest, highest int
	for i, d := range ladder {
		_, _, live := d.offer(t, designWarmup(d.cell))
		if i == 0 || live < lowest {
			lowest = live
		}
		if live > highest {
			highest = live
		}
		t.Logf("think %-5v pool tokens %8d = %.2fx fleet KV", d.think, live, float64(live)/designFleetKV)
	}
	// Sweeping recency over a range that also sweeps the fleet from a quarter
	// full to twice oversubscribed is sweeping two things.
	if ratio := float64(highest) / float64(lowest); ratio < 4 {
		t.Errorf("the think-time ladder moves live session tokens only %.1fx, so the confound this test records has gone away and the ladder may be worth reconsidering", ratio)
	}
	if float64(lowest)/designFleetKV > 1 {
		t.Errorf("the bottom rung already oversubscribes the fleet at %.2fx, so it is not the low-pressure end this test assumes", float64(lowest)/designFleetKV)
	}
}

// The configuration the recency measurement actually runs, and the property that
// makes it readable.
//
// One cell, one arrival rate, one think time, one working set — so every request
// in it meets the same fleet under the same pressure. Zipf skew supplies the
// spread from within: a hot conversation is held by several of the driver's slots
// at once and comes round sooner than the think time, a cold one waits for a
// redraw. So the age of a belief varies across requests while nothing else does,
// which makes the curve a within-cell comparison and not a between-cell one.
//
// The axis is only readable if the buckets are all occupied. A configuration
// that piled 90% of its requests into one bucket would produce a plot with one
// real point on it, which is what the closed-loop cells already produced.
func TestTheRecencyCellSpreadsAcrossTheWholeAxis(t *testing.T) {
	chosen := recencyDesign{rate: 8, think: 30 * time.Second, cell: 300 * time.Second, workingSet: 3, skew: 1.0}
	buckets, measured, live := chosen.offer(t, designWarmup(chosen.cell))

	t.Logf("pool tokens %d = %.2fx fleet KV over %d measured arrivals", live, float64(live)/designFleetKV, measured)
	var occupied int
	for _, label := range recencyLabels {
		share := 100 * float64(buckets[label]) / float64(measured)
		t.Logf("  %-12s %6d %5.1f%%", label, buckets[label], share)
		if buckets[label] > 0 {
			occupied++
		}
		// No bucket may swallow the axis. The closed-loop cells put 86% in one,
		// which is exactly the failure this configuration exists to avoid.
		if share > 60 {
			t.Errorf("bucket %s holds %.1f%% of the cell, so the axis is one point and a tail", label, share)
		}
	}
	// Every bucket up to the one past the derived 57s TTL. The two oldest are
	// allowed to be empty here: reaching them needs a longer think time than
	// this cell runs, which is what the second configuration is for.
	if want := len(recencyLabels) - 1; occupied < want {
		t.Errorf("only %d of %d recency buckets are occupied, so the plot would have gaps", occupied, want)
	}
}

// The second configuration, which exists to reach past the TTL.
//
// The index drops a belief at the derived 57 seconds, so the ages either side of
// that are different situations: below it the index still claims and the engine
// may already have evicted, above it the index claims nothing and the column
// should go quiet. A run that never offers an age past the TTL cannot show the
// second half, and the first configuration's think time does not reach it.
func TestTheSecondRecencyCellReachesPastTheIndexTTL(t *testing.T) {
	const derivedTTL = 57 * time.Second
	past := recencyDesign{rate: 8, think: 75 * time.Second, cell: 420 * time.Second, workingSet: 3, skew: 1.0}
	buckets, measured, _ := past.offer(t, designWarmup(past.cell))

	var beyond int
	for _, label := range recencyLabels {
		t.Logf("  %-12s %6d %5.1f%%", label, buckets[label], 100*float64(buckets[label])/float64(measured))
	}
	// The buckets whose whole range sits past the TTL.
	for i, bound := range recencyBuckets {
		if bound > derivedTTL {
			beyond += buckets[recencyLabels[i+1]]
		}
	}
	beyond += buckets[recencyLabels[len(recencyLabels)-1]]
	if share := 100 * float64(beyond) / float64(measured); share < 10 {
		t.Errorf("only %.1f%% of the cell is older than the %v TTL, so the half of the curve where the index has already forgotten would rest on too few requests", share, derivedTTL)
	}
}
