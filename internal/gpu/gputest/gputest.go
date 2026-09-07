// Package gputest supplies canned nvidia-smi and ps output so code that
// depends on a GPU probe can be tested on a machine that has no GPU.
//
// The fixtures live here rather than in each test package because their shape —
// the ", " separator, the padded ps columns, and the fact that the process
// holding a card is the engine child of the pid a pid file records — is the
// thing that would drift if it were copied.
package gputest

import (
	"context"
	"fmt"
	"strings"

	"github.com/yuchia329/kvroute/internal/gpu"
)

// Canned command output, in the exact shape the real commands produce.
const (
	// SixIdleDevices is the fleet's six cards with nothing running on them.
	SixIdleDevices = `0, GPU-aaaa0000-1111-2222-3333-444444444444, 1, 0
1, GPU-bbbb0000-1111-2222-3333-444444444444, 1, 0
2, GPU-cccc0000-1111-2222-3333-444444444444, 1, 0
3, GPU-dddd0000-1111-2222-3333-444444444444, 1, 0
4, GPU-eeee0000-1111-2222-3333-444444444444, 1, 0
5, GPU-ffff0000-1111-2222-3333-444444444444, 1, 0
`
	// NoComputeApps is what nvidia-smi prints when nothing holds a card.
	NoComputeApps = ""

	// FleetDevices, FleetApps and FleetProcesses are two of our own replicas
	// running. The pid files hold 12100 and 22000; the processes on the cards
	// are their EngineCore children.
	FleetDevices = `0, GPU-aaaa0000-1111-2222-3333-444444444444, 20481, 97
1, GPU-bbbb0000-1111-2222-3333-444444444444, 20481, 95
`
	FleetApps = `12345, GPU-aaaa0000-1111-2222-3333-444444444444, 20480
22345, GPU-bbbb0000-1111-2222-3333-444444444444, 20480
`

	FleetProcesses = `  12345   12100 yc       VLLM::EngineCore
  12100   12000 yc       vllm
  12000       1 yc       bash
  22345   22000 yc       VLLM::EngineCore
  22000       1 yc       vllm
`
	// SixGPUTopology is `nvidia-smi topo -m` as the fleet's host actually prints
	// it, captured on 2026-09-06. The header row carries the driver's underline
	// escape sequences, which are emitted whether or not stdout is a terminal, and
	// the columns are padded with tabs that do not line up with the header's — both
	// of which the parser has to survive.
	SixGPUTopology = "\x1b[4m\tGPU0\tGPU1\tGPU2\tGPU3\tGPU4\tGPU5\tCPU Affinity\tNUMA Affinity\tGPU NUMA ID\x1b[0m\n" +
		"GPU0\t X \tPIX\tNODE\tNODE\tSYS\tSYS\t0-23,48-71\t0\t\tN/A\n" +
		"GPU1\tPIX\t X \tNODE\tNODE\tSYS\tSYS\t0-23,48-71\t0\t\tN/A\n" +
		"GPU2\tNODE\tNODE\t X \tPIX\tSYS\tSYS\t0-23,48-71\t0\t\tN/A\n" +
		"GPU3\tNODE\tNODE\tPIX\t X \tSYS\tSYS\t0-23,48-71\t0\t\tN/A\n" +
		"GPU4\tSYS\tSYS\tSYS\tSYS\t X \tPIX\t24-47,72-95\t1\t\tN/A\n" +
		"GPU5\tSYS\tSYS\tSYS\tSYS\tPIX\t X \t24-47,72-95\t1\t\tN/A\n" +
		"\nLegend:\n\n  X    = Self\n  SYS  = Connection traversing PCIe as well as the SMP interconnect between NUMA nodes (e.g., QPI/UPI)\n  NODE = Connection traversing PCIe as well as the interconnect between PCIe Host Bridges within a NUMA node\n  PIX  = Connection traversing at most a single PCIe bridge\n"
)

// OwnFleetPIDs are the supervisor pids matching the Fleet fixtures, as
// ops/fleet.sh would report them.
var OwnFleetPIDs = []int{12100, 22000}

// Runner answers the commands a probe runs from canned text, with this fleet's
// own topology.
func Runner(devices, apps, processes string) gpu.Runner {
	return RunnerWithTopology(devices, apps, processes, SixGPUTopology)
}

// RunnerWithTopology is Runner over a chosen topology, for the cases that are
// about a host shaped differently from this one.
func RunnerWithTopology(devices, apps, processes, topology string) gpu.Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "ps":
			return []byte(processes), nil
		case name == "nvidia-smi" && strings.Contains(joined, "topo"):
			return []byte(topology), nil
		case name == "nvidia-smi" && strings.Contains(joined, "query-compute-apps"):
			return []byte(apps), nil
		case name == "nvidia-smi":
			return []byte(devices), nil
		}
		return nil, fmt.Errorf("gputest: unexpected command %s %v", name, args)
	}
}

// Fleet is a prober showing our own two replicas and nothing else.
func Fleet() *gpu.Prober {
	return gpu.NewProber(Runner(FleetDevices, FleetApps, FleetProcesses))
}

// Contaminated is a prober showing another user's process holding GPU 0.
func Contaminated() *gpu.Prober {
	return gpu.NewProber(Runner(
		"0, GPU-aaaa0000-1111-2222-3333-444444444444, 9000, 80\n",
		"77777, GPU-aaaa0000-1111-2222-3333-444444444444, 8999\n",
		"  77777       1 labmate  python3\n",
	))
}
