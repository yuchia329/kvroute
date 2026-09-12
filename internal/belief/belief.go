// Package belief holds the router's running account of which replicas are
// honouring what it believes about them.
//
// It exists because the signal the spill rule needs is not one anybody
// publishes. A replica under memory pressure is a replica that is *evicting*,
// and no gauge says so: vllm:kv_cache_usage_perc counts the blocks allocated to
// requests the engine is currently running, so it reads the active batch — the
// same pressure the inflight condition already measures, in different units
// (ADR-0011). Blocks holding the cached prefixes of finished requests are free
// from the allocator's point of view, reclaimable and waiting to be reused or
// evicted, and they are exactly the blocks a prefix match depends on.
//
// What does say so is the engine's own answer to the question the index is
// asking. Every response carries usage.prompt_tokens_details.cached_tokens
// (ADR-0008): how much of this prompt the replica really had. Held against what
// the router claimed that replica was holding when it routed there, it is a
// per-request verdict on the belief — and a replica that has stopped honouring
// beliefs is a replica that is evicting them. The signal therefore rides
// responses the router already proxies, needs no scrape, and closes the loop
// between the divergence #17 measures after a run and the rule #16 applies
// during one.
//
// It is a rate over a window rather than a running total, because the rule acts
// on it. See Window.
package belief

import (
	"fmt"
	"time"
)

// Honoured is one reading of a replica's honoured rate: the share of the prompt
// tokens the router claimed that replica was holding which the engine turned
// out to be holding, over the window the reading was taken across.
//
// It is CONTEXT.md's honoured belief, taken per replica and per window rather
// than per run, and bounded above by one for the same reason: an index that was
// right about everything it claimed is fully honoured however much it missed,
// because missing is the other defect and has its own measurement.
//
// Read is false when the window holds no evidence worth acting on, so "this
// replica is honouring nothing" and "nobody has asked this replica anything"
// do not read the same. The distinction is the whole of the spill rule's
// graceful degradation: a run whose responses carry no usage block, or a
// replica the rule has already stopped sending matches to, must route as though
// the signal did not exist rather than spill every request on a reading nobody
// took.
type Honoured struct {
	Fraction float64 `json:"fraction"`
	Read     bool    `json:"read"`
	// Claims is how many scoring requests the reading rests on, so a rate is
	// never reported without the evidence behind it. A rate of 0.4 over three
	// requests and one over sixty are different facts.
	Claims int `json:"claims"`
}

// Under reports whether this replica is known to have fallen below a low-water
// mark.
//
// An unread reading is never under, whatever the mark. That lives here rather
// than at the call site so that a policy cannot forget it, for the reason the
// same rule lived on the gauge this signal replaces: a fleet with no feedback
// routes as it did before the signal existed.
//
// Strictly under, so a mark of 0 cannot fire on a replica that is honouring
// nothing by arithmetic, and a mark of 1 does not fire on a replica honouring
// everything it was asked about.
func (h Honoured) Under(lowWater float64) bool {
	return h.Read && h.Fraction < lowWater
}

// String renders the reading with the evidence behind it, keeping "unread"
// distinct from a share of zero.
func (h Honoured) String() string {
	if !h.Read {
		return "unread"
	}
	return fmt.Sprintf("%.1f%% of %d", h.Fraction*100, h.Claims)
}

// Window bounds the evidence one reading rests on.
//
// Bounded on three sides because the rule acts on the reading, and each side
// answers a different way of being wrong. Requests and TTL make the signal
// respond rather than accumulate: a replica that was evicting and has stopped
// has to be believed again, and a rate taken over a whole run would carry its
// worst stretch to the end of it. The TTL is the half that matters most, and it
// is what makes the rule self-healing rather than self-confirming — a replica
// the rule has spilled away from stops being sent matches, so it stops
// producing evidence, and a rate held forever would freeze it out of affinity
// on evidence nothing could ever refresh. Quorum is the other direction: a rate
// off one unlucky request is noise, and acting on it would take a replica out
// of affinity for the length of a window on nothing.
//
// None of the three is a measurement, which makes them knobs of the same kind
// as the spill thresholds rather than bounds of the kind ADR-0006 refuses to
// default: a value here is a grid point, and the run records the point it ran
// at.
type Window struct {
	// Requests is how many of a replica's most recent scoring requests the rate
	// is taken over.
	Requests int `json:"requests"`
	// TTL is how old a scoring request may be and still count.
	TTL time.Duration `json:"ttl"`
	// Quorum is the fewest scoring requests a reading may be taken over. Below
	// it the rate is unread, which declines nothing.
	Quorum int `json:"quorum"`
}

// DefaultWindow is the window the router uses when a run does not state one.
//
// 64 requests at a quorum of 8, expiring after 30 seconds. The fleet serves
// roughly 30 requests a second across six replicas at the top of the arrival
// ladder, so one replica sees about five a second: 64 requests is some thirteen
// seconds of history, a quorum of 8 is under two seconds of it, and the TTL is
// comfortably longer than both so that it binds only on a replica that has
// genuinely stopped receiving matches rather than on an ordinary quiet moment.
//
// Defaulted where a prefix index's bounds are refused, because the failure modes
// are opposite. A guessed node cap silently changes what the index believes and
// nothing downstream can tell; a window that is wrong makes the signal noisy or
// sluggish in a way every run reports, since the rate and the decisions it drove
// are both on the rows. The default is a starting point for the sweep, not a
// claim, and -honour-window and -honour-ttl move it.
var DefaultWindow = Window{Requests: 64, TTL: 30 * time.Second, Quorum: 8}

// Validate refuses a window that could not produce a reading, at startup rather
// than by quietly disabling the condition for the length of a run — which is
// the failure this whole signal exists to stop repeating.
func (w Window) Validate() error {
	if w.Requests <= 0 {
		return fmt.Errorf("belief: the honoured-rate window is a count of recent scoring requests and must be positive, got %d", w.Requests)
	}
	if w.TTL <= 0 {
		return fmt.Errorf("belief: the honoured-rate window's TTL is how old evidence may be and must be positive, got %v", w.TTL)
	}
	if w.Quorum <= 0 {
		return fmt.Errorf("belief: the honoured-rate quorum is the fewest scoring requests a reading may rest on and must be positive, got %d", w.Quorum)
	}
	if w.Quorum > w.Requests {
		return fmt.Errorf("belief: a quorum of %d cannot be reached in a window of %d requests, so the rate would never be read and the condition would be silently off", w.Quorum, w.Requests)
	}
	return nil
}

// String renders the window for the line a run logs its own configuration on.
func (w Window) String() string {
	return fmt.Sprintf("%d requests/%v, quorum %d", w.Requests, w.TTL, w.Quorum)
}

// PredictedTokens is what the router claimed the chosen replica was holding for
// one request, in the engine's own tokens, and whether it claimed anything that
// can be held to the engine's answer at all.
//
// Two policies make the claim and they make it in different units, so both are
// taken here rather than converted into one at the call sites. Exact residency
// already counts in the engine's tokens and is taken as it stands: converting it
// through bytes would put into it an error it does not have. Prefix affinity
// counts in its own bytes, because the index has no tokenizer, and is converted
// at this request's own measured bytes per token — both sides of the ratio are
// facts about this one request, so it cannot be an average that fits no
// individual prompt. CONTEXT.md's prompt bytes per token is the same quantity
// over a whole run, and the divergence report is where the two are compared
// rather than assumed.
//
// A claim of nothing is not a prediction. A policy that found no match, and a
// request whose prompt the engine did not account for, both report false rather
// than a claim of zero tokens: a zero claim folded into the window would be
// perfectly honoured by arithmetic and would drag every replica's rate towards
// one, which is the direction that silently disables the rule.
func PredictedTokens(matchBytes, matchTokens, promptBytes, promptTokens int) (float64, bool) {
	if matchTokens > 0 {
		return float64(matchTokens), true
	}
	if matchBytes <= 0 || promptBytes <= 0 || promptTokens <= 0 {
		return 0, false
	}
	return float64(matchBytes) * float64(promptTokens) / float64(promptBytes), true
}
