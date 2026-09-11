package disagg

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/yuchia329/kvroute/internal/gpu"
)

// Transfer is one timed copy, as ops/pcie/bandwidth.py records it.
type Transfer struct {
	// Path is how the bytes moved: h2d or d2h between one card and pinned host
	// memory; peer, CUDA's own card-to-card copy; or bounce, a card-to-card copy
	// staged through pinned host memory by hand.
	Path string `json:"path"`
	// Condition is what else the host was doing: alone, busy (both cards
	// running a memory-bound loop), or together (every pair of one class
	// copying at once).
	Condition string `json:"condition"`
	// Src and Dst are cards by index, or -1 for host memory.
	Src int `json:"src"`
	Dst int `json:"dst"`
	// Link is the pair's topology class as NVML named it when the copy ran. The
	// recorded matrix is what classes a pair; this is only checked against it.
	Link string `json:"link"`
	// HostNode is the NUMA node the host buffer's pages were on.
	HostNode int     `json:"host_node"`
	Bytes    int64   `json:"bytes"`
	Rep      int     `json:"rep"`
	Seconds  float64 `json:"seconds"`
	// The lowest PCIe generation and width either card showed while its group
	// of copies ran, and how many samples that rests on: the evidence the
	// figure was taken on a trained link and not an idle one.
	LinkSamples  int `json:"link_samples"`
	LinkGenMin   int `json:"link_gen_min"`
	LinkWidthMin int `json:"link_width_min"`
}

// GBps is the copy's rate in decimal gigabytes a second.
func (t Transfer) GBps() float64 { return float64(t.Bytes) / t.Seconds / 1e9 }

// PrefillRow is one request's prefill, as ops/pcie/prefill.py records it.
type PrefillRow struct {
	// Tokens is the prompt length sent, as token ids.
	Tokens int `json:"tokens"`
	Rep    int `json:"rep"`
	// ClientSeconds is the request's wall clock at the client, one output
	// token included.
	ClientSeconds float64 `json:"client_seconds"`
	// EnginePrefillSeconds is the change in vllm:request_prefill_time_seconds
	// across the request: from the scheduler first taking it to its first
	// token.
	EnginePrefillSeconds float64 `json:"engine_prefill_seconds"`
	EngineTTFTSeconds    float64 `json:"engine_ttft_seconds"`
	// RequestsCounted is how many requests the engine's histogram counted
	// across the request, which has to be one for the change to be this
	// request's.
	RequestsCounted int `json:"requests_counted"`
	// PromptTokens and CachedTokens are the engine's own usage account: how
	// many tokens it read and how many of them its prefix cache served. -1
	// means it did not say.
	PromptTokens int `json:"prompt_tokens"`
	CachedTokens int `json:"cached_tokens"`
}

// computedInFull reports whether this row is a whole prefill of the prompt it
// was sent, by one request, which is the only kind a prefill figure may rest
// on. A cached count the engine did not report is not a count of zero.
func (r PrefillRow) computedInFull() bool {
	return r.CachedTokens == 0 && r.RequestsCounted == 1 && r.PromptTokens == r.Tokens
}

// PrefillSummary is the prefill of one prompt length.
type PrefillSummary struct {
	Tokens int
	// Requests is how many rows the medians rest on, and Excluded how many
	// were set aside as not a whole prefill by one request.
	Requests             int
	Excluded             int
	EnginePrefillSeconds float64
	EngineTTFTSeconds    float64
	ClientSeconds        float64
}

// Inputs is everything the arithmetic is done from.
type Inputs struct {
	Geometry  KVGeometry
	Topology  gpu.Topology
	Transfers []Transfer
	Prefills  []PrefillRow
	// BusyLoopGBps is the busy condition's device-local copy rate, one per card
	// per busy group: the evidence those cards really were busy.
	BusyLoopGBps []float64
	// Runs is each bandwidth pass's own record of the conditions it ran under.
	Runs []BandwidthRun
}

// LinkSummary is one kind of copy across one class of link: every transfer of
// one path, class, condition and size.
type LinkSummary struct {
	Path string
	// Class is the pair's topology class for a copy between two cards. A copy
	// between a card and host memory has no pair to class, and is local or
	// remote by where the host buffer sat instead — see classOf.
	Class     string
	Condition string
	Bytes     int64
	Transfers int
	// TypicalGBps is the median transfer.
	TypicalGBps float64
	// SlowestGBps is the median of the slowest pair and host placement in the
	// class, which is what a fleet that put a transfer there would live with.
	SlowestGBps     float64
	SlowestSrc      int
	SlowestDst      int
	SlowestHostNode int
	LinkGenMin      int
	LinkWidthMin    int
}

// Analysis is the arithmetic, done.
type Analysis struct {
	Geometry KVGeometry
	Links    []LinkSummary
	Prefills []PrefillSummary
	// The evidence the conditions held, carried through so the report can
	// state it beside the figures.
	BusyLoopGBps []float64
	Runs         []BandwidthRun
}

// Link finds the summary of one kind of copy.
func (a Analysis) Link(path, class, condition string, bytes int64) (LinkSummary, bool) {
	for _, s := range a.Links {
		if s.Path == path && s.Class == class && s.Condition == condition && s.Bytes == bytes {
			return s, true
		}
	}
	return LinkSummary{}, false
}

// Prefill finds the prefill of one prompt length.
func (a Analysis) Prefill(tokens int) (PrefillSummary, bool) {
	for _, p := range a.Prefills {
		if p.Tokens == tokens {
			return p, true
		}
	}
	return PrefillSummary{}, false
}

// summarisePrefill takes each prompt length's medians over the rows that were a
// whole prefill by one request, shortest length first.
func summarisePrefill(rows []PrefillRow) []PrefillSummary {
	byTokens := map[int][]PrefillRow{}
	var lengths []int
	for _, r := range rows {
		if _, seen := byTokens[r.Tokens]; !seen {
			lengths = append(lengths, r.Tokens)
		}
		byTokens[r.Tokens] = append(byTokens[r.Tokens], r)
	}
	slices.Sort(lengths)

	out := make([]PrefillSummary, 0, len(lengths))
	for _, n := range lengths {
		s := PrefillSummary{Tokens: n}
		var engine, ttft, client []float64
		for _, r := range byTokens[n] {
			if !r.computedInFull() {
				s.Excluded++
				continue
			}
			engine = append(engine, r.EnginePrefillSeconds)
			ttft = append(ttft, r.EngineTTFTSeconds)
			client = append(client, r.ClientSeconds)
		}
		s.Requests = len(engine)
		s.EnginePrefillSeconds, s.EngineTTFTSeconds, s.ClientSeconds = median(engine), median(ttft), median(client)
		out = append(out, s)
	}
	return out
}

type linkKey struct {
	path, class, condition string
	bytes                  int64
}

type placement struct{ src, dst, hostNode int }

// Analyse does the arithmetic.
func Analyse(in Inputs) (Analysis, error) {
	groups := map[linkKey][]Transfer{}
	var order []linkKey
	for _, t := range in.Transfers {
		class, err := classOf(in.Topology, t)
		if err != nil {
			return Analysis{}, err
		}
		// The row's own link is a check on the matrix, never a substitute for
		// it. Disagreeing, the two came off different hosts or numbered the
		// cards differently, and every class would hold the wrong pairs.
		if t.Link != "" && t.Src >= 0 && t.Dst >= 0 && t.Link != class {
			return Analysis{}, fmt.Errorf("disagg: the copy %d→%d was recorded as crossing %s, and the topology says %s",
				t.Src, t.Dst, t.Link, class)
		}
		k := linkKey{t.Path, class, t.Condition, t.Bytes}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], t)
	}

	a := Analysis{Geometry: in.Geometry, Prefills: summarisePrefill(in.Prefills), BusyLoopGBps: in.BusyLoopGBps, Runs: in.Runs}
	for _, k := range order {
		a.Links = append(a.Links, summarise(k, groups[k]))
	}
	if err := a.covered(); err != nil {
		return Analysis{}, err
	}
	return a, nil
}

// covered checks that every prompt length measured has both halves of the
// arithmetic: at least one whole prefill, and its own KV size moved card to
// card, alone. A length missing either would otherwise drop out of the answer
// without a word, the table simply a row shorter.
func (a Analysis) covered() error {
	costs := a.Costs()
	for _, p := range a.Prefills {
		if p.Requests == 0 {
			return fmt.Errorf("disagg: none of the %d requests at %d tokens was a whole prefill counted once, so there is "+
				"no prefill to set its transfer against; was ENABLE_PROMPT_TOKENS_DETAILS on?", p.Excluded, p.Tokens)
		}
		moved := slices.ContainsFunc(costs, func(c Cost) bool { return c.Tokens == p.Tokens && c.Condition == "alone" })
		if !moved {
			return fmt.Errorf("disagg: %d tokens is %d bytes of KV, and no card-to-card copy of that size was measured alone; "+
				"the lengths and sizes in ops/pcie/measure.sh have drifted apart",
				p.Tokens, int64(p.Tokens)*a.Geometry.BytesPerToken())
		}
	}
	return nil
}

// Cost is what moving one request's KV across one class of link costs, set
// against the prefill that built it. It is §8's plain case: the transfer starts
// when prefill ends, with none of it overlapped.
type Cost struct {
	Tokens         int
	KVBytes        int64
	PrefillSeconds float64
	Path           string
	Class          string
	Condition      string
	// TypicalSeconds moves the KV at the class's typical bandwidth, and
	// WorstSeconds at its slowest pair's.
	TypicalSeconds float64
	WorstSeconds   float64
}

// WorstShare is the slowest pair's transfer as a share of the prefill before
// it.
func (c Cost) WorstShare() float64 { return c.WorstSeconds / c.PrefillSeconds }

// Costs sets every measured prompt length's KV against every card-to-card link
// measured at exactly that many bytes. Derived on demand from the summaries, so
// it cannot disagree with them.
func (a Analysis) Costs() []Cost {
	var out []Cost
	for _, p := range a.Prefills {
		if p.Requests == 0 {
			continue // no whole prefill to set a transfer against
		}
		kv := int64(p.Tokens) * a.Geometry.BytesPerToken()
		for _, l := range a.Links {
			if l.Bytes != kv || !cardToCard(l.Path) {
				continue
			}
			out = append(out, Cost{
				Tokens: p.Tokens, KVBytes: kv, PrefillSeconds: p.EnginePrefillSeconds,
				Path: l.Path, Class: l.Class, Condition: l.Condition,
				TypicalSeconds: float64(kv) / (l.TypicalGBps * 1e9),
				WorstSeconds:   float64(kv) / (l.SlowestGBps * 1e9),
			})
		}
	}
	return out
}

// Cost finds one prompt length's transfer over one kind of copy.
func (a Analysis) Cost(tokens int, path, class, condition string) (Cost, bool) {
	for _, c := range a.Costs() {
		if c.Tokens == tokens && c.Path == path && c.Class == class && c.Condition == condition {
			return c, true
		}
	}
	return Cost{}, false
}

// cardToCard reports whether a path moves bytes between two cards, rather than
// between one card and host memory.
func cardToCard(path string) bool { return path == "peer" || path == "bounce" }

// classOf groups a copy by what it crossed: for two cards, the pair's topology
// class, which is the host matrix's to name. A copy between one card and host
// memory crosses no such pair, and is classed local or remote by whether the
// host buffer sat on that card's own NUMA node — the two legs a card-to-card
// bounce is built from.
func classOf(topo gpu.Topology, t Transfer) (string, error) {
	if t.Src < 0 || t.Dst < 0 {
		card := max(t.Src, t.Dst)
		node, known := topo.NUMANodeOf(card)
		switch {
		case !known:
			return "", fmt.Errorf("disagg: the topology does not know card %d", card)
		case node == t.HostNode:
			return "local", nil
		default:
			return "remote", nil
		}
	}
	class, known := topo.LinkBetween(t.Src, t.Dst)
	if !known {
		return "", fmt.Errorf("disagg: the topology does not know the pair %d→%d", t.Src, t.Dst)
	}
	return class, nil
}

func summarise(k linkKey, transfers []Transfer) LinkSummary {
	s := LinkSummary{Path: k.path, Class: k.class, Condition: k.condition, Bytes: k.bytes, Transfers: len(transfers)}

	rates := make([]float64, 0, len(transfers))
	byPlacement := map[placement][]float64{}
	var placements []placement
	sampled := false
	for _, t := range transfers {
		rates = append(rates, t.GBps())
		p := placement{t.Src, t.Dst, t.HostNode}
		if _, seen := byPlacement[p]; !seen {
			placements = append(placements, p)
		}
		byPlacement[p] = append(byPlacement[p], t.GBps())
		// A group the sampler caught no sample of says nothing about its link,
		// and must not read as one that ran at generation zero.
		if t.LinkSamples == 0 {
			continue
		}
		if !sampled || t.LinkGenMin < s.LinkGenMin {
			s.LinkGenMin = t.LinkGenMin
		}
		if !sampled || t.LinkWidthMin < s.LinkWidthMin {
			s.LinkWidthMin = t.LinkWidthMin
		}
		sampled = true
	}
	s.TypicalGBps = median(rates)

	// In the order the placements were measured, so a tie names the same pair
	// on every run rather than whichever a map yields first.
	for i, p := range placements {
		if m := median(byPlacement[p]); i == 0 || m < s.SlowestGBps {
			s.SlowestGBps, s.SlowestSrc, s.SlowestDst, s.SlowestHostNode = m, p.src, p.dst, p.hostNode
		}
	}
	return s
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.SortFunc(s, cmp.Compare[float64])
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}
