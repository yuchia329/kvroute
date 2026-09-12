package gpu_test

import (
	"context"
	"slices"
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

// TestSnapshotReadsTheClockAndWhyItIsNotHigher. The mask is the whole reason
// this column is read: a card can be at a low clock for a normal reason, and
// only the driver knows which.
func TestSnapshotReadsTheClockAndWhyItIsNotHigher(t *testing.T) {
	s := snapshot(t, gputest.SixDevicesGPU3Thermal, gputest.NoComputeApps, gputest.FleetProcesses)

	healthy := s.Devices[0]
	if !healthy.ClocksRead || healthy.SMClockMHz != 1305 {
		t.Errorf("GPU 0 read as %+v, want 1305 MHz read", healthy)
	}
	if !healthy.Throttle.Has(gpu.ThrottleSwPowerCap) || healthy.Throttle.Thermal() {
		t.Errorf("GPU 0 throttle is %s, want the power cap alone", healthy.Throttle)
	}

	bad := s.Devices[3]
	if bad.SMClockMHz != 960 {
		t.Errorf("GPU 3 is at %d MHz, want 960", bad.SMClockMHz)
	}
	if !bad.Throttle.Thermal() {
		t.Errorf("GPU 3 throttle is %s, want a thermal reason in it", bad.Throttle)
	}
	// Both bits, not one: the card is power-capped like its siblings *and*
	// thermally limited unlike them, and collapsing that to one reason would
	// lose the comparison the flag is drawn from.
	if got, want := bad.Throttle.Names(), []string{"SwPowerCap", "SwThermal"}; !slices.Equal(got, want) {
		t.Errorf("GPU 3 reasons are %v, want %v", got, want)
	}
}

// TestAnIdleCardIsNotAThrottledOne: every card in an idle fleet reports GpuIdle,
// which is the driver answering "nothing is asking for more", not a defect.
func TestAnIdleCardIsNotAThrottledOne(t *testing.T) {
	s := snapshot(t, gputest.SixIdleDevices, gputest.NoComputeApps, gputest.FleetProcesses)

	for _, d := range s.Devices {
		if d.Throttle.Thermal() {
			t.Errorf("idle GPU %d reads as thermally throttled: %s", d.Index, d.Throttle)
		}
		if !d.Throttle.Has(gpu.ThrottleGPUIdle) {
			t.Errorf("idle GPU %d reports %s, want GpuIdle", d.Index, d.Throttle)
		}
	}
}

// TestADriverThatReportsNoClocksSaysSoRatherThanReadingAsZero. A card sitting at
// 0 MHz and a driver that will not answer must not be the same record, and
// neither may take the probe down with it: cleanliness is what this probe is
// for.
func TestADriverThatReportsNoClocksSaysSoRatherThanReadingAsZero(t *testing.T) {
	s := snapshot(t, gputest.SixDevicesNoClocks, gputest.NoComputeApps, gputest.FleetProcesses)

	if len(s.Devices) != 6 {
		t.Fatalf("read %d devices, want all 6 despite the missing clocks", len(s.Devices))
	}
	for _, d := range s.Devices {
		if d.ClocksRead {
			t.Errorf("GPU %d claims its clocks were read from [N/A]", d.Index)
		}
	}
	if got := s.Devices[0].MemoryUsedMiB; got != 20481 {
		t.Errorf("GPU 0 memory is %d MiB, want the probe's own column still read", got)
	}
}

// TestDeviceLinesFromBeforeTheClockColumnsStillParse: captures and older
// callers hold four-column lines, and the columns that decide cleanliness are
// all in them.
func TestDeviceLinesFromBeforeTheClockColumnsStillParse(t *testing.T) {
	s := snapshot(t, "0, GPU-aaaa0000-1111-2222-3333-444444444444, 20481, 97\n",
		gputest.NoComputeApps, gputest.FleetProcesses)

	if len(s.Devices) != 1 || s.Devices[0].MemoryUsedMiB != 20481 {
		t.Fatalf("read %+v, want the one device", s.Devices)
	}
	if s.Devices[0].ClocksRead {
		t.Error("a four-column line claims its clocks were read")
	}
}
