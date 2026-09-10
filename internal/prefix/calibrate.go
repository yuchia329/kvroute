package prefix

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// Calibration is the measured basis for an index's two bounds.
//
// Both bounds are modelling decisions about hardware this process does not own,
// and idea.md §4.3 is explicit that sizing them to the fleet is what makes them
// defensible rather than arbitrary. So they are derived here from three figures
// that were measured — aggregate fleet KV capacity, the prompt bytes per token
// this workload actually sends, and the engine's own idle-before-evict tail —
// and Config refuses when one of them is missing rather than falling back to a
// constant. A guess that reaches the router as a default is a guess nobody will
// ever find again.
type Calibration struct {
	// FleetTokens is aggregate fleet KV capacity: num_gpu_blocks x block_size
	// read off every replica's own cache configuration and summed, as the
	// characterization pass measures it. Never extrapolated from one card.
	FleetTokens int `json:"fleet_tokens"`
	// PromptBytesPerToken is how many bytes of prompt one token of KV
	// corresponds to, measured over a run rather than assumed. It is the
	// conversion the index needs and cannot compute: the index counts bytes
	// because it has no tokenizer, and the capacity it is sized against is
	// counted in tokens.
	//
	// It is not the characterization pass's Geometry.BytesPerToken, which is a
	// token's KV footprint in GPU memory. These two quantities share a name in
	// English and nothing else.
	PromptBytesPerToken float64 `json:"prompt_bytes_per_token"`
	// BlockIdle is the engine's own vllm:kv_block_idle_before_evict_seconds.
	// The TTL is taken from its tail.
	BlockIdle vllmmetrics.Distribution `json:"block_idle_before_evict"`
	// BlockLifetime is vllm:kv_block_lifetime_seconds, recorded but not derived
	// from.
	//
	// It is the check on the figure rather than its source. A TTL is a claim
	// about how long a block sits unused before the engine drops it, which is
	// what BlockIdle measures; a lifetime counts a block's whole existence
	// including the time it was being reused, so deriving a TTL from it would
	// believe longest exactly where a conversation was hottest. Kept because a
	// TTL longer than blocks live at all is a calibration that passed its own
	// check while believing in blocks the fleet could never have held, and
	// nothing else in the file would show it.
	BlockLifetime vllmmetrics.Distribution `json:"block_lifetime"`
	// ChosenTTL, when set, is used instead of deriving the TTL from BlockIdle.
	//
	// It exists for the case the derivation cannot serve: the residency
	// histograms are empty until the fleet has evicted blocks, and the fleet is
	// brought down between policy passes, so a sweep that has to start against a
	// freshly booted fleet has nothing to derive from. Recorded in the file and
	// reported by TTLSource, so a run under a chosen TTL can never be written up
	// as a run under a measured one -- which is the only thing that made the
	// derivation worth insisting on.
	ChosenTTL time.Duration `json:"chosen_ttl,omitempty"`
	// ObservedDivergence is what a completed run measured of the gap between
	// what the index believed and what the engines held. It scales the node cap;
	// see NodeCap.
	//
	// It arrives from a run rather than from the fleet, which is what makes it
	// the check ADR-0006 could not make for itself: the fleet can say how large
	// an index modelling it would be, and only a run can say how much of that
	// model the engines honoured. Empty until one has, and then the cap says so
	// through NodeCapSource.
	ObservedDivergence Divergence `json:"observed_divergence,omitzero"`
}

// TTLSource says where the TTL came from, so every report of it carries its own
// provenance rather than relying on whoever writes the summary to remember.
func (c Calibration) TTLSource() string {
	if c.ChosenTTL > 0 {
		return "chosen"
	}
	return "measured from " + vllmmetrics.BlockIdleBeforeEvict
}

// CheckAgainstLifetime reports whether the derived TTL is consistent with how
// long the fleet's blocks actually live.
//
// A TTL past the median lifetime means the index goes on believing in blocks
// most of which no longer exist by then, whatever the idle tail said. It is a
// warning rather than a refusal: the two families measure different things and
// can legitimately disagree, so this hands the reader the disagreement instead
// of deciding it for them. No lifetime reading means no check, not a failure.
func (c Calibration) CheckAgainstLifetime(ttl time.Duration) (string, bool) {
	if !c.BlockLifetime.Evidenced() {
		return "", false
	}
	median, located := c.BlockLifetime.Quantile(0.50)
	if !located {
		return "", false
	}
	lifetime := time.Duration(median * float64(time.Second))
	if ttl <= lifetime {
		return "", false
	}
	return fmt.Sprintf("the derived TTL of %v is longer than the median block lifetime of %v, so the index would believe in blocks most of which have already been evicted: check the idle-before-evict reading against the load the fleet was actually under",
		ttl, lifetime), true
}

// TTLQuantile is the point of the idle-before-evict distribution the TTL is
// taken from.
//
// The 90th percentile, not the 99th. The TTL bounds how long the index goes on
// believing in a block, and the two directions of error are not symmetric:
// believing too long routes a request to a replica that has already evicted the
// data, which pays the full prefill *and* spends the routing decision on a
// reason that stopped being true — strictly worse than having routed on load.
// Believing too briefly only forfeits a match the fleet might have had, which
// costs a prefill that would otherwise have been avoided but never actively
// misroutes. So the TTL sits where most blocks are still resident rather than
// where nearly all of them are gone.
const TTLQuantile = 0.90

// Config turns the measurements into the index's bounds, or explains which
// measurement is missing.
//
// It refuses rather than defaults. The point of the whole type is that these two
// numbers can be traced back to something read off the fleet, and a Config that
// quietly substituted a constant for an absent histogram would produce an index
// that looks calibrated and is not.
func (c Calibration) Config() (Config, error) {
	if c.FleetTokens <= 0 {
		return Config{}, errors.New("prefix: aggregate fleet KV capacity is required to size the index, and it is measured by the characterization pass rather than assumed")
	}
	if c.PromptBytesPerToken <= 0 {
		return Config{}, errors.New("prefix: the prompt bytes-per-token ratio is required to size the index, and it is measured over a run rather than assumed")
	}
	ttl, err := c.TTL()
	if err != nil {
		return Config{}, err
	}
	return Config{NodeCap: c.NodeCap(), TTL: ttl}, nil
}

// FleetModelNodeCap is the index size that models a fleet of this capacity: the
// fleet's tokens converted to bytes of prompt, divided into blocks.
//
// Sizing it to the fleet is the modelling claim. An index larger than the fleet
// believes replicas hold prefixes they evicted hours ago, and idea.md §4.3 is
// blunt about what that costs: affinity to a replica whose cache no longer has
// the data is strictly worse than least-loaded. An index much smaller than the
// fleet forgets blocks the replicas are still holding, and forfeits matches that
// were really there.
//
// It is a model of the fleet and not a measurement of the index's accuracy,
// which is why it is a ceiling rather than the answer: see NodeCap.
func (c Calibration) FleetModelNodeCap() int {
	if c.FleetTokens <= 0 || c.PromptBytesPerToken <= 0 {
		return 0
	}
	return int(math.Round(float64(c.FleetTokens) * c.PromptBytesPerToken / BlockBytes))
}

// NodeCap is the cap the index actually runs with: the fleet model, scaled down
// by the share of its belief the engines turned out to be honouring.
//
// ADR-0006 sized the cap to the fleet and said plainly what that argument could
// not reach — nothing in it proves a fleet-sized index is the *right* size, only
// that it is a size derived from the fleet. Belief divergence is the measurement
// that closes it, and this is where it lands (#17): an index whose claims the
// engines honour half the time is holding twice as much belief as the fleet is
// backing, so it models half the fleet.
//
// Three guards, each of them a way the scaling would otherwise be superstition:
//
//   - The fleet model is a ceiling. Under-prediction means the engines held more
//     than the index claimed, which is a reason to forget less, not a licence to
//     believe in more blocks than the fleet can hold.
//   - A divergence that claimed nothing does not resize anything. Every request
//     under a policy that consults no index predicts zero, and scaling by that
//     would shrink the index to nothing on evidence that never exercised it.
//   - A cap that never bound is not what over-predicted. If the index never
//     filled it, the beliefs that failed were expired by the TTL's reckoning or
//     evicted by the engines on their own schedule, and shrinking the cap would
//     be treating their failure as its.
func (c Calibration) NodeCap() int {
	model := c.FleetModelNodeCap()
	scale, ok := c.nodeCapScale()
	if !ok {
		return model
	}
	// Never to zero: a cap of nothing is a config prefix.New refuses, which is a
	// worse answer to a badly honoured belief than the smallest index that can
	// hold one.
	return max(int(math.Round(float64(model)*scale)), 1)
}

// NodeCapSource says where the cap came from, so every report of it carries its
// own provenance — the same reason TTLSource exists.
func (c Calibration) NodeCapSource() string {
	scale, ok := c.nodeCapScale()
	if !ok {
		return c.uncalibratedNodeCapSource()
	}
	return fmt.Sprintf("the fleet model of %d nodes, scaled by the %.1f%% of predicted tokens the engines honoured over %d requests",
		c.FleetModelNodeCap(), scale*100, c.ObservedDivergence.Requests)
}

// uncalibratedNodeCapSource says why an observed divergence did not move the
// cap, which is a different statement from there having been none.
func (c Calibration) uncalibratedNodeCapSource() string {
	const model = "the fleet's own KV capacity in blocks of prompt, which models the fleet rather than measuring the index"
	d := c.ObservedDivergence
	switch {
	case !d.Evidenced():
		return model + "; no divergence has been measured against it yet"
	case d.PredictedTokens <= 0:
		return model + "; the measured divergence claimed no prefix on any request, so it says nothing about how large the index should be"
	case !d.CapBound():
		return fmt.Sprintf("%s; the measured divergence is not applied because the index reached %d of its %d nodes and so was never capped",
			model, d.IndexNodes, d.IndexCap)
	}
	return model
}

// nodeCapScale is the factor the observed divergence puts on the fleet model,
// and whether there is one at all. Above one it is clamped away rather than
// returned: see NodeCap.
func (c Calibration) nodeCapScale() (float64, bool) {
	d := c.ObservedDivergence
	if !d.Evidenced() || !d.CapBound() {
		return 0, false
	}
	honoured, ok := d.Honoured()
	if !ok || honoured >= 1 {
		return 0, false
	}
	return honoured, true
}

// TTL is how long a belief stands, taken from the engine's idle-before-evict
// tail at TTLQuantile.
//
// The failure this returns an error for is the one worth naming: the three KV
// residency families are off unless the replica was started with
// --kv-cache-metrics, so a scrape of a fleet without it finds nothing at all
// rather than zeros. Turning the flag on mid-experiment to get this reading
// would change the engine configuration every cell is supposed to share and
// invalidate every completed cell, so the answer to an unread histogram is to
// refuse, never to enable it and re-scrape.
func (c Calibration) TTL() (time.Duration, error) {
	if c.ChosenTTL > 0 {
		return c.ChosenTTL, nil
	}
	if !c.BlockIdle.Read {
		return 0, fmt.Errorf("prefix: %s was not published, so the index TTL cannot be calibrated. "+
			"The family needs --kv-cache-metrics, which ADR-0001 sets from the first cell — do not enable it now to obtain this reading, because changing the engine configuration mid-experiment invalidates every completed cell",
			vllmmetrics.BlockIdleBeforeEvict)
	}
	if !c.BlockIdle.Evidenced() {
		return 0, fmt.Errorf("prefix: %s is published but empty, so no block has been evicted yet and there is no tail to calibrate against. Run the fleet under load past its KV capacity first",
			vllmmetrics.BlockIdleBeforeEvict)
	}
	seconds, located := c.BlockIdle.Quantile(TTLQuantile)
	if !located {
		return 0, fmt.Errorf("prefix: the p%.0f of %s falls past the last bucket the engine publishes, so its value is not in the exposition. Calibrate against a window in which blocks were actually evicted",
			TTLQuantile*100, vllmmetrics.BlockIdleBeforeEvict)
	}
	ttl := time.Duration(seconds * float64(time.Second))
	if ttl <= 0 {
		return 0, fmt.Errorf("prefix: the p%.0f of %s is %v, which is not a duration a belief can stand for",
			TTLQuantile*100, vllmmetrics.BlockIdleBeforeEvict, ttl)
	}
	return ttl, nil
}

// MeasurePromptBytesPerToken is the prompt bytes-per-token ratio over a window: the
// prompt bytes the harness sent, over the prompt tokens the engines reported
// processing.
//
// Measured rather than assumed, because it is the conversion every byte figure
// in this project has to be read through. A prefix match is reported in bytes —
// the index has no tokenizer and will not pretend to — and this is what lets a
// reader turn one into the tokens the engine charges for. The two sides come
// from different places on purpose: the bytes are what the client sent, and the
// tokens are what the engine says it processed, so the ratio measures the chat
// template and the tokenizer together rather than either alone.
func MeasurePromptBytesPerToken(promptBytes int64, prefill vllmmetrics.Prefill) (float64, bool) {
	if promptBytes <= 0 || !prefill.Read || prefill.PromptTokens <= 0 {
		return 0, false
	}
	return float64(promptBytes) / prefill.PromptTokens, true
}
