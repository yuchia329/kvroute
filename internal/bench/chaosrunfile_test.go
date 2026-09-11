package bench_test

import (
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
)

// Each policy's chaos run is made after its own fleet restart, by its own
// process, and the comparison is drawn later from what they left on disk. So a
// scenario has to read back as exactly the run that wrote it — checked through
// its report, which is everything anyone reads of it.
func TestAScenarioReadsBackAsTheRunThatWroteIt(t *testing.T) {
	fault := chaosStart.Add(2 * time.Second)
	s := chaosRun(bench.FaultKill)
	s.Assess(healthyThenFault(
		dropped(fault, "replica-2"), reroutedAt(fault.Add(200*time.Millisecond)),
		success(fault.Add(400*time.Millisecond), time.Second), success(fault.Add(600*time.Millisecond), 9*time.Second),
	), []bench.Event{
		{AtNs: 0, Kind: bench.EventFault, Detail: "ops/replica.sh kill 2"},
		{AtNs: (120 * time.Millisecond).Nanoseconds(), Kind: bench.EventOutOfRotation, Detail: "ejected"},
	})
	dir := t.TempDir()

	if err := bench.SaveChaosRun(dir, s); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := bench.LoadChaosRun(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if got.Report() != s.Report() {
		t.Errorf("the scenario read back reports differently from the one written\nwritten:\n%s\nread:\n%s", s.Report(), got.Report())
	}
}

// A directory a run never finished in holds no scenario, and reading it as one
// would put an empty run into a comparison as a policy that recovered instantly.
func TestADirectoryWithNoScenarioIsNotARun(t *testing.T) {
	if _, err := bench.LoadChaosRun(t.TempDir()); err == nil {
		t.Error("a directory with no scenario in it was read as a run")
	}
}
