package gpu

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Topology is how the host places its GPUs: which links connect each pair, and
// which NUMA node's cores each card is attached to.
//
// It is recorded beside every characterization because the replica symmetry
// result is not interpretable without it. Six identical 3090s can still deliver
// different latency, and the reason is host-side: vLLM's per-step CPU work runs
// on the cores of whichever NUMA node the card hangs off, so a node carrying
// four cards gives each of them half the threads a node carrying two does. A
// symmetry number with no topology beside it cannot say whether a difference is
// noise or the host.
//
// The link matrix is kept for the same reason in the other direction: the PCIe
// links matter for the disaggregated-prefill arithmetic, which moves KV blocks
// between cards, and essentially nowhere else. Recording it now costs one
// command; recording it later means it describes a host that may have changed.
type Topology struct {
	GPUs []GPUTopology `json:"gpus"`
	// Raw is `nvidia-smi topo -m` verbatim, legend included, so the parse above
	// can always be checked against what the driver actually said.
	Raw string `json:"raw"`
}

// GPUTopology is one card's placement.
type GPUTopology struct {
	Index int `json:"index"`
	// Links is how this card reaches each other card, indexed by that card's
	// index: PIX, NODE, SYS, one of nvidia-smi's own names. Its own entry is X.
	Links []string `json:"links"`
	// CPUAffinity is the core list the driver reports for this card, and
	// CPUThreads is how many threads that list holds.
	CPUAffinity string `json:"cpu_affinity"`
	CPUThreads  int    `json:"cpu_threads"`
	// NUMANode is the node the card is attached to, or -1 when the driver
	// reports none.
	NUMANode int `json:"numa_node"`
}

// NUMAGroup is the set of cards sharing one NUMA node's cores.
//
// ThreadsPerGPU is the quantity the symmetry check is about: it is the host's
// asymmetry stated as a number, and it is what tells a measured latency
// difference apart from a host that was never going to be even.
type NUMAGroup struct {
	Node          int     `json:"node"`
	GPUs          []int   `json:"gpus"`
	CPUThreads    int     `json:"cpu_threads"`
	ThreadsPerGPU float64 `json:"threads_per_gpu"`
}

// NUMAGroups groups the cards by the node they hang off, in node order.
func (t Topology) NUMAGroups() []NUMAGroup {
	var groups []NUMAGroup
	byNode := map[int]int{}
	for _, g := range t.GPUs {
		at, seen := byNode[g.NUMANode]
		if !seen {
			byNode[g.NUMANode] = len(groups)
			groups = append(groups, NUMAGroup{Node: g.NUMANode, CPUThreads: g.CPUThreads})
			at = len(groups) - 1
		}
		groups[at].GPUs = append(groups[at].GPUs, g.Index)
	}
	for i := range groups {
		groups[i].ThreadsPerGPU = float64(groups[i].CPUThreads) / float64(len(groups[i].GPUs))
	}
	return groups
}

// Printable is the raw output with the driver's escape sequences removed:
// invisible in a terminal, and garbage anywhere the matrix is quoted. The Raw
// field keeps them, because what the driver said is what the driver said.
func (t Topology) Printable() string {
	return ansi.ReplaceAllString(t.Raw, "")
}

// NUMANodeOf reports which node a card hangs off, and whether the topology
// knows about that card at all.
func (t Topology) NUMANodeOf(index int) (int, bool) {
	for _, g := range t.GPUs {
		if g.Index == index {
			return g.NUMANode, true
		}
	}
	return 0, false
}

// Topology reads the host's GPU interconnect and NUMA placement.
func (p *Prober) Topology(ctx context.Context) (Topology, error) {
	out, err := p.run(ctx, "nvidia-smi", "topo", "-m")
	if err != nil {
		return Topology{}, err
	}
	return ParseTopology(string(out))
}

// ansi matches the escape sequences nvidia-smi underlines its header row with.
// They are in the output whether or not stdout is a terminal, so the parse has
// to strip them rather than assume they are absent.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// ParseTopology parses `nvidia-smi topo -m`.
//
// Columns are located by their header name rather than by position, because a
// host with InfiniBand adapters gets NIC columns and rows that this fleet's
// does not, and a positional parse would silently read one of them as a link.
func ParseTopology(out string) (Topology, error) {
	raw := ansi.ReplaceAllString(out, "")

	var header []string
	var gpus []GPUTopology
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Legend") {
			break
		}
		fields := tabFields(line)
		if len(fields) == 0 {
			continue
		}
		// The header row is the one that names the columns; every row after it
		// starts with its own device name.
		if header == nil {
			if _, isCard := gpuIndex(fields[0]); !isCard {
				continue
			}
			header = fields
			continue
		}
		if _, isCard := gpuIndex(fields[0]); !isCard {
			continue // a NIC row, which this fleet has none of and nothing reads
		}
		gpu, err := parseTopologyRow(header, fields)
		if err != nil {
			return Topology{}, err
		}
		gpus = append(gpus, gpu)
	}
	if len(gpus) == 0 {
		return Topology{}, fmt.Errorf("gpu: nvidia-smi topo -m named no GPUs:\n%s", raw)
	}
	return Topology{GPUs: gpus, Raw: out}, nil
}

// parseTopologyRow reads one card's row against the header that names its
// columns. Data rows carry the device name first, so a column at header index i
// is at data index i+1.
func parseTopologyRow(header, fields []string) (GPUTopology, error) {
	index, isCard := gpuIndex(fields[0])
	if !isCard {
		return GPUTopology{}, fmt.Errorf("gpu: %q is not a GPU label in nvidia-smi topo -m output", fields[0])
	}
	g := GPUTopology{Index: index, NUMANode: -1}

	links := map[int]string{}
	highest := -1
	for i, column := range header {
		if i+1 >= len(fields) {
			break
		}
		value := fields[i+1]
		switch peer, isCard := gpuIndex(column); {
		case isCard:
			links[peer] = value
			highest = max(highest, peer)
		case column == "CPU Affinity":
			g.CPUAffinity = value
			g.CPUThreads = countThreads(value)
		case column == "NUMA Affinity":
			// N/A on a host that does not report node affinity. Left at -1
			// rather than defaulted to 0, which would claim every card is on
			// the same node and make the symmetry check unfalsifiable.
			if node, err := strconv.Atoi(value); err == nil {
				g.NUMANode = node
			}
		}
	}
	g.Links = make([]string, highest+1)
	for peer, link := range links {
		g.Links[peer] = link
	}
	return g, nil
}

// gpuIndex reads the number out of a "GPU3" label, and reports whether the
// label named a card at all.
//
// The prefix alone is not enough: the last column of the matrix is headed
// "GPU NUMA ID", which starts with GPU and is not a card. Reading it as one put
// a seventh link on every row.
func gpuIndex(label string) (int, bool) {
	rest, found := strings.CutPrefix(label, "GPU")
	if !found {
		return 0, false
	}
	index, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return index, true
}

// countThreads counts the threads in an affinity list such as "0-23,48-71".
func countThreads(affinity string) int {
	total := 0
	for _, part := range strings.Split(affinity, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		low, high, ranged := strings.Cut(part, "-")
		first, err := strconv.Atoi(low)
		if err != nil {
			return 0
		}
		if !ranged {
			total++
			continue
		}
		last, err := strconv.Atoi(high)
		if err != nil {
			return 0
		}
		total += last - first + 1
	}
	return total
}

// tabFields splits a topo row on tabs, dropping the empty fields nvidia-smi
// pads its columns with.
func tabFields(line string) []string {
	var fields []string
	for _, field := range strings.Split(line, "\t") {
		if field = strings.TrimSpace(field); field != "" {
			fields = append(fields, field)
		}
	}
	return fields
}
