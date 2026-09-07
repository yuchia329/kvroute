package gpu_test

import (
	"context"
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/gpu"
	"github.com/yuchia329/kvroute/internal/gpu/gputest"
)

func snapshot(t *testing.T, devices, apps, processes string) gpu.Snapshot {
	t.Helper()
	s, err := gpu.NewProber(gputest.Runner(devices, apps, processes)).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return s
}

func TestSnapshotReadsEveryDeviceAndTheProcessesHoldingThem(t *testing.T) {
	devices := `0, GPU-aaaa0000-1111-2222-3333-444444444444, 20481, 97
1, GPU-bbbb0000-1111-2222-3333-444444444444, 1, 0
`
	apps := `12345, GPU-aaaa0000-1111-2222-3333-444444444444, 20480
`
	processes := `  12345   12000 yc       VLLM::EngineCore
  12000       1 yc       vllm
`

	s := snapshot(t, devices, apps, processes)

	if len(s.Devices) != 2 {
		t.Fatalf("read %d devices, want 2", len(s.Devices))
	}
	if got := s.Devices[0]; got.Index != 0 || got.MemoryUsedMiB != 20481 || got.UtilizationPct != 97 {
		t.Errorf("device 0 is %+v, want index 0, 20481 MiB, 97%%", got)
	}
	if len(s.Processes) != 1 {
		t.Fatalf("read %d GPU processes, want 1", len(s.Processes))
	}
	got := s.Processes[0]
	if got.PID != 12345 || got.GPU != 0 || got.MemoryUsedMiB != 20480 {
		t.Errorf("process is %+v, want pid 12345 on GPU 0 holding 20480 MiB", got)
	}
	// The user and command come from ps: nvidia-smi reports neither, and a
	// refusal that cannot say who is holding the card is not actionable.
	if got.User != "yc" || got.Command != "VLLM::EngineCore" {
		t.Errorf("process is %+v, want user yc running VLLM::EngineCore", got)
	}
}

func TestSnapshotReadsAFleetWithNoComputeProcesses(t *testing.T) {
	s := snapshot(t, gputest.SixIdleDevices, gputest.NoComputeApps, "")

	if len(s.Devices) != 6 {
		t.Fatalf("read %d devices, want 6", len(s.Devices))
	}
	if len(s.Processes) != 0 {
		t.Fatalf("read %d GPU processes, want none", len(s.Processes))
	}
}

func TestDirtyNamesEveryGPUAlreadyHoldingMemory(t *testing.T) {
	// Card 3 holds 8 GiB with nothing attributable to it, which is what
	// another user's process looks like when their pid namespace is not
	// visible. Preflight has to refuse on the memory, not on the process list.
	devices := strings.Replace(gputest.SixIdleDevices,
		"3, GPU-dddd0000-1111-2222-3333-444444444444, 1, 0",
		"3, GPU-dddd0000-1111-2222-3333-444444444444, 8192, 61", 1)

	dirty := snapshot(t, devices, gputest.NoComputeApps, "").Dirty(256)

	if len(dirty) != 1 {
		t.Fatalf("found %d dirty devices, want 1: %+v", len(dirty), dirty)
	}
	if dirty[0].Index != 3 {
		t.Errorf("dirty device is %d, want 3", dirty[0].Index)
	}
}

func TestDirtyIgnoresTheFewMiBAnIdleDriverHolds(t *testing.T) {
	if dirty := snapshot(t, gputest.SixIdleDevices, gputest.NoComputeApps, "").Dirty(256); len(dirty) != 0 {
		t.Fatalf("an idle fleet reported %d dirty devices: %+v", len(dirty), dirty)
	}
}

func TestForeignExcludesOurOwnReplicasAndTheirEngineChildren(t *testing.T) {
	s := snapshot(t, gputest.FleetDevices, gputest.FleetApps, gputest.FleetProcesses)

	// The pid files hold the supervisors, not the processes on the cards.
	foreign := s.Foreign([]int{12100, 22000})

	if len(foreign) != 0 {
		t.Fatalf("our own fleet reported %d foreign processes: %+v", len(foreign), foreign)
	}
}

func TestForeignReportsAnotherUsersProcess(t *testing.T) {
	devices := gputest.FleetDevices + "2, GPU-cccc0000-1111-2222-3333-444444444444, 9000, 80\n"
	apps := gputest.FleetApps + "77777, GPU-cccc0000-1111-2222-3333-444444444444, 8999\n"
	processes := gputest.FleetProcesses + "  77777   77000 labmate  python3\n  77000       1 labmate  bash\n"

	s := snapshot(t, devices, apps, processes)
	foreign := s.Foreign([]int{12100, 22000})

	if len(foreign) != 1 {
		t.Fatalf("found %d foreign processes, want 1: %+v", len(foreign), foreign)
	}
	if foreign[0].PID != 77777 || foreign[0].User != "labmate" {
		t.Errorf("foreign process is %+v, want pid 77777 owned by labmate", foreign[0])
	}
	if got := s.ForeignMemoryMiB([]int{12100, 22000}); got != 8999 {
		t.Errorf("foreign memory is %d MiB, want 8999", got)
	}
}

func TestForeignReportsOurOwnLeftoverWhenItIsNotInTheFleet(t *testing.T) {
	// A replica from a previous run that was never brought down. It is ours and
	// it is still contamination: the fleet the cell is measuring did not start
	// it, and it is holding a card.
	s := snapshot(t, gputest.FleetDevices, gputest.FleetApps, gputest.FleetProcesses)

	foreign := s.Foreign([]int{12100})

	if len(foreign) != 1 || foreign[0].PID != 22345 {
		t.Fatalf("found %+v, want the leftover replica's engine process 22345", foreign)
	}
}

func TestLimitRestrictsTheSnapshotToTheFleetsGPUs(t *testing.T) {
	// Reducing to a single NUMA node is the escalation §10 takes if the
	// replicas turn out not to be interchangeable, so cleanliness has to be
	// answerable for a subset of the cards.
	devices := gputest.FleetDevices + "2, GPU-cccc0000-1111-2222-3333-444444444444, 9000, 80\n"
	apps := gputest.FleetApps + "77777, GPU-cccc0000-1111-2222-3333-444444444444, 8999\n"
	processes := gputest.FleetProcesses + "  77777   77000 labmate  python3\n  77000       1 labmate  bash\n"

	s := snapshot(t, devices, apps, processes).Limit([]int{0, 1})

	if len(s.Devices) != 2 {
		t.Errorf("limited snapshot has %d devices, want 2", len(s.Devices))
	}
	if foreign := s.Foreign([]int{12100, 22000}); len(foreign) != 0 {
		t.Errorf("a process on an excluded GPU counted as foreign: %+v", foreign)
	}
	if dirty := s.Dirty(256); len(dirty) != 2 {
		t.Errorf("limited snapshot found %d dirty devices, want the fleet's own 2", len(dirty))
	}
}
