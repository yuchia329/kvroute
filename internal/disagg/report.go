package disagg

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const mib = 1 << 20

// Nearest first, the way nvidia-smi's legend reads, so a table runs from the
// shortest path to the longest.
var (
	classOrder     = map[string]int{"local": 0, "remote": 1, "PIX": 2, "PXB": 3, "PHB": 4, "NODE": 5, "SYS": 6}
	conditionOrder = map[string]int{"alone": 0, "busy": 1, "together": 2}
	pathOrder      = map[string]int{"h2d": 0, "d2h": 1, "peer": 2, "bounce": 3}
)

// Report renders the arithmetic as markdown: what a token of KV costs and why,
// what the host's links carried and under what conditions, and what moving a
// request's KV costs set against the prefill that built it.
func (a Analysis) Report() string {
	var b strings.Builder
	b.WriteString("# KV transfer against prefill\n\n")
	b.WriteString("idea.md §8's arithmetic, rebuilt by `cmd/disagg` from the rows beside it: what a request's KV cache " +
		"would cost to move from the card that prefilled it to another, set against that prefill. Nothing here moved " +
		"KV; the copies moved bytes of the same size.\n\n")
	a.writeConditions(&b)
	a.writeGeometry(&b)
	a.writePeerAccess(&b)
	a.writeLinkEvidence(&b)
	a.writeLinks(&b, "Card and host memory", "Each card's own link, the leg every card-to-card copy below is built from. "+
		"These have no pair to class, so *local* is a host buffer on the card's own NUMA node and *remote* one on the "+
		"other node.", false)
	a.writeLinks(&b, "Card to card", "*peer* is CUDA's own copy, which the driver stages through host memory when peer "+
		"access is off; *bounce* stages it by hand through pinned host memory, chunked and pipelined. The slowest "+
		"pair is the median of the slowest (pair, host node) in its class.", true)
	a.writePrefill(&b)
	a.writeCosts(&b)
	return b.String()
}

func (a Analysis) writeConditions(b *strings.Builder) {
	if len(a.Runs) == 0 || a.Runs[0].Host == "" {
		return
	}
	r := a.Runs[0]
	fmt.Fprintf(b, "Measured on %s — driver %s, torch %s, CUDA %s — from %s, in %d passes, one per NUMA node the host "+
		"buffer was bound to.\n\n", r.Host, r.Driver, r.Torch, r.CUDA, r.Started, len(a.Runs))
}

func (a Analysis) writeGeometry(b *strings.Builder) {
	g := a.Geometry
	fmt.Fprintf(b, "## A token's KV\n\n2 (K and V) × %d layers × %d KV heads × %d head dim × %d bytes (%s) = **%s bytes**, "+
		"%s KiB a token, read off the model's own config.json. The engine keeps its cache in the model's dtype "+
		"(`kv_cache_dtype=auto`).\n\n",
		g.Layers, g.KVHeads, g.HeadDim, g.DTypeBytes, g.DType, thousands(g.BytesPerToken()), thousands(g.BytesPerToken()/1024))
}

func (a Analysis) writePeerAccess(b *strings.Builder) {
	checked := map[string]bool{}
	var direct []string
	for _, r := range a.Runs {
		for pair, ok := range r.PeerAccess {
			if _, seen := checked[pair]; !seen && ok {
				direct = append(direct, pair)
			}
			checked[pair] = ok
		}
	}
	slices.Sort(direct)
	b.WriteString("## Peer access\n\n")
	switch {
	case len(checked) == 0:
		b.WriteString("No pass recorded whether any pair could reach the other directly.\n\n")
	case len(direct) == 0:
		fmt.Fprintf(b, "Torch reported direct peer access for **no pair** of the %d ordered pairs checked, so every "+
			"card-to-card copy here went through host memory.\n\n", len(checked))
	default:
		fmt.Fprintf(b, "Torch reported direct peer access for %d of the %d ordered pairs checked: %s.\n\n",
			len(direct), len(checked), strings.Join(direct, ", "))
	}
}

func (a Analysis) writeLinkEvidence(b *strings.Builder) {
	gen, width, unsampled := 0, 0, 0
	for _, l := range a.Links {
		if l.LinkGenMin == 0 {
			unsampled++
			continue
		}
		if gen == 0 || l.LinkGenMin < gen {
			gen = l.LinkGenMin
		}
		if width == 0 || l.LinkWidthMin < width {
			width = l.LinkWidthMin
		}
	}
	b.WriteString("## The links the copies ran on\n\n")
	fmt.Fprintf(b, "Sampled through NVML while every group of copies ran, the lowest link either card showed was **%s**. "+
		"Idle, the same cards report gen 1; see `pcie-tree.txt`.", linkState(gen, width))
	if unsampled > 0 {
		fmt.Fprintf(b, " %d of %d groups finished between samples and say nothing either way.", unsampled, len(a.Links))
	}
	b.WriteString("\n\n")
	if len(a.BusyLoopGBps) > 0 {
		fmt.Fprintf(b, "In the *busy* condition, each card's own memory-bound copy loop ran at **%.1f GB/s** or more "+
			"throughout, so those cards really were busy. That loop stays inside the card's own memory: it loads the "+
			"card, not the link.\n\n", slices.Min(a.BusyLoopGBps))
	}
}

func (a Analysis) writeLinks(b *strings.Builder, title, intro string, cardToCardOnly bool) {
	var links []LinkSummary
	for _, l := range a.Links {
		if cardToCard(l.Path) == cardToCardOnly {
			links = append(links, l)
		}
	}
	slices.SortStableFunc(links, func(x, y LinkSummary) int {
		return cmp.Or(
			cmp.Compare(conditionOrder[x.Condition], conditionOrder[y.Condition]),
			cmp.Compare(pathOrder[x.Path], pathOrder[y.Path]),
			cmp.Compare(classOrder[x.Class], classOrder[y.Class]),
			cmp.Compare(x.Bytes, y.Bytes),
		)
	})
	fmt.Fprintf(b, "## %s\n\n%s GB/s are decimal: 10⁹ bytes a second.\n\n", title, intro)
	b.WriteString("| condition | path | class | size | transfers | typical GB/s | slowest GB/s | slowest on | link |\n")
	b.WriteString("|---|---|---|---:|---:|---:|---:|---|---|\n")
	for _, l := range links {
		fmt.Fprintf(b, "| %s | %s | %s | %s | %d | %.2f | %.2f | %s | %s |\n",
			l.Condition, l.Path, l.Class, mibs(l.Bytes), l.Transfers, l.TypicalGBps, l.SlowestGBps,
			slowestOn(l), linkState(l.LinkGenMin, l.LinkWidthMin))
	}
	b.WriteString("\n")
}

func (a Analysis) writePrefill(b *strings.Builder) {
	b.WriteString("## Prefill\n\nOne replica, one request at a time, every prompt unseen. *engine prefill* is " +
		"`vllm:request_prefill_time_seconds`: from the scheduler taking the request to its first token. A request the " +
		"cache served any of, or the engine did not count exactly once, is excluded.\n\n")
	b.WriteString("| tokens | requests | excluded | engine prefill | engine TTFT | client |\n")
	b.WriteString("|---:|---:|---:|---:|---:|---:|\n")
	for _, p := range a.Prefills {
		fmt.Fprintf(b, "| %s | %d | %d | %s | %s | %s |\n",
			thousands(int64(p.Tokens)), p.Requests, p.Excluded, ms(p.EnginePrefillSeconds), ms(p.EngineTTFTSeconds), ms(p.ClientSeconds))
	}
	b.WriteString("\n")
}

func (a Analysis) writeCosts(b *strings.Builder) {
	costs := a.Costs()
	slices.SortStableFunc(costs, func(x, y Cost) int {
		return cmp.Or(
			cmp.Compare(x.Tokens, y.Tokens),
			cmp.Compare(conditionOrder[x.Condition], conditionOrder[y.Condition]),
			cmp.Compare(pathOrder[x.Path], pathOrder[y.Path]),
			cmp.Compare(classOrder[x.Class], classOrder[y.Class]),
		)
	})
	b.WriteString("## Transfer against prefill\n\nA request's KV moved once its prefill has finished, none of it " +
		"overlapped: tokens × a token's KV, over the bandwidth measured at exactly that size.\n\n")
	b.WriteString("| tokens | KV | prefill | condition | path | class | typical | slowest pair | slowest / prefill |\n")
	b.WriteString("|---:|---:|---:|---|---|---|---:|---:|---:|\n")
	for _, c := range costs {
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s | %s | %s | %.1f%% |\n",
			thousands(int64(c.Tokens)), mibs(c.KVBytes), ms(c.PrefillSeconds), c.Condition, c.Path, c.Class,
			ms(c.TypicalSeconds), ms(c.WorstSeconds), c.WorstShare()*100)
	}
	b.WriteString("\n")
}

// slowestOn names where a class's slowest median was measured.
func slowestOn(l LinkSummary) string {
	if !cardToCard(l.Path) {
		return fmt.Sprintf("card %d, node %d", max(l.SlowestSrc, l.SlowestDst), l.SlowestHostNode)
	}
	return fmt.Sprintf("%d→%d, node %d", l.SlowestSrc, l.SlowestDst, l.SlowestHostNode)
}

func linkState(gen, width int) string {
	if gen == 0 {
		return "unsampled"
	}
	return fmt.Sprintf("gen %d x%d", gen, width)
}

func ms(seconds float64) string { return fmt.Sprintf("%.1f ms", seconds*1e3) }

func mibs(bytes int64) string {
	if bytes%mib == 0 {
		return fmt.Sprintf("%d MiB", bytes/mib)
	}
	return fmt.Sprintf("%.1f MiB", float64(bytes)/mib)
}

// thousands writes n with a comma between each group of three digits.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
