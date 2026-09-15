package prefix

import (
	"encoding/json"
	"fmt"
	"os"
)

// Divergence is the measured gap between what the index believed a replica held
// and what the engine actually held, accumulated over a set of requests.
//
// The index is a belief about state the router does not own, and this is the
// price of that: CONTEXT.md keeps prefix match and prefix cache hit rate as two
// separate terms precisely so the distance between them can be a result rather
// than an error to be tidied away. Nobody in idea.md §1 publishes it, and it
// costs one extra field per row to have.
//
// Both sides are in prompt tokens. The prediction is the router's prefix match,
// which the index counts in its own bytes because it has no tokenizer, converted
// through the bytes-per-token ratio measured on the very request it converts;
// the truth is what the engine says it served out of cache for that request. A
// divergence assembled in bytes on one side and tokens on the other would be a
// number with no units.
//
// The two directions are accumulated separately and never netted. They are
// different failures with different costs — ADR-0006 is built on the asymmetry —
// so a run that over-predicted as much as it under-predicted has two problems,
// and a single signed total would report it as having none.
type Divergence struct {
	// Requests is how many requests contributed, whichever way they diverged.
	Requests int `json:"requests"`

	// PredictedTokens and ActualTokens are the totals each side claimed, kept so
	// that every ratio below can be re-derived rather than trusted.
	PredictedTokens float64 `json:"predicted_tokens"`
	ActualTokens    float64 `json:"actual_tokens"`

	// Over is requests where the router believed in more than the engine held:
	// the failure that misroutes. The request pays the full prefill anyway and
	// spent its routing decision on a reason that had stopped being true, which
	// idea.md §4.3 calls strictly worse than having routed on load.
	Over int `json:"over_predicted"`
	// Under is requests where the engine held more than the router claimed. It
	// forfeits a match that was really there, which costs an avoidable prefill
	// but never actively misroutes.
	Under int `json:"under_predicted"`
	// Exact is requests the router called exactly, including the ones where it
	// claimed nothing and the engine held nothing. Its own column, because
	// folding it either way would make an index that was right look like one
	// that erred in whichever direction the fold chose.
	Exact int `json:"exact"`

	// OverTokens and UnderTokens are the magnitudes on each side: tokens
	// believed in that were not there, and tokens that were there and not
	// believed in.
	OverTokens  float64 `json:"over_predicted_tokens"`
	UnderTokens float64 `json:"under_predicted_tokens"`

	// IndexNodes and IndexCap are how full the index was and what it was allowed
	// to hold, at the widest point anything observed.
	//
	// They are here because they say whether the cap was the constraint at all.
	// An index that never reached its cap cannot have over-predicted *because of*
	// the cap, and shrinking it on that evidence would be superstition; the TTL
	// and the engines' own eviction are what remain. Zero means nobody looked.
	IndexNodes int `json:"index_nodes"`
	IndexCap   int `json:"index_cap"`
}

// Observe folds one request into the reading: what the router predicted the
// chosen replica held, against what the engine says it served out of cache.
func (d *Divergence) Observe(predicted, actual float64) {
	d.Requests++
	d.PredictedTokens += predicted
	d.ActualTokens += actual
	switch {
	case predicted > actual:
		d.Over++
		d.OverTokens += predicted - actual
	case actual > predicted:
		d.Under++
		d.UnderTokens += actual - predicted
	default:
		d.Exact++
	}
}

// ObserveIndex records how full the index was, keeping the fullest observation
// and the cap that observation was made against.
//
// The fullest rather than the latest: the question the figure answers is whether
// the cap ever bound, and an index sampled after a quiet stretch has already
// forgotten that it did.
//
// The pair is kept together, which is the part worth stating. Widening the two
// independently would take a full index's occupancy from one run and a larger cap
// from another, and report a pair nothing ever observed — a cap that bound as one
// that never did, silently skipping the recalibration that evidence called for.
// So the two move as one, and the observation with the highest occupancy ratio
// wins.
func (d *Divergence) ObserveIndex(nodes, cap int) {
	// No cap is no index: the three policies that consult none report zero, and
	// that must not displace an observation of one that exists.
	if cap <= 0 {
		return
	}
	if d.IndexCap > 0 && float64(nodes)*float64(d.IndexCap) <= float64(d.IndexNodes)*float64(cap) {
		return
	}
	d.IndexNodes, d.IndexCap = nodes, cap
}

// Merge adds another reading's evidence to this one.
//
// It adds the components rather than averaging the ratios, so a cell of forty
// requests does not weigh as much as one of four thousand. Every ratio this type
// reports is derived at the end from the totals, which is what makes merging
// safe at all.
func (d *Divergence) Merge(other Divergence) {
	d.Requests += other.Requests
	d.PredictedTokens += other.PredictedTokens
	d.ActualTokens += other.ActualTokens
	d.Over += other.Over
	d.Under += other.Under
	d.Exact += other.Exact
	d.OverTokens += other.OverTokens
	d.UnderTokens += other.UnderTokens
	d.ObserveIndex(other.IndexNodes, other.IndexCap)
}

// Evidenced reports whether anything was measured. A divergence nobody took is
// distinguished from one that found nothing wrong, for the same reason every
// other reading in this project is: a zero with nothing behind it is a scrape
// failure published as a result.
func (d Divergence) Evidenced() bool { return d.Requests > 0 }

// Honoured is the share of the tokens the index claimed that the engine turned
// out to be holding: sum of min(predicted, actual) over sum of predicted.
//
// Bounded above by one on purpose. An index that was right about everything it
// claimed is fully honoured however much it *missed*, because missing is the
// other defect and has its own column; letting under-prediction push this past
// one would let a forgetful index look over-confident.
//
// It is not reported when nothing was claimed. A policy that consults no index
// predicts zero on every request, and reading that as a perfectly honoured
// belief is how the node cap would come to be calibrated off a run that never
// exercised the index.
func (d Divergence) Honoured() (float64, bool) {
	if d.PredictedTokens <= 0 {
		return 0, false
	}
	return (d.PredictedTokens - d.OverTokens) / d.PredictedTokens, true
}

// CapBound reports whether the index ever filled the cap it was given, and so
// whether the cap can be blamed for anything this reading found.
func (d Divergence) CapBound() bool {
	return d.IndexCap > 0 && d.IndexNodes >= d.IndexCap
}

// MeanOverTokens and MeanUnderTokens are the size of each direction's error on
// the requests that made it, rather than spread over the requests that did not.
// An over-prediction of 500 tokens on one request in a thousand is a different
// fact from half a token on every one.
func (d Divergence) MeanOverTokens() float64 {
	if d.Over == 0 {
		return 0
	}
	return d.OverTokens / float64(d.Over)
}

func (d Divergence) MeanUnderTokens() float64 {
	if d.Under == 0 {
		return 0
	}
	return d.UnderTokens / float64(d.Under)
}

// String renders the reading as both directions and the share honoured, because
// no one of the three is readable alone.
func (d Divergence) String() string {
	if !d.Evidenced() {
		return "unmeasured"
	}
	honoured := "no belief to honour"
	if share, ok := d.Honoured(); ok {
		honoured = fmt.Sprintf("%.1f%% of belief honoured", share*100)
	}
	return fmt.Sprintf("%d requests, %s; over-predicted on %d (%.0f tokens), under-predicted on %d (%.0f tokens)",
		d.Requests, honoured, d.Over, d.OverTokens, d.Under, d.UnderTokens)
}

// Save writes the reading to a file, so that the node cap the router runs with
// can be traced back to the run that measured it.
//
// A file for the same reason the calibration itself is one: the divergence is
// measured over a completed sweep and consumed at the next bring-up, and a bound
// whose evidence lives only in somebody's shell history cannot be defended later
// — which is the whole objection to an arbitrary constant.
func (d Divergence) Save(path string) error {
	body, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("prefix: encode divergence: %w", err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return fmt.Errorf("prefix: write divergence: %w", err)
	}
	return nil
}

// LoadDivergence reads a reading written by Save.
func LoadDivergence(path string) (Divergence, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Divergence{}, fmt.Errorf("prefix: read divergence: %w", err)
	}
	var d Divergence
	if err := json.Unmarshal(body, &d); err != nil {
		return Divergence{}, fmt.Errorf("prefix: parse divergence %s: %w", path, err)
	}
	if !d.Evidenced() {
		return Divergence{}, fmt.Errorf("prefix: the divergence in %s was measured over no requests, so it says nothing about the index", path)
	}
	return d, nil
}
