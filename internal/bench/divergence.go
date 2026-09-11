package bench

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/record"
)

// Belief divergence: the gap between what the router's prefix index believed a
// replica held and what the engine says it actually held, measured per request
// and reported along the two axes it is expected to move on.
//
// This is #17, and idea.md §1 records that nobody publishes it at any scale. The
// sticky-versus-cache-aware ablation has been published at datacentre scale
// three times over; the accuracy of the approximate index those routers decide
// on has not been, which is what makes this result stand whichever policy wins.
//
// Everything here only reads. The rows a sweep wrote are the system of record,
// so the whole measurement is recomputable from a repository checkout with no
// fleet running and no GPU present — which matters more for this figure than for
// most, because it is derived rather than counted and a derivation nobody can
// re-run is a claim rather than a result.

// FirstTurnLabel is the recency bucket for a request whose session nothing had
// served yet inside its cell.
//
// Its own bucket rather than an infinite age: the index has no belief to have
// gone stale, so a divergence there is measuring the *other* thing — a prefix
// arriving from a shared system prompt or a branched ancestor, which is exactly
// where idea.md §5 says prefix affinity should separate from session affinity.
// Averaged into the oldest bucket it would look like the worst staleness in the
// run.
const FirstTurnLabel = "first turn"

// SpillOffLabel is the spill bucket for a cell whose policy declined nothing:
// the reference every threshold is read against, and the only population the
// node cap is calibrated from.
const SpillOffLabel = "off"

// UnstatedLabel is the working-set bucket for a cell whose workload stated no WS
// point. It is an absence rather than WS 0, which would be a point on the axis.
const UnstatedLabel = "unstated"

// recencyBuckets are the upper bounds of the "time since the session was last
// served" axis, in the units the fleet's own behaviour lives in.
//
// The ladder spans the range the index's TTL is derived into: the p90 of the
// engines' idle-before-evict tail is a matter of seconds to tens of seconds on
// this host, so buckets finer than a second would split noise. The last bucket is
// open-ended, because there is no age past which a belief stops being
// interesting — that is the whole question.
//
// The ladder runs past the TTL rather than stopping at it. It was first written
// against a 20-second TTL, and half a minute was then a sound top bound: an
// index that had already forgotten could not over-predict, so everything past it
// behaved alike. The derived TTL is 57 seconds, measured off 5,076 real
// evictions, which puts that top bound in the middle of the interesting range
// instead of past it. The three ages either side of the TTL are three different
// situations and must not share a bucket:
//
//   - 30s–1m: the index still believes and the engines may already have evicted.
//     This is where over-prediction lives, and it was previously pooled with
//     ages where the index claims nothing.
//   - 1m–2m: the index has dropped the belief. Predicted falls to whatever a
//     shared system prompt or a branch ancestor supplies, so the column should
//     go quiet — and if it does not, the TTL is not doing what it is derived to.
//   - 2m+: far past every belief's life, and a control on the two above.
//
// Widening the ladder re-bins existing rows rather than invalidating them: the
// gap is derived from the recorded start times, never stored, so every sweep
// already measured re-reads under the new bounds.
var recencyBuckets = []time.Duration{
	time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
}

// recencyLabels are the buckets in order, oldest last, with FirstTurnLabel
// leading. One list rather than a formatter and a separate ordering, so a bucket
// cannot be labelled one way and sorted another.
var recencyLabels = buildRecencyLabels()

func buildRecencyLabels() []string {
	labels := []string{FirstTurnLabel}
	previous := time.Duration(0)
	for _, bound := range recencyBuckets {
		if previous == 0 {
			labels = append(labels, "<"+bound.String())
		} else {
			labels = append(labels, previous.String()+"-"+bound.String())
		}
		previous = bound
	}
	return append(labels, ">="+previous.String())
}

// recencyLabel bins one gap. The label is the bucket's own range, so a table row
// says what it covers without a legend.
func recencyLabel(since time.Duration) string {
	for i, bound := range recencyBuckets {
		if since < bound {
			// The leading FirstTurnLabel offsets the bucket index by one.
			return recencyLabels[i+1]
		}
	}
	return recencyLabels[len(recencyLabels)-1]
}

// DivergenceBin is one row of the report: a slice of the requests, and what the
// divergence was over it.
type DivergenceBin struct {
	Label string `json:"label"`
	// order sorts the bins numerically where their labels would sort
	// alphabetically, so WS 10 does not come between WS 1 and WS 3.
	order float64
	prefix.Divergence
}

// DivergenceReport is the measurement over one or more sweeps.
type DivergenceReport struct {
	// Dirs are the sweeps this was measured over, named so a report cannot be
	// read without knowing what went into it.
	Dirs  []string `json:"dirs"`
	Cells int      `json:"cells"`
	// Rows is every measured row considered, and Unaccounted how many of them
	// carried no engine account of their prompt and so could contribute no
	// divergence. Reported rather than quietly dropped: a figure measured over a
	// tenth of a run is a different figure from one measured over all of it, and
	// nothing else in the output would say which this was.
	Rows        int `json:"rows"`
	Unaccounted int `json:"unaccounted"`

	Overall prefix.Divergence `json:"overall"`

	ByPolicy     []DivergenceBin `json:"by_policy"`
	ByWorkingSet []DivergenceBin `json:"by_working_set"`
	ByRecency    []DivergenceBin `json:"by_recency"`
	// BySpill separates the spill grid points from the reference that declines
	// nothing.
	//
	// It is not a nicety. Spill diverts exactly the requests that would have
	// tested the index's best-match belief, and the row then records the
	// *target's* match rather than the declined replica's — usually near zero. A
	// reading blended across thresholds therefore measures the index on a
	// population the spill rule chose, which is why ForCalibration takes the
	// spill-off cells alone.
	BySpill []DivergenceBin `json:"by_spill"`

	// EngineComputedTokens is the prompt tokens the rows say the GPUs computed,
	// summed, and CounterComputedTokens the same quantity off the fleet's own
	// counters over the same cells' windows.
	//
	// Both, because they are two independent accounts of one physical fact and
	// the whole report rests on the per-request one being trustworthy. They are
	// not expected to match exactly — the counter window excludes each cell's
	// warm-up by a boundary that is a few requests wide, and a cell's dropped and
	// failed requests reached the engines without returning usage — but a gross
	// disagreement means the per-request account is measuring something else.
	// Published side by side rather than checked against a threshold: the
	// tolerance would be a constant nobody could defend, and the reader can see
	// two numbers.
	EngineComputedTokens  float64 `json:"engine_computed_tokens"`
	CounterComputedTokens float64 `json:"counter_computed_tokens"`

	// byPolicySpill is the calibration's own view, unexported because it is not
	// a table anybody reads: it exists so ForCalibration can hold both the policy
	// and the spill point fixed at once.
	byPolicySpill map[policySpill]*prefix.Divergence
}

// MeasureDivergence reads the sweeps in dirs and measures belief divergence over
// every request they recorded.
//
// Several directories rather than one, for the same reason compare takes
// several: the working-set axis is a property of the pressure grid, whose points
// are swept into their own directories, and a report that could only read one of
// them could not draw the axis it exists to draw.
func MeasureDivergence(dirs []string) (DivergenceReport, error) {
	report := DivergenceReport{Dirs: dirs}

	byPolicy := map[string]*prefix.Divergence{}
	byWorkingSet := map[pressurePoint]*prefix.Divergence{}
	byRecency := map[string]*prefix.Divergence{}
	bySpill := map[string]*prefix.Divergence{}
	// Keyed on both, because the calibration wants the cells that declined
	// nothing *and* consulted an index: a policy with no index reports its spill
	// as off too, and folding those in would size the index off requests that
	// never claimed anything.
	byPolicySpill := map[policySpill]*prefix.Divergence{}

	for _, dir := range dirs {
		cells, err := LoadCells(dir)
		if err != nil {
			return DivergenceReport{}, err
		}
		for _, cell := range cells {
			rows, err := decodeFile[Result](filepath.Join(dir, "cells", cell.ID+".jsonl"))
			if err != nil {
				return DivergenceReport{}, err
			}
			report.Cells++
			if prefill := cell.Prefill(); prefill.Read {
				report.CounterComputedTokens += prefill.Recomputed()
			}
			report.measureCell(cell, rows, byPolicy, byWorkingSet, byRecency, bySpill, byPolicySpill)
		}
	}
	if report.Cells == 0 {
		return DivergenceReport{}, fmt.Errorf("bench: no cells in %v, so there is no divergence to measure", dirs)
	}

	report.ByPolicy = binsOf(byPolicy, func(policy string) float64 { return policyOrder(policy) })
	report.ByWorkingSet = workingSetBins(byWorkingSet)
	report.ByRecency = binsOf(byRecency, recencyOrder)
	report.BySpill = binsOf(bySpill, spillOrder)
	report.byPolicySpill = byPolicySpill
	return report, nil
}

// policySpill keys a divergence on the two things the calibration has to hold
// fixed at once: which policy made the prediction, and whether anything declined
// it.
type policySpill struct {
	policy string
	spill  string
}

// spillOrder puts the reference first, so a table reads as "this is what the
// index did undisturbed, and this is what each threshold did to it".
func spillOrder(label string) float64 {
	if label == SpillOffLabel {
		return -1
	}
	return 0
}

// spillLabel names a cell's grid point.
func spillLabel(cell Cell) string {
	s := policy.Spill{KVHighWater: cell.KVHighWater, LoadImbalanceFactor: cell.LoadImbalanceFactor}
	if !s.Enabled() {
		return SpillOffLabel
	}
	return s.String()
}

// measureCell folds one cell's rows into the report.
//
// The rows are walked in start order rather than the order they were written.
// The record is written as each request completes, so a cell's file is in
// completion order, and reading a session's recency off that would date a turn
// by whichever of its neighbours happened to finish first.
func (r *DivergenceReport) measureCell(cell Cell, rows []Result, byPolicy map[string]*prefix.Divergence, byWorkingSet map[pressurePoint]*prefix.Divergence, byRecency map[string]*prefix.Divergence, bySpill map[string]*prefix.Divergence, byPolicySpill map[policySpill]*prefix.Divergence) {
	spill := spillLabel(cell)
	pair := policySpill{policy: cell.Policy, spill: spill}
	ordered := make([]Result, len(rows))
	copy(ordered, rows)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].StartedAtNs < ordered[j].StartedAtNs })

	// lastServed is when each session's previous turn finished, within this cell.
	// Per cell rather than across the sweep, because cells are separated by a
	// settle pause and draw from their own slice of the workload's user space:
	// a session id repeating in the next cell is a different conversation.
	lastServed := map[string]int64{}

	for _, row := range ordered {
		// Warm-up rows are excluded from the divergence, as they are from every
		// other summary, but they still date the session. Skipping them entirely
		// would report the first measured turn of a session as its first turn
		// ever, and bin a fresh belief as one that had never been formed.
		measured := !row.Warmup
		if measured {
			r.Rows++
		}

		session := row.Session
		since, seen := time.Duration(0), false
		if at, served := lastServed[session]; served && session != "" {
			seen = true
			// Clamped: two turns of one conversation can overlap under the
			// open-loop driver, and a negative age is not a fresher belief.
			since = max(time.Duration(row.StartedAtNs-at), 0)
		}
		if session != "" && row.Outcome == record.OutcomeSuccess {
			lastServed[session] = max(lastServed[session], row.StartedAtNs+row.TotalNs)
		}
		if !measured {
			continue
		}

		predicted, converts := row.PredictedCachedTokens()
		if !converts || !row.EngineCacheRead {
			// No engine account of this request's prompt, so its prediction has
			// nothing to be checked against. Counted as unmeasured rather than as
			// a request that diverged by nothing, which would report an index as
			// accurate on requests nobody checked.
			r.Unaccounted++
			continue
		}
		actual := float64(row.EngineCachedTokens)
		if computed, ok := row.ComputedPrefillTokens(); ok {
			r.EngineComputedTokens += float64(computed)
		}

		r.Overall.Observe(predicted, actual)
		bin(byPolicy, cell.Policy).Observe(predicted, actual)
		bin(byWorkingSet, pressurePoint{workingSet: cell.WorkingSet, skew: cell.Skew}).Observe(predicted, actual)
		bin(bySpill, spill).Observe(predicted, actual)
		bin(byPolicySpill, pair).Observe(predicted, actual)
		if seen {
			bin(byRecency, recencyLabel(since)).Observe(predicted, actual)
		} else {
			bin(byRecency, FirstTurnLabel).Observe(predicted, actual)
		}
	}

	// The index occupancy is the cell's, so it lands on every bin the cell
	// contributed to — it is what says whether the cap bound, and the cap is one
	// index shared by every request the router placed.
	r.Overall.ObserveIndex(cell.PrefixIndexNodes, cell.PrefixIndexCap)
	bin(byPolicy, cell.Policy).ObserveIndex(cell.PrefixIndexNodes, cell.PrefixIndexCap)
	bin(byWorkingSet, pressurePoint{workingSet: cell.WorkingSet, skew: cell.Skew}).ObserveIndex(cell.PrefixIndexNodes, cell.PrefixIndexCap)
	bin(bySpill, spill).ObserveIndex(cell.PrefixIndexNodes, cell.PrefixIndexCap)
	bin(byPolicySpill, pair).ObserveIndex(cell.PrefixIndexNodes, cell.PrefixIndexCap)
}

func bin[K comparable](bins map[K]*prefix.Divergence, key K) *prefix.Divergence {
	if bins[key] == nil {
		bins[key] = &prefix.Divergence{}
	}
	return bins[key]
}

func binsOf(bins map[string]*prefix.Divergence, order func(string) float64) []DivergenceBin {
	out := make([]DivergenceBin, 0, len(bins))
	for label, d := range bins {
		out = append(out, DivergenceBin{Label: label, order: order(label), Divergence: *d})
	}
	sortBins(out)
	return out
}

// pressurePoint keys a working-set bin on both grid axes at once.
//
// Both, because skew decides how much of the pool a cell of finite length
// actually draws from: at WS 1 a cell realises 0.97 of its label at alpha 0 and
// 0.40 at alpha 1.4. A working-set bin pooling three skew levels therefore
// averages a several-fold range of realised memory pressure, which flattens the
// curve this measurement exists to draw.
type pressurePoint struct {
	workingSet float64
	skew       float64
}

func workingSetBins(bins map[pressurePoint]*prefix.Divergence) []DivergenceBin {
	out := make([]DivergenceBin, 0, len(bins))
	for at, d := range bins {
		label, order := UnstatedLabel, -1.0
		if at.workingSet > 0 {
			label = fmt.Sprintf("%.2g (skew %g)", at.workingSet, at.skew)
			order = at.workingSet
		}
		out = append(out, DivergenceBin{Label: label, order: order, Divergence: *d})
	}
	sortBins(out)
	return out
}

func sortBins(bins []DivergenceBin) {
	sort.SliceStable(bins, func(i, j int) bool {
		if bins[i].order != bins[j].order {
			return bins[i].order < bins[j].order
		}
		return bins[i].Label < bins[j].Label
	})
}

// policyOrder puts the policies in the order the comparison names them, so this
// table's rows sit in the same order as every other table's.
func policyOrder(name string) float64 {
	if i := slices.Index(policy.Order, name); i >= 0 {
		return float64(i)
	}
	return float64(len(policy.Order))
}

// recencyOrder sorts the buckets by the age they cover rather than by their
// labels, which would put "<1s" after "10s-30s".
func recencyOrder(label string) float64 {
	if i := slices.Index(recencyLabels, label); i >= 0 {
		return float64(i)
	}
	return float64(len(recencyLabels))
}

// ForCalibration is the reading the prefix index's node cap is resized against:
// the divergence of the one policy that routes on that index.
//
// Restricted on purpose, twice over. A policy that consults no index predicts
// nothing on every request, so folding its rows in would dilute the honoured
// share with requests that never made a claim — and the cap would be shrunk on
// evidence from a run that never exercised it. prefix.Calibration refuses such a
// reading too, but refusing it there and assembling it here would leave the
// report publishing a figure the calibration then discards. And exact residency
// predicts plenty, from a different index fed by the engines rather than by
// dispatch history: scaling the prefix index's cap by how much of that belief the
// engines honoured would credit the approximation with the exact policy's
// accuracy.
func (r DivergenceReport) ForCalibration() prefix.Divergence {
	var d prefix.Divergence
	for pair, measured := range r.byPolicySpill {
		if pair.policy != policy.PrefixAffinityName {
			continue
		}
		// Spill-off only. A declined match sends the request to a replica the
		// index claimed little or nothing about, and the row records that
		// target's match — so a blended reading measures the index on the
		// population the spill rule selected rather than on its own belief.
		if pair.spill != SpillOffLabel {
			continue
		}
		if measured.PredictedTokens <= 0 {
			continue
		}
		d.Merge(*measured)
	}
	return d
}

// Report renders the measurement as markdown.
//
// Tables rather than a drawn figure: the per-request rows are in the run's
// requests.parquet and carry every column a plot needs, so this is the shape the
// numbers are read in and checked in, and the plot is drawn from the rows rather
// than from a picture somebody would have to trust.
func (r DivergenceReport) Report() string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Belief divergence\n\n")
	fmt.Fprintf(&b, "The gap between what the router believed the chosen replica held of a prompt\n")
	fmt.Fprintf(&b, "and what the engine says it actually served out of cache, per request.\n\n")
	fmt.Fprintf(&b, "- Prediction: the router's prefix match, in bytes, converted at each request's own\n")
	fmt.Fprintf(&b, "  prompt bytes per token — both sides of that ratio are on the row. Exact residency\n")
	fmt.Fprintf(&b, "  predicts in the engine's own tokens and is read as it stands.\n")
	fmt.Fprintf(&b, "- Truth: `usage.prompt_tokens_details.cached_tokens`, the engine's own per-request\n")
	fmt.Fprintf(&b, "  account. `vllm:request_prefill_kv_computed_tokens` is the same quantity as a\n")
	fmt.Fprintf(&b, "  histogram and carries no request id, so it cannot be joined to the prediction\n")
	fmt.Fprintf(&b, "  it would check.\n")
	fmt.Fprintf(&b, "- Over-prediction and under-prediction are never netted. Believing in blocks a\n")
	fmt.Fprintf(&b, "  replica evicted misroutes the request; forgetting blocks it still holds only\n")
	fmt.Fprintf(&b, "  forfeits a match. They are different failures and share no column.\n\n")

	fmt.Fprintf(&b, "Measured over %d cells in %s: %d of %d measured requests carried an engine\n",
		r.Cells, strings.Join(r.Dirs, ", "), r.Rows-r.Unaccounted, r.Rows)
	fmt.Fprintf(&b, "account of their prompt.\n\n")
	fmt.Fprintf(&b, "Overall: %s\n\n", r.Overall.String())

	fmt.Fprintf(&b, "Computed prefill, two ways: %.0f tokens summed off the per-request rows, against\n", r.EngineComputedTokens)
	fmt.Fprintf(&b, "%.0f off the fleet's own counters over the same cells' measured windows. They\n", r.CounterComputedTokens)
	fmt.Fprintf(&b, "cover slightly different windows and are printed rather than reconciled; a gross\n")
	fmt.Fprintf(&b, "disagreement means the per-request account is measuring something else.\n\n")

	fmt.Fprintf(&b, "## By policy\n\n")
	writeDivergenceTable(&b, "policy", r.ByPolicy)

	fmt.Fprintf(&b, "\n## By working set ratio\n\n")
	fmt.Fprintf(&b, "The pressure axis that drives eviction: a fleet that cannot hold every session\n")
	fmt.Fprintf(&b, "at once is one whose replicas are dropping the blocks the index still believes in.\n")
	fmt.Fprintf(&b, "\n**These are nominal labels and they overstate nothing but themselves.** The\n")
	fmt.Fprintf(&b, "generator sizes prompts at a declared bytes-per-token that the engines do not\n")
	fmt.Fprintf(&b, "agree with, so the ratio a cell is labelled with is not the pressure it applied —\n")
	fmt.Fprintf(&b, "ADR-0007 measures the gap and is explicit that runs under it must not be\n")
	fmt.Fprintf(&b, "published as WS 1.0. The error is identical across cells, so the *shape* of the\n")
	fmt.Fprintf(&b, "curve below is sound and only its x-axis is mislabelled. The measured ratio each\n")
	fmt.Fprintf(&b, "cell actually ran at is on the cell record, and skew is printed beside the label\n")
	fmt.Fprintf(&b, "because it decides how much of the pool a cell of finite length ever touches.\n\n")
	fmt.Fprintf(&b, "`%s` is a cell whose workload stated no WS point, which is an absence rather\n", UnstatedLabel)
	fmt.Fprintf(&b, "than WS 0 — pass `-kv-capacity` to the sweep and the ratio is derived without\n")
	fmt.Fprintf(&b, "changing a byte of what it sends.\n\n")
	writeDivergenceTable(&b, "WS", r.ByWorkingSet)

	fmt.Fprintf(&b, "\n## By time since the session was last served\n\n")
	fmt.Fprintf(&b, "How stale the belief was when it was acted on. Derived from the rows rather than\n")
	fmt.Fprintf(&b, "recorded by the router — a request's age is its own start minus the end of that\n")
	fmt.Fprintf(&b, "session's previous turn in the same cell — so it costs the routing path nothing\n")
	fmt.Fprintf(&b, "and is recomputable from the record. `%s` is a request whose session nothing\n", FirstTurnLabel)
	fmt.Fprintf(&b, "had served yet: there is no belief to have gone stale, so a match there came from\n")
	fmt.Fprintf(&b, "a shared system prompt or a branched ancestor instead.\n\n")
	writeDivergenceTable(&b, "since last served", r.ByRecency)

	fmt.Fprintf(&b, "\n## By spill point\n\n")
	fmt.Fprintf(&b, "`%s` is the reference that declines nothing, and it is the only population the\n", SpillOffLabel)
	fmt.Fprintf(&b, "node cap is calibrated from. Spill diverts exactly the requests that would have\n")
	fmt.Fprintf(&b, "tested the index's best-match belief, and the row then records the *target's*\n")
	fmt.Fprintf(&b, "match rather than the declined replica's — so a threshold's row says what the\n")
	fmt.Fprintf(&b, "index was worth on the requests the rule left alone, not what it believed.\n\n")
	writeDivergenceTable(&b, "spill", r.BySpill)

	fmt.Fprintf(&b, "\n## The index's own bounds\n\n")
	if r.Overall.IndexCap == 0 {
		fmt.Fprintf(&b, "No cell recorded an index: none of these policies consults one.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "The index reached %d of its %d nodes. ", r.Overall.IndexNodes, r.Overall.IndexCap)
	if r.Overall.CapBound() {
		fmt.Fprintf(&b, "The cap bound, so it is a candidate for what\nthe over-prediction above is measuring, and `calibrate -divergence` will resize it.\n")
	} else {
		fmt.Fprintf(&b, "The cap never bound, so it is not what the\nover-prediction above is measuring — the TTL and the engines' own eviction are what\nremain, and resizing the cap on this evidence would be superstition.\n")
	}
	return b.String()
}

func writeDivergenceTable(b *strings.Builder, axis string, bins []DivergenceBin) {
	// The two token columns are means over the requests that erred in that
	// direction, not totals: an over-prediction of 500 tokens on one request in a
	// thousand is a different fact from half a token on every one, and a total
	// cannot tell them apart.
	fmt.Fprintf(b, "| %s | requests | predicted/req | actual/req | honoured | over-predicted | mean tokens over | under-predicted | mean tokens under | exact |\n", axis)
	fmt.Fprintln(b, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, bin := range bins {
		fmt.Fprintf(b, "| %s | %d | %s | %s | %s | %d | %s | %d | %s | %d |\n",
			bin.Label, bin.Requests,
			perRequest(bin.PredictedTokens, bin.Requests),
			perRequest(bin.ActualTokens, bin.Requests),
			honouredCell(bin.Divergence),
			bin.Over, meanCell(bin.MeanOverTokens(), bin.Over),
			bin.Under, meanCell(bin.MeanUnderTokens(), bin.Under),
			bin.Exact)
	}
}

func perRequest(total float64, requests int) string {
	if requests == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f", total/float64(requests))
}

// honouredCell renders the share of belief the engines confirmed, and says so
// rather than printing a percentage when there was no belief to confirm.
func honouredCell(d prefix.Divergence) string {
	share, ok := d.Honoured()
	if !ok {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", share*100)
}

func meanCell(mean float64, requests int) string {
	if requests == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f", mean)
}
