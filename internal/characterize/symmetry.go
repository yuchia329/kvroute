package characterize

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/gpu"
)

// DefaultSymmetryTolerance is how far replicas may differ before the fleet
// stops being interchangeable.
//
// Eight percent, from idea.md §10. The number is not about noise: it is the
// point at which a systematic difference between replicas is large enough to
// show up as a difference between policies. Round-robin touches all six evenly
// while prefix affinity concentrates on whichever ones hold the right blocks,
// so a fleet with a fast corner would hand the concentrating policy a win that
// belongs to the host.
const DefaultSymmetryTolerance = 0.08

// Escalation is the ladder idea.md §10 climbs when the replicas are not
// interchangeable.
type Escalation string

const (
	// EscalationNone is the outcome when the fleet is already symmetric.
	EscalationNone Escalation = "none"
	// EscalationPinCPUs is the first rung: give every replica the same number
	// of cores on its own card's NUMA node, with numactl.
	EscalationPinCPUs Escalation = "pin_cpus"
	// EscalationSingleNUMANode is the second rung, taken only if pinning does
	// not settle it: run four symmetric replicas on GPUs 0-3 rather than six
	// confounded ones.
	//
	// Nothing here selects it. A single characterization cannot know whether
	// pinning has already been tried, so this is the value an operator records
	// after climbing to it, and the name that keeps the two rungs from being
	// described in two different ways.
	EscalationSingleNUMANode Escalation = "single_numa_node"
)

// Placement is where a replica's card sits on the host. It is what makes a
// latency difference between replicas interpretable rather than merely visible.
type Placement struct {
	ReplicaID string `json:"replica_id"`
	GPUIndex  int    `json:"gpu_index"`
	NUMANode  int    `json:"numa_node"`
}

// trailingIndex pulls the GPU index out of a replica id such as "replica-4".
var trailingIndex = regexp.MustCompile(`\d+$`)

// Placements pairs each replica with the card it runs on and that card's NUMA
// node.
//
// The replica's own id carries the GPU index because ops/replica.sh is what
// assigns both: `ops/replica.sh up 4` starts replica-4 on GPU 4 with
// CUDA_VISIBLE_DEVICES=4. A replica whose id carries no index falls back to its
// position in the fleet, which is the order ops/fleet.sh emits them in.
func Placements(replicaIDs []string, topo gpu.Topology) []Placement {
	out := make([]Placement, 0, len(replicaIDs))
	for position, id := range replicaIDs {
		index := position
		if match := trailingIndex.FindString(id); match != "" {
			if parsed, err := strconv.Atoi(match); err == nil {
				index = parsed
			}
		}
		node, ok := topo.NUMANodeOf(index)
		if !ok {
			node = -1
		}
		out = append(out, Placement{ReplicaID: id, GPUIndex: index, NUMANode: node})
	}
	return out
}

// ReplicaLatency is one replica's latency at one load level, pooled over every
// repetition it was driven for.
type ReplicaLatency struct {
	Placement     `json:"placement"`
	bench.Summary `json:"summary"`
}

// NUMALatency is the mean of the replicas hanging off one node.
//
// Mean rather than median: there are four replicas on one node and two on the
// other, and the question is whether the node as a whole is slower, not which
// of its cards is typical.
type NUMALatency struct {
	Node        int     `json:"node"`
	Replicas    int     `json:"replicas"`
	MeanTTFTNs  int64   `json:"mean_ttft_ns"`
	MeanITLNs   int64   `json:"mean_itl_ns"`
	ThreadsEach float64 `json:"threads_per_gpu"`
}

// LevelComparison is every replica at one load level, side by side.
type LevelComparison struct {
	Concurrency int              `json:"concurrency"`
	Replicas    []ReplicaLatency `json:"replicas"`

	// TTFTSpread and ITLSpread are (slowest - fastest) / fastest across the
	// replicas: 0.08 means the slowest replica took 8% longer than the fastest.
	TTFTSpread float64 `json:"ttft_spread"`
	ITLSpread  float64 `json:"itl_spread"`
	Fastest    string  `json:"fastest"`
	Slowest    string  `json:"slowest"`

	// NUMA is the same comparison grouped by node, and NUMASpread is the gap
	// between the slowest and fastest node. It is reported separately because a
	// spread that follows the node boundary has a cause and a fix, and one that
	// does not is either noise or something else entirely.
	//
	// This, not the per-replica spread, is what CPU pinning answers. Pinning
	// equalises what each node's replicas get; it does nothing for one slow
	// card, which is a different problem with a different fix.
	NUMA       []NUMALatency `json:"numa"`
	NUMASpread float64       `json:"numa_spread"`
	// NUMARepeatSpread and NUMAResolved are the same noise-floor test applied
	// between nodes: how much a node's own mean moved across repetitions, and
	// whether the gap between nodes clears it or is inside the tolerance.
	NUMARepeatSpread float64 `json:"numa_repeat_spread"`
	NUMAResolved     bool    `json:"numa_resolved"`
	// NUMASymmetric is whether the nodes are within tolerance of each other.
	// -1 spreads mean there is only one node and nothing to compare, in which
	// case this is true and NUMAResolved is false.
	NUMASymmetric bool `json:"numa_symmetric"`

	// RepeatSpread is the largest gap any single replica showed against itself
	// between its own repetitions. It is the level's noise floor: a difference
	// between replicas smaller than a replica's own wobble is not a difference
	// between replicas. Negative when there was only one repetition and there
	// is therefore nothing to estimate it from.
	RepeatSpread float64 `json:"repeat_spread"`
	// Resolved is whether this level settled the question it was asked, which is
	// not "are the replicas identical" but "do they differ by more than the
	// tolerance". Two different things settle it: a spread that clears the
	// noise floor, which is a real difference; and a noise floor that is itself
	// inside the tolerance, which means the measurement is precise enough to
	// rule an over-tolerance difference out even though it cannot resolve the
	// small one it sees.
	//
	// It is false when neither holds — when the noise is wider than the
	// tolerance and the spread is inside the noise — and then the level says
	// nothing either way: it has not found the replicas equal, and it has not
	// found them different.
	Resolved bool `json:"resolved"`
	// Silent names the replicas that produced no successful response at this
	// level. They are the reason a comparison can be missing rather than even.
	Silent []string `json:"silent,omitempty"`

	Symmetric bool `json:"symmetric"`
}

// Symmetry is the verdict on whether the six replicas are interchangeable.
type Symmetry struct {
	Tolerance float64           `json:"tolerance"`
	Levels    []LevelComparison `json:"levels"`
	Symmetric bool              `json:"symmetric"`
	// Resolved is false when some level could not tell a real difference from
	// its own noise. A verdict of "symmetric" from an unresolved level is not a
	// verdict, so this travels with it.
	Resolved   bool       `json:"resolved"`
	Escalation Escalation `json:"escalation"`
	Findings   []string   `json:"findings,omitempty"`
}

// CompareReplicas builds the symmetry verdict from rows gathered by driving
// each replica on its own.
//
// Rows are pooled per replica across repetitions rather than each repetition
// being compared separately: the question is whether a replica is
// systematically slower, and a per-repetition comparison answers whether it was
// slower once.
func CompareReplicas(rows []bench.Result, placements []Placement, topo gpu.Topology, tolerance float64) Symmetry {
	if tolerance <= 0 {
		tolerance = DefaultSymmetryTolerance
	}
	s := Symmetry{Tolerance: tolerance, Symmetric: true, Escalation: EscalationNone}

	byReplica := map[string]Placement{}
	for _, p := range placements {
		byReplica[p.ReplicaID] = p
	}
	threads := map[int]float64{}
	for _, group := range topo.NUMAGroups() {
		threads[group.Node] = group.ThreadsPerGPU
	}

	grouped := map[int]map[string][]bench.Result{}
	for _, row := range rows {
		if _, known := byReplica[row.Replica]; !known {
			continue
		}
		if grouped[row.Concurrency] == nil {
			grouped[row.Concurrency] = map[string][]bench.Result{}
		}
		grouped[row.Concurrency][row.Replica] = append(grouped[row.Concurrency][row.Replica], row)
	}

	s.Resolved = true
	nodeDifference := false
	for _, concurrency := range slices.Sorted(maps.Keys(grouped)) {
		level := compareLevel(concurrency, grouped[concurrency], placements, threads, tolerance)
		s.Levels = append(s.Levels, level)
		if !level.Resolved {
			s.Resolved = false
		}
		// Escalation follows the node comparison and not the replica one. CPU
		// pinning equalises what each node's replicas get; it is no answer at
		// all to a single slow card, which is a bad card or a bad slot.
		if !level.NUMASymmetric && level.NUMAResolved {
			nodeDifference = true
		}
		if level.Symmetric {
			continue
		}
		s.Symmetric = false
		s.Findings = append(s.Findings, level.findings(tolerance)...)
	}

	if len(s.Levels) == 0 {
		s.Symmetric, s.Resolved = false, false
		s.Findings = append(s.Findings, "no replica was driven on its own, so nothing was compared and the fleet's symmetry is unproven")
	}
	// Escalation follows only from a difference the measurement could actually
	// resolve. Pinning CPUs because one probe's median landed high is a change
	// to the engine configuration made on the strength of noise.
	if nodeDifference {
		s.Escalation = EscalationPinCPUs
	}
	return s
}

func compareLevel(concurrency int, rowsByReplica map[string][]bench.Result, placements []Placement, threads map[int]float64, tolerance float64) LevelComparison {
	byReplica := map[string]Placement{}
	for _, p := range placements {
		byReplica[p.ReplicaID] = p
	}
	level := LevelComparison{Concurrency: concurrency, Symmetric: true}

	// Negative until a replica shows two repetitions to compare: the noise floor
	// is unknown, not zero.
	level.RepeatSpread = -1
	// Iterated over placements rather than over the map, so the table reads in
	// replica order however the probes happened to be scheduled.
	for _, p := range placements {
		rows, ok := rowsByReplica[p.ReplicaID]
		if !ok {
			continue
		}
		level.Replicas = append(level.Replicas, ReplicaLatency{
			Placement: p,
			Summary:   bench.Summarize(rows, pooledOptions),
		})
		if own := repeatSpread(rows); own > level.RepeatSpread {
			level.RepeatSpread = own
		}
	}
	if len(level.Replicas) < 2 {
		// One replica is not a comparison. Leaving it symmetric by default
		// would be a claim about a fleet nothing was compared across.
		level.Symmetric, level.Resolved = false, false
		return level
	}

	fastest, slowest := level.Replicas[0], level.Replicas[0]
	byNode := map[int][]ReplicaLatency{}
	for _, r := range level.Replicas {
		if r.TTFTP50Ns < fastest.TTFTP50Ns {
			fastest = r
		}
		if r.TTFTP50Ns > slowest.TTFTP50Ns {
			slowest = r
		}
		byNode[r.NUMANode] = append(byNode[r.NUMANode], r)
	}
	level.Fastest, level.Slowest = fastest.ReplicaID, slowest.ReplicaID
	level.TTFTSpread = spread(level.Replicas, func(r ReplicaLatency) int64 { return r.TTFTP50Ns })
	level.ITLSpread = spread(level.Replicas, func(r ReplicaLatency) int64 { return r.ITLP50Ns })
	// A replica that answered nothing leaves the comparison with a hole in it.
	// Neither symmetric nor asymmetric: unmeasured.
	if level.TTFTSpread < 0 || level.ITLSpread < 0 {
		level.Symmetric, level.Resolved = false, false
		level.Silent = silentReplicas(level.Replicas)
		return level
	}

	for _, node := range slices.Sorted(maps.Keys(byNode)) {
		group := byNode[node]
		level.NUMA = append(level.NUMA, NUMALatency{
			Node:        node,
			Replicas:    len(group),
			MeanTTFTNs:  mean(group, func(r ReplicaLatency) int64 { return r.TTFTP50Ns }),
			MeanITLNs:   mean(group, func(r ReplicaLatency) int64 { return r.ITLP50Ns }),
			ThreadsEach: threads[node],
		})
	}
	level.NUMASpread = spread(level.NUMA, func(n NUMALatency) int64 { return n.MeanTTFTNs })
	level.NUMARepeatSpread = numaRepeatSpread(rowsByReplica, byReplica)
	level.NUMASymmetric = level.NUMASpread < 0 || level.NUMASpread <= tolerance
	level.NUMAResolved = level.NUMASpread >= 0 && level.NUMARepeatSpread >= 0 &&
		(level.NUMASpread > level.NUMARepeatSpread || level.NUMARepeatSpread <= tolerance)

	level.Symmetric = level.TTFTSpread <= tolerance && level.ITLSpread <= tolerance

	// A difference smaller than a replica's own variation between repetitions is
	// not a difference between replicas. Measured at concurrency 32 on this
	// fleet: 22% between replicas, and 36% for one replica against itself.
	//
	// But a spread inside the noise is only inconclusive when the noise is wide
	// enough to hide an over-tolerance difference. At concurrency 1 the fleet
	// came back with a 2.3% spread against a 2.8% noise floor: the measurement
	// cannot say which replica is faster, and it does not need to — both are far
	// enough inside the 8% tolerance that an 8% difference is ruled out. Calling
	// that unresolved would report the cleanest result the fleet produced as
	// though nothing had been learned.
	//
	// One repetition gives no estimate of the noise at all, so it settles
	// nothing either way. Treating "no estimate" as "the estimate is zero" would
	// make a single-repetition run the most confident one there is, which is
	// backwards.
	widest := max(level.TTFTSpread, level.ITLSpread)
	level.Resolved = level.RepeatSpread >= 0 &&
		(widest > level.RepeatSpread || level.RepeatSpread <= tolerance)
	return level
}

// numaRepeatSpread is how far a node's mean TTFT p50 moved between repetitions,
// taken over the worst-behaved node. It is the noise floor for the between-node
// comparison, and it is not the same number as the per-replica one: averaging a
// node's replicas cancels some of what makes an individual replica wobble, so a
// node comparison can resolve a difference a replica comparison cannot.
//
// Negative when there is one node, or one repetition, and therefore nothing to
// estimate it from.
func numaRepeatSpread(rowsByReplica map[string][]bench.Result, byReplica map[string]Placement) float64 {
	// node -> repetition -> the medians of that node's replicas
	byNode := map[int]map[int][]int64{}
	for replicaID, rows := range rowsByReplica {
		placement, known := byReplica[replicaID]
		if !known {
			continue
		}
		byRepetition := map[int][]bench.Result{}
		for _, row := range rows {
			byRepetition[row.Repetition] = append(byRepetition[row.Repetition], row)
		}
		for repetition, batch := range byRepetition {
			if byNode[placement.NUMANode] == nil {
				byNode[placement.NUMANode] = map[int][]int64{}
			}
			p50 := bench.Summarize(batch, pooledOptions).TTFTP50Ns
			byNode[placement.NUMANode][repetition] = append(byNode[placement.NUMANode][repetition], p50)
		}
	}
	if len(byNode) < 2 {
		return -1
	}

	worst := -1.0
	for _, byRepetition := range byNode {
		if len(byRepetition) < 2 {
			return -1
		}
		var means []NUMALatency
		for _, repetition := range slices.Sorted(maps.Keys(byRepetition)) {
			medians := byRepetition[repetition]
			total := int64(0)
			for _, v := range medians {
				total += v
			}
			means = append(means, NUMALatency{MeanTTFTNs: total / int64(len(medians))})
		}
		if own := spread(means, func(n NUMALatency) int64 { return n.MeanTTFTNs }); own > worst {
			worst = own
		}
	}
	return worst
}

// repeatSpread is how far one replica's TTFT p50 moved between its own
// repetitions at this level, as a fraction of its fastest.
//
// It is the only estimate of the measurement's own noise that costs nothing:
// the repetitions were run anyway, and identical load on the same replica is as
// close to a control as this experiment has. Zero when there was one
// repetition, in which case there is nothing to compare and the caller treats
// the noise floor as unknown.
func repeatSpread(rows []bench.Result) float64 {
	byRepetition := map[int][]bench.Result{}
	for _, row := range rows {
		byRepetition[row.Repetition] = append(byRepetition[row.Repetition], row)
	}
	if len(byRepetition) < 2 {
		return -1
	}
	var medians []ReplicaLatency
	for _, repetition := range slices.Sorted(maps.Keys(byRepetition)) {
		medians = append(medians, ReplicaLatency{Summary: bench.Summarize(byRepetition[repetition], pooledOptions)})
	}
	return spread(medians, func(r ReplicaLatency) int64 { return r.TTFTP50Ns })
}

// findings says which dimension broke the tolerance and by how much, and
// whether the difference follows the host's NUMA boundary — which is what
// decides whether pinning is the fix or whether something else is going on.
// silentReplicas names the replicas that produced no successful response.
func silentReplicas(replicas []ReplicaLatency) []string {
	var out []string
	for _, r := range replicas {
		if r.Successes == 0 || r.TTFTP50Ns <= 0 || r.ITLP50Ns <= 0 {
			out = append(out, r.ReplicaID)
		}
	}
	return out
}

func (l LevelComparison) findings(tolerance float64) []string {
	var out []string
	if len(l.Replicas) < 2 {
		return []string{fmt.Sprintf("at concurrency %d only %d replica was driven, so nothing was compared and symmetry at that level is unproven",
			l.Concurrency, len(l.Replicas))}
	}
	if len(l.Silent) > 0 {
		return []string{fmt.Sprintf("at concurrency %d, %v produced no successful response, so there is nothing to compare the rest against",
			l.Concurrency, l.Silent)}
	}
	if l.TTFTSpread > tolerance {
		out = append(out, fmt.Sprintf("at concurrency %d, TTFT p50 spread %.1f%% over the %.1f%% tolerance: %s at %v against %s at %v",
			l.Concurrency, l.TTFTSpread*100, tolerance*100,
			l.Slowest, l.durationOf(l.Slowest).Round(time.Millisecond),
			l.Fastest, l.durationOf(l.Fastest).Round(time.Millisecond)))
	}
	if l.ITLSpread > tolerance {
		out = append(out, fmt.Sprintf("at concurrency %d, inter-token latency spread %.1f%% over the %.1f%% tolerance",
			l.Concurrency, l.ITLSpread*100, tolerance*100))
	}
	if len(l.NUMA) > 1 && l.NUMASpread > tolerance {
		out = append(out, fmt.Sprintf("at concurrency %d the difference follows the NUMA boundary: %.1f%% between nodes, %s",
			l.Concurrency, l.NUMASpread*100, describeNodes(l.NUMA)))
	}
	switch {
	case len(l.Silent) > 0:
		out = append(out, fmt.Sprintf("at concurrency %d, %v produced no successful response, so there is nothing to compare the rest against",
			l.Concurrency, l.Silent))
	case l.RepeatSpread < 0:
		out = append(out, fmt.Sprintf("at concurrency %d there was one repetition, so nothing estimates the measurement's own noise and the spread cannot be told from it. Add repetitions before acting on it",
			l.Concurrency))
	case !l.Resolved:
		out = append(out, fmt.Sprintf("at concurrency %d that spread is not resolvable: one replica varies %.1f%% against itself between repetitions, wider than the %.1f%% tolerance, so the difference between replicas is inside the measurement's own noise. Lengthen the probe or add repetitions rather than acting on it",
			l.Concurrency, l.RepeatSpread*100, tolerance*100))
	}
	return out
}

func (l LevelComparison) durationOf(replicaID string) time.Duration {
	for _, r := range l.Replicas {
		if r.ReplicaID == replicaID {
			return time.Duration(r.TTFTP50Ns)
		}
	}
	return 0
}

func describeNodes(nodes []NUMALatency) string {
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		parts = append(parts, fmt.Sprintf("node %d holds %d replicas at %.0f threads each and a mean TTFT of %v",
			n.Node, n.Replicas, n.ThreadsEach, time.Duration(n.MeanTTFTNs)))
	}
	return strings.Join(parts, "; ")
}

// spread is (max - min) / min over a per-replica figure, or -1 when there is no
// spread to compute.
//
// Negative rather than zero for the missing cases, of which there are two and
// both would otherwise read as "perfectly even". A replica that produced no
// successful response has no latency to contribute, and returning zero for it
// would report the fleet as even on the strength of a replica nobody heard
// from. Fewer than two things is not a narrow spread either: a host with one
// NUMA node has no between-node comparison to make, and must not manufacture
// one out of comparing a node with itself.
func spread[T any](items []T, of func(T) int64) float64 {
	if len(items) < 2 {
		return -1
	}
	low, high := int64(0), int64(0)
	for i, item := range items {
		v := of(item)
		if v <= 0 {
			return -1
		}
		if i == 0 || v < low {
			low = v
		}
		if v > high {
			high = v
		}
	}
	return float64(high-low) / float64(low)
}

func mean[T any](items []T, of func(T) int64) int64 {
	if len(items) == 0 {
		return 0
	}
	total := int64(0)
	for _, item := range items {
		total += of(item)
	}
	return total / int64(len(items))
}
