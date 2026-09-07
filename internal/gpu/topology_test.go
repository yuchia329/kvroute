package gpu_test

import (
	"context"
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/gpu"
	"github.com/yuchia329/kvroute/internal/gpu/gputest"
)

func topology(t *testing.T, canned string) gpu.Topology {
	t.Helper()
	topo, err := gpu.NewProber(gputest.RunnerWithTopology("", "", "", canned)).Topology(context.Background())
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	return topo
}

func TestTopologyReadsTheLinkMatrixAndNUMAPlacementOfEveryCard(t *testing.T) {
	topo := topology(t, gputest.SixGPUTopology)

	if len(topo.GPUs) != 6 {
		t.Fatalf("read %d GPUs, want 6", len(topo.GPUs))
	}
	// The pairs (0,1)(2,3)(4,5) sit behind one PCIe bridge, cards within a
	// socket reach each other across host bridges, and 0-3 reach 4-5 only over
	// the inter-socket link. That last one is what §8's KV-transfer arithmetic
	// is about.
	gpu0 := topo.GPUs[0]
	if got := gpu0.Links[0]; got != "X" {
		t.Errorf("GPU0's link to itself is %q, want X", got)
	}
	if got := gpu0.Links[1]; got != "PIX" {
		t.Errorf("GPU0 to GPU1 is %q, want PIX", got)
	}
	if got := gpu0.Links[2]; got != "NODE" {
		t.Errorf("GPU0 to GPU2 is %q, want NODE", got)
	}
	if got := gpu0.Links[4]; got != "SYS" {
		t.Errorf("GPU0 to GPU4 is %q, want SYS: they are on different sockets", got)
	}
	if got := topo.GPUs[5].Links[4]; got != "PIX" {
		t.Errorf("GPU5 to GPU4 is %q, want PIX", got)
	}

	if gpu0.NUMANode != 0 || topo.GPUs[4].NUMANode != 1 {
		t.Errorf("NUMA nodes are %d and %d for GPUs 0 and 4, want 0 and 1", gpu0.NUMANode, topo.GPUs[4].NUMANode)
	}
	if gpu0.CPUAffinity != "0-23,48-71" {
		t.Errorf("GPU0 CPU affinity is %q, want 0-23,48-71", gpu0.CPUAffinity)
	}
	if gpu0.CPUThreads != 48 {
		t.Errorf("GPU0 sees %d threads, want 48: 0-23 and 48-71 are 24 each", gpu0.CPUThreads)
	}
	// The raw output is kept so the parse can always be checked against what
	// the driver said, rather than believed.
	if !strings.Contains(topo.Raw, "Legend") {
		t.Error("the raw nvidia-smi output was not kept")
	}
}

// The whole reason topology is recorded beside the symmetry check: this host is
// not even. Four cards share one node's 48 threads and two share the other's,
// so a replica on NUMA 0 has half the CPU a replica on NUMA 1 does for the same
// per-step host-side work.
func TestNUMAGroupsStateTheHostsAsymmetryAsANumber(t *testing.T) {
	groups := topology(t, gputest.SixGPUTopology).NUMAGroups()

	if len(groups) != 2 {
		t.Fatalf("found %d NUMA groups, want 2", len(groups))
	}
	if groups[0].Node != 0 || len(groups[0].GPUs) != 4 || groups[0].ThreadsPerGPU != 12 {
		t.Errorf("node 0 is %+v, want 4 GPUs at 12 threads each", groups[0])
	}
	if groups[1].Node != 1 || len(groups[1].GPUs) != 2 || groups[1].ThreadsPerGPU != 24 {
		t.Errorf("node 1 is %+v, want 2 GPUs at 24 threads each", groups[1])
	}
}

// A host that reports no NUMA affinity must not read as one where every card is
// on node 0: that would make the symmetry check unfalsifiable, since it would
// never have two groups to compare.
func TestTopologyWithoutNUMAAffinityRecordsThatItIsUnknown(t *testing.T) {
	canned := "\tGPU0\tGPU1\tCPU Affinity\tNUMA Affinity\n" +
		"GPU0\t X \tPHB\t0-95\tN/A\n" +
		"GPU1\tPHB\t X \t0-95\tN/A\n"

	topo := topology(t, canned)

	for _, g := range topo.GPUs {
		if g.NUMANode != -1 {
			t.Errorf("GPU%d reports node %d, want -1 for unknown", g.Index, g.NUMANode)
		}
	}
}

// InfiniBand hosts get NIC rows and columns this fleet's host does not have. A
// positional parse would read one of them as a GPU link.
func TestTopologyIgnoresNonGPURowsAndColumns(t *testing.T) {
	canned := "\tGPU0\tGPU1\tNIC0\tCPU Affinity\tNUMA Affinity\n" +
		"GPU0\t X \tPIX\tSYS\t0-23\t0\n" +
		"GPU1\tPIX\t X \tSYS\t0-23\t0\n" +
		"NIC0\tSYS\tSYS\t X \t\t\n"

	topo := topology(t, canned)

	if len(topo.GPUs) != 2 {
		t.Fatalf("read %d GPUs, want 2: the NIC row is not a card", len(topo.GPUs))
	}
	if len(topo.GPUs[0].Links) != 2 {
		t.Errorf("GPU0 has %d links, want 2: the NIC column is not a card", len(topo.GPUs[0].Links))
	}
	if topo.GPUs[0].CPUAffinity != "0-23" {
		t.Errorf("GPU0 CPU affinity is %q, want 0-23: the NIC column shifted the columns", topo.GPUs[0].CPUAffinity)
	}
}

func TestTopologyRefusesOutputThatNamesNoGPUs(t *testing.T) {
	_, err := gpu.NewProber(gputest.RunnerWithTopology("", "", "", "nvidia-smi: command not found\n")).
		Topology(context.Background())
	if err == nil {
		t.Fatal("parsed a topology out of output naming no GPUs")
	}
}
