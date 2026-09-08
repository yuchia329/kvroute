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

// NodeCap is the index size that models a fleet of this capacity: the fleet's
// tokens converted to bytes of prompt, divided into blocks.
//
// Sizing it to the fleet is the modelling claim. An index larger than the fleet
// believes replicas hold prefixes they evicted hours ago, and idea.md §4.3 is
// blunt about what that costs: affinity to a replica whose cache no longer has
// the data is strictly worse than least-loaded. An index much smaller than the
// fleet forgets blocks the replicas are still holding, and forfeits matches that
// were really there.
func (c Calibration) NodeCap() int {
	if c.FleetTokens <= 0 || c.PromptBytesPerToken <= 0 {
		return 0
	}
	return int(math.Round(float64(c.FleetTokens) * c.PromptBytesPerToken / BlockBytes))
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
	if !c.BlockIdle.Read {
		return 0, fmt.Errorf("prefix: %s was not published, so the index TTL cannot be calibrated. "+
			"The family needs --kv-cache-metrics, which ADR-0001 sets from the first cell — do not enable it now to obtain this reading, because changing the engine configuration mid-experiment invalidates every completed cell",
			vllmmetrics.BlockIdleBeforeEvict)
	}
	if !c.BlockIdle.Observed() {
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

// MeasureBytesPerToken is the prompt bytes-per-token ratio over a window: the
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
func MeasureBytesPerToken(promptBytes int64, prefill vllmmetrics.Prefill) (float64, bool) {
	if promptBytes <= 0 || !prefill.Read || prefill.PromptTokens <= 0 {
		return 0, false
	}
	return float64(promptBytes) / prefill.PromptTokens, true
}
