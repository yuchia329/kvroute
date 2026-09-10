package prefix_test

import (
	"math"
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/prefix"
)

// The two directions are different failures and are never summed. A run that
// over-predicted 100 tokens on one request and under-predicted 100 on the next
// has two problems, not none.
func TestOverAndUnderPredictionAreNeverNetted(t *testing.T) {
	var d prefix.Divergence
	d.Observe(300, 200) // believed 100 tokens the engine did not hold
	d.Observe(100, 200) // held 100 tokens the router never claimed

	if d.OverTokens != 100 {
		t.Errorf("over-predicted tokens = %v, want 100", d.OverTokens)
	}
	if d.UnderTokens != 100 {
		t.Errorf("under-predicted tokens = %v, want 100", d.UnderTokens)
	}
	if d.Over != 1 || d.Under != 1 {
		t.Errorf("over/under request counts = %d/%d, want 1/1", d.Over, d.Under)
	}
	if d.Requests != 2 {
		t.Errorf("requests = %d, want 2", d.Requests)
	}
}

// A request the router predicted exactly is its own category. Folding it into
// either direction would make an index that was right look like one that erred
// in whichever direction the fold chose.
func TestAnExactPredictionIsNeitherOverNorUnder(t *testing.T) {
	var d prefix.Divergence
	d.Observe(256, 256)
	d.Observe(0, 0)

	if d.Exact != 2 {
		t.Errorf("exact = %d, want 2", d.Exact)
	}
	if d.Over != 0 || d.Under != 0 {
		t.Errorf("over/under = %d/%d, want 0/0", d.Over, d.Under)
	}
	if d.OverTokens != 0 || d.UnderTokens != 0 {
		t.Errorf("over/under tokens = %v/%v, want 0/0", d.OverTokens, d.UnderTokens)
	}
}

// The honoured share is what the cap calibration rests on: of the tokens the
// index claimed, how many the engine actually had. Under-prediction must not
// raise it above one — an index that was right about everything it claimed is
// fully honoured however much it missed, because missing is a different defect
// with a different cost.
func TestTheHonouredShareIsWhatTheEnginesConfirmedOfWhatWasClaimed(t *testing.T) {
	var d prefix.Divergence
	d.Observe(400, 100) // 100 of 400 claimed tokens were really there
	d.Observe(100, 500) // every claimed token was there, and 400 more besides

	honoured, ok := d.Honoured()
	if !ok {
		t.Fatal("a divergence over two predicting requests reports no honoured share")
	}
	// min(400,100) + min(100,500) = 200, over 500 claimed.
	if math.Abs(honoured-0.4) > 1e-9 {
		t.Errorf("honoured = %v, want 0.4", honoured)
	}
}

// Nothing claimed is not a claim that was fully honoured. A policy that
// consults no index predicts zero on every request, and reading that as a
// perfect index is how the cap would be calibrated off a run that never
// exercised it.
func TestNothingClaimedIsNotAnHonouredBelief(t *testing.T) {
	var d prefix.Divergence
	d.Observe(0, 300)
	d.Observe(0, 250)

	if _, ok := d.Honoured(); ok {
		t.Error("a divergence over requests that claimed nothing reports an honoured share")
	}
	if !d.Evidenced() {
		t.Error("requests were observed, so the divergence has evidence of under-prediction to report")
	}
	if d.Under != 2 {
		t.Errorf("under-predicted = %d, want 2", d.Under)
	}
}

// A divergence nobody measured is distinguished from one that measured nothing
// wrong, for the same reason every other reading in this project is.
func TestAnUnmeasuredDivergenceSaysSo(t *testing.T) {
	var d prefix.Divergence
	if d.Evidenced() {
		t.Error("a divergence over no requests claims evidence")
	}
	if _, ok := d.Honoured(); ok {
		t.Error("a divergence over no requests reports an honoured share")
	}
	if !strings.Contains(d.String(), "unmeasured") {
		t.Errorf("an unmeasured divergence renders as %q", d.String())
	}
}

// Merging is how a sweep's cells become one reading. It has to add the
// components rather than average the ratios: two cells of very different sizes
// averaged would weight the smaller one as heavily as the larger.
func TestMergingAddsTheEvidenceRatherThanAveragingTheRatios(t *testing.T) {
	var small, large prefix.Divergence
	small.Observe(100, 0)
	for range 99 {
		large.Observe(100, 100)
	}

	merged := small
	merged.Merge(large)

	if merged.Requests != 100 {
		t.Fatalf("merged requests = %d, want 100", merged.Requests)
	}
	honoured, ok := merged.Honoured()
	if !ok {
		t.Fatal("the merged divergence reports no honoured share")
	}
	if math.Abs(honoured-0.99) > 1e-9 {
		t.Errorf("honoured = %v, want 0.99: averaging the two cells' ratios would have given 0.5", honoured)
	}
}

// AC4 of #17, and the check ADR-0006 says it cannot make itself: the cap is a
// model of the fleet until something has measured how much of that model the
// engines honour, and then it is that model scaled by what they honoured.
func TestTheNodeCapIsScaledByTheShareOfBeliefTheEnginesHonoured(t *testing.T) {
	measured := prefix.Calibration{FleetTokens: 629_760, PromptBytesPerToken: 3.8, BlockIdle: idle()}
	model := measured.NodeCap()

	// Half of everything the index claimed was not there.
	var observed prefix.Divergence
	observed.Observe(400, 200)
	observed.ObserveIndex(model, model)
	measured.ObservedDivergence = observed

	want := model / 2
	if got := measured.NodeCap(); got != want {
		t.Errorf("node cap = %d, want %d: half the belief was honoured, so the index models half the fleet", got, want)
	}
	if source := measured.NodeCapSource(); !strings.Contains(source, "50") {
		t.Errorf("the cap does not say what scaled it: %q", source)
	}
}

// The fleet model is a ceiling. An index larger than the fleet believes in
// blocks the fleet could not be holding whatever the divergence says, so
// under-prediction is not a licence to grow past it.
func TestUnderPredictionDoesNotPushTheCapPastTheFleetModel(t *testing.T) {
	measured := prefix.Calibration{FleetTokens: 629_760, PromptBytesPerToken: 3.8, BlockIdle: idle()}
	model := measured.NodeCap()

	var observed prefix.Divergence
	observed.Observe(100, 900) // the engines held nine times what was claimed
	measured.ObservedDivergence = observed

	if got := measured.NodeCap(); got != model {
		t.Errorf("node cap = %d, want the fleet model %d", got, model)
	}
}

// A divergence measured under a policy that consults no index claims nothing on
// every request, so it says nothing about how large the index should be. Scaling
// the cap by it would shrink the index to nothing on evidence that never
// exercised it.
func TestADivergenceThatClaimedNothingDoesNotResizeTheIndex(t *testing.T) {
	measured := prefix.Calibration{FleetTokens: 629_760, PromptBytesPerToken: 3.8, BlockIdle: idle()}
	model := measured.NodeCap()

	var observed prefix.Divergence
	observed.Observe(0, 300)
	observed.Observe(0, 400)
	measured.ObservedDivergence = observed

	if got := measured.NodeCap(); got != model {
		t.Errorf("node cap = %d, want the fleet model %d", got, model)
	}
	if source := measured.NodeCapSource(); strings.Contains(source, "honoured") {
		t.Errorf("the cap claims to have been calibrated against a divergence that claimed nothing: %q", source)
	}
}

// An index that never filled its cap cannot have over-predicted because of the
// cap. Shrinking it on that evidence would be treating the TTL's failure, and
// the engines' own eviction, as the cap's.
func TestACapThatNeverBoundIsNotShrunkOnOverPrediction(t *testing.T) {
	measured := prefix.Calibration{FleetTokens: 629_760, PromptBytesPerToken: 3.8, BlockIdle: idle()}
	model := measured.NodeCap()

	var observed prefix.Divergence
	observed.Observe(400, 100)
	// The index held a tenth of what it was allowed to.
	observed.ObserveIndex(model/10, model)
	measured.ObservedDivergence = observed

	if got := measured.NodeCap(); got != model {
		t.Errorf("node cap = %d, want the fleet model %d: the cap never bound, so it is not what over-predicted", got, model)
	}
	if source := measured.NodeCapSource(); !strings.Contains(source, "never") {
		t.Errorf("the cap does not say why the divergence was not applied: %q", source)
	}
}

// The calibrated cap still has to produce a usable index. Rounding a heavily
// dishonoured belief down to zero would hand the router a config it refuses,
// which is a worse answer than the smallest index that can hold a belief.
func TestACalibratedCapNeverRoundsAwayToNothing(t *testing.T) {
	measured := prefix.Calibration{FleetTokens: 64, PromptBytesPerToken: 1, BlockIdle: idle()}

	var observed prefix.Divergence
	observed.Observe(1_000_000, 1)
	observed.ObserveIndex(measured.NodeCap(), measured.NodeCap())
	measured.ObservedDivergence = observed

	if got := measured.NodeCap(); got < 1 {
		t.Errorf("node cap = %d: a calibration that produces an unusable index is not a calibration", got)
	}
	if _, err := measured.Config(); err != nil {
		t.Errorf("the calibrated bounds do not build an index: %v", err)
	}
}

// Merging two runs must not invent an occupancy nobody saw. Taking the widest
// nodes and the widest cap independently would pair a full index's occupancy with
// a larger run's cap, report a cap that bound as one that never did, and silently
// skip the recalibration that evidence called for.
func TestMergingKeepsTheIndexOccupancyAndItsCapTogether(t *testing.T) {
	var full, roomy prefix.Divergence
	full.Observe(400, 100)
	full.ObserveIndex(1000, 1000) // a run whose cap bound
	roomy.Observe(400, 100)
	roomy.ObserveIndex(100, 50_000) // a later run with a far larger cap

	merged := full
	merged.Merge(roomy)

	if !merged.CapBound() {
		t.Errorf("merged index = %d of %d: a run whose cap bound is reported as one that never did",
			merged.IndexNodes, merged.IndexCap)
	}
	if merged.IndexNodes > merged.IndexCap {
		t.Errorf("merged index = %d of %d, which is not a pair anything observed", merged.IndexNodes, merged.IndexCap)
	}
}

// The fullest observation wins, so an index that filled a small cap is not hidden
// by a later run that barely touched a large one.
func TestTheFullestIndexObservationIsTheOneKept(t *testing.T) {
	var d prefix.Divergence
	d.ObserveIndex(100, 50_000)
	d.ObserveIndex(900, 1000)
	d.ObserveIndex(50, 50_000)

	if d.IndexNodes != 900 || d.IndexCap != 1000 {
		t.Errorf("index = %d of %d, want 900 of 1000 — the fullest observation", d.IndexNodes, d.IndexCap)
	}
}
