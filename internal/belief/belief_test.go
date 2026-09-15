package belief_test

import (
	"math"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/belief"
)

// The reading is the share of what was claimed that the engines turned out to
// be holding, which is CONTEXT.md's honoured belief taken per replica over a
// window rather than per run.
func TestTheRateIsTheShareOfWhatWasClaimedThatTheEngineHeld(t *testing.T) {
	now := time.Now()
	f := belief.NewFeedback(belief.Window{Requests: 8, TTL: time.Minute, Quorum: 1})
	f.Observe(now, 400, 200)
	f.Observe(now, 400, 400)

	rate := f.Rate(now)
	if !rate.Read {
		t.Fatal("two observations left the rate unread")
	}
	if want := 0.75; math.Abs(rate.Fraction-want) > 1e-9 {
		t.Errorf("rate = %v, want %v: 600 of 800 claimed tokens were held", rate.Fraction, want)
	}
	if rate.Claims != 2 {
		t.Errorf("claims = %d, want 2", rate.Claims)
	}
}

// Bounded above by one, for the reason prefix.Divergence.Honoured is: an index
// that was right about everything it claimed is fully honoured however much it
// missed, and letting an under-prediction push the rate past one would let a
// forgetful index look like a replica with room to spare.
func TestHoldingMoreThanWasClaimedDoesNotPushTheRatePastOne(t *testing.T) {
	now := time.Now()
	f := belief.NewFeedback(belief.Window{Requests: 8, TTL: time.Minute, Quorum: 1})
	f.Observe(now, 100, 900)

	if rate := f.Rate(now); rate.Fraction != 1 {
		t.Errorf("rate = %v, want 1: the engine held nine times the claim and the claim was still fully honoured", rate.Fraction)
	}
}

// A request the index claimed nothing for says nothing about whether the
// replica is honouring beliefs, and folding it in as a perfectly honoured zero
// would make a policy that found no match look like a replica holding
// everything.
func TestARequestThatClaimedNothingIsNotEvidence(t *testing.T) {
	now := time.Now()
	f := belief.NewFeedback(belief.Window{Requests: 8, TTL: time.Minute, Quorum: 1})
	f.Observe(now, 0, 0)
	f.Observe(now, 0, 500)

	if rate := f.Rate(now); rate.Read {
		t.Errorf("rate = %v, want unread: nothing was claimed, so nothing was honoured or dishonoured", rate)
	}
}

// The graceful degradation the spill rule rests on, in the one place a policy
// cannot forget it. A replica nobody has sent a scoring request to declines
// nothing, which is the policy as it was measured before the signal existed.
func TestAnUnreadRateIsNeverUnderAnyLowWaterMark(t *testing.T) {
	var unread belief.Honoured
	for _, mark := range []float64{0.01, 0.5, 0.99, 1} {
		if unread.Under(mark) {
			t.Errorf("an unread rate is under %v", mark)
		}
	}
}

// Strictly under, so a mark of 0 cannot fire on a replica honouring nothing by
// arithmetic and a mark of 1 does not fire on a replica honouring everything.
func TestUnderIsStrict(t *testing.T) {
	exact := belief.Honoured{Fraction: 0.5, Read: true, Claims: 4}
	if exact.Under(0.5) {
		t.Error("a rate exactly at the mark is under it")
	}
	if !exact.Under(0.51) {
		t.Error("a rate below the mark is not under it")
	}
}

// A rate off one unlucky request would spill a replica out of affinity on
// noise, so a reading is not taken until the window holds evidence enough to be
// worth acting on — and until then it is unread, not zero.
func TestARateBelowQuorumIsUnreadRatherThanZero(t *testing.T) {
	now := time.Now()
	f := belief.NewFeedback(belief.Window{Requests: 8, TTL: time.Minute, Quorum: 3})
	f.Observe(now, 100, 0)
	f.Observe(now, 100, 0)

	if rate := f.Rate(now); rate.Read {
		t.Fatalf("rate = %v, want unread below quorum", rate)
	}
	f.Observe(now, 100, 0)
	rate := f.Rate(now)
	if !rate.Read || rate.Fraction != 0 {
		t.Errorf("rate = %v, want a read rate of 0 once quorum is reached", rate)
	}
}

// The window is what makes the signal respond rather than accumulate. A replica
// that was evicting and has stopped has to be believed again, and a rate taken
// over the whole run would carry the bad stretch for the rest of it.
func TestTheRateForgetsObservationsPushedOutOfTheWindow(t *testing.T) {
	now := time.Now()
	f := belief.NewFeedback(belief.Window{Requests: 2, TTL: time.Minute, Quorum: 1})
	f.Observe(now, 100, 0)
	f.Observe(now, 100, 0)
	if rate := f.Rate(now); rate.Fraction != 0 {
		t.Fatalf("rate = %v, want 0 while the window holds two dishonoured claims", rate.Fraction)
	}

	f.Observe(now, 100, 100)
	f.Observe(now, 100, 100)
	if rate := f.Rate(now); rate.Fraction != 1 {
		t.Errorf("rate = %v, want 1: both dishonoured claims were pushed out of a window of two", rate.Fraction)
	}
	if claims := f.Rate(now).Claims; claims != 2 {
		t.Errorf("claims = %d, want 2: the window holds two", claims)
	}
}

// And it forgets by age as well as by count, which is the self-healing half of
// the rule. A replica the spill rule has stopped sending matches to stops
// producing evidence, so a rate held forever would freeze it out of affinity on
// evidence nothing could refresh.
func TestEvidenceOlderThanTheTTLIsForgotten(t *testing.T) {
	now := time.Now()
	f := belief.NewFeedback(belief.Window{Requests: 64, TTL: 30 * time.Second, Quorum: 1})
	f.Observe(now, 100, 0)

	if rate := f.Rate(now.Add(29 * time.Second)); !rate.Read {
		t.Error("evidence inside the TTL was forgotten")
	}
	if rate := f.Rate(now.Add(31 * time.Second)); rate.Read {
		t.Errorf("rate = %v, want unread: its only evidence is older than the TTL", rate)
	}
}

// Unread and zero print differently, because a replica honouring nothing and a
// replica nobody has asked about are the two states this whole type exists to
// keep apart.
func TestUnreadAndZeroDoNotPrintTheSame(t *testing.T) {
	unread := belief.Honoured{}.String()
	zero := belief.Honoured{Read: true, Claims: 4}.String()
	if unread == zero {
		t.Errorf("an unread rate and a rate of zero both print %q", unread)
	}
	if unread != "unread" {
		t.Errorf("an unread rate prints %q", unread)
	}
}

// A window nobody could act on is refused at startup rather than silently
// disabling the condition mid-run, which is the failure mode the whole ticket
// is about.
func TestAWindowThatCannotProduceAReadingIsRefused(t *testing.T) {
	for _, w := range []belief.Window{
		{Requests: 0, TTL: time.Minute, Quorum: 1},
		{Requests: 8, TTL: 0, Quorum: 1},
		{Requests: 8, TTL: time.Minute, Quorum: 0},
		{Requests: 4, TTL: time.Minute, Quorum: 8},
	} {
		if err := w.Validate(); err == nil {
			t.Errorf("%+v was accepted", w)
		}
	}
	if err := belief.DefaultWindow.Validate(); err != nil {
		t.Errorf("the default window is invalid: %v", err)
	}
}

// The conversion every byte-denominated claim has to be read through, measured
// on the very request it converts rather than through a run-wide average that
// fits no individual prompt.
func TestAByteClaimIsConvertedAtTheRequestsOwnBytesPerToken(t *testing.T) {
	// 800 prompt bytes came to 200 tokens, so 400 claimed bytes are 100 tokens.
	tokens, ok := belief.PredictedTokens(400, 0, 800, 200)
	if !ok || tokens != 100 {
		t.Errorf("predicted = %v (%v), want 100", tokens, ok)
	}
}

// Exact residency's claim is already in the engine's tokens, and converting it
// through bytes would put into it an error it does not have.
func TestATokenClaimIsTakenAsItStands(t *testing.T) {
	tokens, ok := belief.PredictedTokens(0, 512, 800, 200)
	if !ok || tokens != 512 {
		t.Errorf("predicted = %v (%v), want 512 unconverted", tokens, ok)
	}
}

// Nothing to convert with is no prediction rather than a prediction of zero,
// which would enter the window as a claim that was never made.
func TestAByteClaimWithNothingToConvertItByIsNoPrediction(t *testing.T) {
	if _, ok := belief.PredictedTokens(400, 0, 0, 200); ok {
		t.Error("a claim converted by zero prompt bytes was accepted")
	}
	if _, ok := belief.PredictedTokens(400, 0, 800, 0); ok {
		t.Error("a claim converted by zero prompt tokens was accepted")
	}
}
