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
)

// OwnFleetPIDs are the supervisor pids matching the Fleet fixtures, as
// ops/fleet.sh would report them.
var OwnFleetPIDs = []int{12100, 22000}

// Runner answers the three commands a probe runs from canned text.
func Runner(devices, apps, processes string) gpu.Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "ps":
			return []byte(processes), nil
		case name == "nvidia-smi" && strings.Contains(strings.Join(args, " "), "query-compute-apps"):
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
