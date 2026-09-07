package characterize

import (
	"fmt"
	"strings"
	"time"
)

// Report renders a characterization as the markdown that goes beside the rows.
//
// Every number here is recomputable from probes.jsonl and characterization.json;
// this is the reading of them, not the record of them. It leads with the
// capacity because every working set ratio scales off it, and it ends with the
// symmetry verdict because that is the one that can invalidate the comparison
// the whole project exists to make.
func Report(c Characterization) string {
	var b strings.Builder

	// Local rather than UTC: the record stores UTC, and the heading has to
	// agree with the date on the directory it lands in.
	fmt.Fprintf(&b, "# Characterization — %s\n\n", c.At.Local().Format("2006-01-02"))
	fmt.Fprintf(&b, "Model `%s`, workload `%s`, %d replicas driven individually.\n\n", c.Model, c.Workload, len(c.Capacity.Replicas))
	if c.Flagged {
		fmt.Fprintln(&b, "> ⚠️ **This characterization is flagged.** Every downstream figure scales off these")
		fmt.Fprintln(&b, "> numbers, so read the reasons before using any of them.")
		fmt.Fprintln(&b)
		for _, reason := range c.FlagReasons {
			fmt.Fprintf(&b, "> - %s\n", reason)
		}
		fmt.Fprintln(&b)
	}

	capacitySection(&b, c)
	topologySection(&b, c)
	floorSection(&b, c)
	symmetrySection(&b, c)
	return b.String()
}

func capacitySection(b *strings.Builder, c Characterization) {
	capacity := c.Capacity
	fmt.Fprintln(b, "## Aggregate fleet KV capacity")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "Read off every replica's own `vllm:cache_config_info`, not extrapolated from one.")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| replica | GPU | num_gpu_blocks | block size | tokens | KV bytes |")
	fmt.Fprintln(b, "|---|---:|---:|---:|---:|---:|")
	for i, r := range capacity.Replicas {
		// Falls back to the replica's position when no probe named its card,
		// which is the order ops/fleet.sh emits the fleet in.
		gpuIndex := i
		if index, known := gpuIndexOf(c, r.ReplicaID); known {
			gpuIndex = index
		}
		fmt.Fprintf(b, "| `%s` | %d | %s | %d | %s | %s |\n",
			r.ReplicaID, gpuIndex, commas(r.NumGPUBlocks), r.BlockSize, commas(r.Tokens), gib(r.Bytes))
	}
	fmt.Fprintf(b, "| **fleet** | | **%s** | | **%s** | **%s** |\n\n",
		commas(sumBlocks(capacity)), commas(capacity.Tokens), gib(capacity.Bytes))

	uniform := "every replica reports the same figure"
	if !capacity.Uniform {
		uniform = "**the replicas disagree**, so the aggregate hides a difference between cards"
	}
	fmt.Fprintf(b, "%s. At %s per token, that is %s of KV per replica.\n\n",
		capitalize(uniform), kib(capacity.BytesPerToken), gib(capacity.ImpliedKVBytesPerReplica))

	fmt.Fprintln(b, "### Against the figure computed by hand")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| | per replica | fleet |")
	fmt.Fprintln(b, "|---|---:|---:|")
	fmt.Fprintf(b, "| Measured | %s tokens | %s tokens |\n",
		commas(perReplicaTokens(capacity)), commas(capacity.Tokens))
	fmt.Fprintf(b, "| Hand-computed | %s tokens | %s tokens |\n",
		commas(capacity.EstimatedTokensPerReplica), commas(capacity.EstimatedTokens))
	fmt.Fprintf(b, "| Gap | %+.1f%% | %+.1f%% |\n\n", capacity.Gap*100, capacity.Gap*100)

	// The gap turned back into bytes, which is what makes it an explanation
	// rather than a percentage.
	fmt.Fprintf(b, "The arithmetic is %d layers x %d KV heads x %d dim x %d bytes x 2 (K,V) = %s per token. "+
		"That part is exact. The soft input is the KV budget: the hand figure assumed %s was left for KV after "+
		"weights, activations and CUDA graphs, and the engine actually left %s — the whole gap is that assumption, "+
		"not the per-token arithmetic.\n\n",
		capacity.Estimate.Geometry.Layers, capacity.Estimate.Geometry.KVHeads, capacity.Estimate.Geometry.HeadDim,
		capacity.Estimate.Geometry.BytesPerElement, kib(capacity.BytesPerToken),
		gib(capacity.Estimate.KVBudgetBytes), gib(capacity.ImpliedKVBytesPerReplica))

	fmt.Fprintln(b, "### Working set ratios rescaled off the measured total")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| WS | offered session tokens | sessions of 2k |")
	fmt.Fprintln(b, "|---:|---:|---:|")
	for _, ws := range c.WorkingSets {
		fmt.Fprintf(b, "| %g | %s | %s |\n", ws.Ratio, commas(ws.Tokens), commas(ws.Sessions))
	}
	fmt.Fprintln(b)
}

func topologySection(b *strings.Builder, c Characterization) {
	if len(c.Topology.GPUs) == 0 {
		fmt.Fprintln(b, "## Host GPU topology")
		fmt.Fprintln(b)
		fmt.Fprintln(b, "**Not recorded**: no GPU prober was available, so the symmetry result below cannot be read against the host.")
		fmt.Fprintln(b)
		return
	}
	fmt.Fprintln(b, "## Host GPU topology")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| node | GPUs | threads on the node | threads per GPU |")
	fmt.Fprintln(b, "|---:|---|---:|---:|")
	for _, g := range c.Topology.NUMAGroups() {
		fmt.Fprintf(b, "| %d | %v | %d | %.0f |\n", g.Node, g.GPUs, g.CPUThreads, g.ThreadsPerGPU)
	}
	fmt.Fprintln(b)
	fmt.Fprintln(b, "```")
	fmt.Fprintln(b, strings.TrimRight(c.Topology.Printable(), "\n"))
	fmt.Fprintln(b, "```")
	fmt.Fprintln(b)
}

func floorSection(b *strings.Builder, c Characterization) {
	fmt.Fprintln(b, "## Hardware latency floor, and the SLO derived from it")
	fmt.Fprintln(b)
	fmt.Fprintf(b, "One request at a time, straight at each replica, %d successful requests pooled across %d replicas.\n\n",
		c.Floor.Successes, c.Floor.Replicas)
	// The floor is only a floor if the prompts were actually prefilled, and
	// that is the engine's counters to say, not the latency's.
	if c.Floor.PrefixCache.Read {
		fmt.Fprintf(b, "%.1f%% of the prompt tokens behind this figure came out of the replicas' prefix caches, against a %.0f%% limit — "+
			"so this is the cost of prefilling, not of finding a prompt already there.\n\n",
			c.Floor.PrefixCache.HitRate()*100, c.Floor.MaxPrefixHitRate*100)
	} else {
		fmt.Fprintln(b, "**The prefix-cache counters behind this figure could not be read**, so there is no evidence it measured prefill rather than cache hits.")
		fmt.Fprintln(b)
	}
	fmt.Fprintln(b, "| | p50 | p95 | p99 |")
	fmt.Fprintln(b, "|---|---:|---:|---:|")
	fmt.Fprintf(b, "| TTFT | %s | %s | %s |\n", ms(c.Floor.TTFTP50Ns), ms(c.Floor.TTFTP95Ns), ms(c.Floor.TTFTP99Ns))
	fmt.Fprintf(b, "| Inter-token | %s | %s | — |\n\n", ms(c.Floor.ITLP50Ns), ms(c.Floor.ITLP95Ns))

	fmt.Fprintf(b, "**SLO: %s**\n\n", c.SLO.String())
	fmt.Fprintln(b, "The multiple is a judgement and the floor is not, so the alternatives are published beside it:")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| multiple | TTFT | inter-token p50 |")
	fmt.Fprintln(b, "|---:|---:|---:|")
	for _, candidate := range c.SLOCandidates {
		chosen := ""
		if candidate.Multiple == c.SLO.Multiple {
			chosen = " ← chosen"
		}
		fmt.Fprintf(b, "| %gx | %v | %v%s |\n", candidate.Multiple, candidate.TTFT, candidate.ITL, chosen)
	}
	fmt.Fprintln(b)
}

func symmetrySection(b *strings.Builder, c Characterization) {
	s := c.Symmetry
	fmt.Fprintln(b, "## Replica symmetry")
	fmt.Fprintln(b)
	verdict := fmt.Sprintf("**The six replicas are interchangeable** within the %.0f%% tolerance.", s.Tolerance*100)
	switch {
	case !s.Symmetric && s.Escalation != EscalationNone:
		verdict = fmt.Sprintf("**The replicas are not interchangeable** at the %.0f%% tolerance, by a difference every repetition agreed on. Escalation: `%s`.",
			s.Tolerance*100, s.Escalation)
	case !s.Symmetric:
		verdict = fmt.Sprintf("**Unresolved.** Some level's spread is over the %.0f%% tolerance, but not by more than the measurement's own noise, so it is neither a difference between replicas nor evidence that there is none. No escalation follows from it.",
			s.Tolerance*100)
	case !s.Resolved:
		verdict = fmt.Sprintf("**The replicas are interchangeable within the %.0f%% tolerance at every level that could resolve one** — see the unresolved levels below.", s.Tolerance*100)
	}
	fmt.Fprintln(b, verdict)
	fmt.Fprintln(b)
	for _, finding := range s.Findings {
		fmt.Fprintf(b, "- %s\n", finding)
	}
	if len(s.Findings) > 0 {
		fmt.Fprintln(b)
	}

	for _, level := range s.Levels {
		fmt.Fprintf(b, "### Concurrency %d\n\n", level.Concurrency)
		fmt.Fprintln(b, "| replica | GPU | NUMA | reqs | TTFT p50 | TTFT p95 | ITL p50 | tput/s | prefix hits |")
		fmt.Fprintln(b, "|---|---:|---:|---:|---:|---:|---:|---:|---:|")
		for _, r := range level.Replicas {
			fmt.Fprintf(b, "| `%s` | %d | %d | %d | %s | %s | %s | %.2f | %s |\n",
				r.ReplicaID, r.GPUIndex, r.NUMANode, r.Requests,
				ms(r.TTFTP50Ns), ms(r.TTFTP95Ns), ms(r.ITLP50Ns), r.ThroughputRPS,
				hitRate(c, level.Concurrency, r.ReplicaID))
		}
		fmt.Fprintln(b)
		fmt.Fprintf(b, "TTFT p50 spread **%.1f%%** (%s slowest, %s fastest), inter-token spread **%.1f%%**",
			level.TTFTSpread*100, level.Slowest, level.Fastest, level.ITLSpread*100)
		if len(level.NUMA) > 1 {
			fmt.Fprintf(b, ", between NUMA nodes **%.1f%%**", level.NUMASpread*100)
		}
		fmt.Fprintf(b, " — %s.\n\n", passFail(level.Symmetric, s.Tolerance))
		// The noise floor beside the difference, always: a spread reported
		// without it cannot be told from the measurement's own scatter.
		switch {
		case level.RepeatSpread < 0:
			fmt.Fprintln(b, "One repetition, so there is no estimate of this level's own noise to read the spread against.")
		case level.Resolved:
			fmt.Fprintf(b, "A replica varies %.1f%% against itself between repetitions, so a %.1f%% spread between replicas is larger than the measurement's own noise.\n",
				level.RepeatSpread*100, level.TTFTSpread*100)
		default:
			fmt.Fprintf(b, "**Not resolvable at this sample size**: one replica varies %.1f%% against *itself* between repetitions, more than the %.1f%% between replicas. Lengthen the probe or add repetitions rather than acting on it.\n",
				level.RepeatSpread*100, level.TTFTSpread*100)
		}
		fmt.Fprintln(b)
	}
}

// hitRate is how much of one replica's prompt work at one level came out of its
// prefix cache, pooled over that level's repetitions. A latency table without
// it cannot be read: a replica that answered from cache looks like a fast one.
func hitRate(c Characterization, concurrency int, replicaID string) string {
	pooled := PoolPrefixCache(c.Probes, func(p Probe) bool {
		return p.Concurrency == concurrency && p.ReplicaID == replicaID
	})
	if !pooled.Read || pooled.Queries <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", pooled.HitRate()*100)
}

func passFail(ok bool, tolerance float64) string {
	if ok {
		return fmt.Sprintf("inside the %.0f%% tolerance", tolerance*100)
	}
	return fmt.Sprintf("**outside the %.0f%% tolerance**", tolerance*100)
}

// gpuIndexOf is the card a replica ran on, as the probes recorded it.
func gpuIndexOf(c Characterization, replicaID string) (int, bool) {
	for _, probe := range c.Probes {
		if probe.ReplicaID == replicaID {
			return probe.GPUIndex, true
		}
	}
	return 0, false
}

func perReplicaTokens(c Capacity) int {
	if len(c.Replicas) == 0 {
		return 0
	}
	return c.Tokens / len(c.Replicas)
}

func sumBlocks(c Capacity) int {
	total := 0
	for _, r := range c.Replicas {
		total += r.NumGPUBlocks
	}
	return total
}

func ms(ns int64) string {
	if ns == 0 {
		return "—"
	}
	d := time.Duration(ns)
	if d < time.Millisecond {
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%.0fms", float64(d)/float64(time.Millisecond))
}

func gib(b int64) string {
	return fmt.Sprintf("%.2f GiB", float64(b)/float64(GiB))
}

// kib renders a byte count in KiB, falling back to bytes when it is not a whole
// number of them.
func kib(b int64) string {
	if b%1024 == 0 {
		return fmt.Sprintf("%d KiB", b/1024)
	}
	return fmt.Sprintf("%d B", b)
}

// commas groups a count for reading: these are six-figure numbers that get
// quoted, and 125952 is harder to check against an engine log than 125,952.
func commas(n int) string {
	digits := fmt.Sprintf("%d", n)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
