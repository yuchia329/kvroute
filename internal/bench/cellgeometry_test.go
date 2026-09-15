package bench_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
)

// A cell has to say how long it ran and how much of that was warm-up, and a
// sweep has to refuse to resume cells of another length or warm-up.
//
// The same trap as think time, and it has already been sprung once. A cell is
// cached on an id naming the policy, the load point and the repetition, and the
// workload's name catches everything that changes the bytes. A cell's length and
// warm-up change no bytes, so they are in neither — and #18's smoke cell, run at
// 150 s, shared an id with repetition 1 of a grid that then ran at 300 s. Had it
// not been deleted by hand, the grid would have resumed it as its own: one cell
// with half the measured window of its siblings, pooled into their median, and
// nothing in any log saying so.
//
// Warm-up is guarded with the length because it is the same hole and the value
// that has actually moved: ADR-0007 raised it from 25 s to 50 s, and a cell's
// measured window is its length less its warm-up, so two warm-ups are two
// windows.

// sweepAtGeometry asks a sweep to resume into a directory at one cell length and
// warm-up, and returns what it refused to do. Like sweepAtThinkTime it never
// reaches a router: the cached-cell checks run first, so a refusal here is the
// one under test.
func sweepAtGeometry(t *testing.T, dir string, length, warmup time.Duration) error {
	t.Helper()
	_, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir:           dir,
		Target:        "http://127.0.0.1:1", // never reached
		Policy:        "round_robin",
		Concurrencies: []int{32},
		Repetitions:   1,
		CellDuration:  length,
		Warmup:        warmup,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 64, OutputTokens: 1}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return err
}

// cachedCellOfGeometry is a finished closed-loop cell of one length and warm-up.
func cachedCellOfGeometry(length, warmup time.Duration) bench.Cell {
	return bench.Cell{
		ID:             "round_robin-c32-r1",
		Policy:         "round_robin",
		Driver:         bench.ClosedLoopDriver,
		Concurrency:    32,
		Repetition:     1,
		Workload:       bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 64, OutputTokens: 1}).Name(),
		CellDurationNs: length.Nanoseconds(),
		WarmupNs:       warmup.Nanoseconds(),
	}
}

func TestACellRecordsTheLengthAndWarmUpItRanAt(t *testing.T) {
	cells, _ := sweepUnderTest(t, t.TempDir(), bench.SweepConfig{
		CellDuration: 40 * time.Millisecond,
		Warmup:       10 * time.Millisecond,
	})
	if len(cells) == 0 {
		t.Fatal("the sweep recorded no cells")
	}
	for _, c := range cells {
		if c.CellDurationNs != (40 * time.Millisecond).Nanoseconds() {
			t.Errorf("cell %s records a length of %v, want the 40ms it ran", c.ID, time.Duration(c.CellDurationNs))
		}
		if c.WarmupNs != (10 * time.Millisecond).Nanoseconds() {
			t.Errorf("cell %s records a warm-up of %v, want the 10ms it ran", c.ID, time.Duration(c.WarmupNs))
		}
	}
}

// The case that happened: a 150 s cell sharing an id with a 300 s grid's first
// repetition.
func TestASweepRefusesCellsOfAnotherLength(t *testing.T) {
	dir := t.TempDir()
	writeCachedCell(t, dir, cachedCellOfGeometry(150*time.Second, 50*time.Second))

	err := sweepAtGeometry(t, dir, 300*time.Second, 50*time.Second)
	if err == nil {
		t.Fatal("a sweep resumed a cell of a different length, pooling a half-length measurement into a full-length median")
	}
	// Both lengths, or the reader cannot tell which directory holds which and the
	// repair is guesswork.
	if !strings.Contains(err.Error(), "2m30s") || !strings.Contains(err.Error(), "5m0s") {
		t.Errorf("the refusal does not name both lengths: %v", err)
	}
}

func TestASweepRefusesCellsWithAnotherWarmUp(t *testing.T) {
	dir := t.TempDir()
	writeCachedCell(t, dir, cachedCellOfGeometry(300*time.Second, 25*time.Second))

	err := sweepAtGeometry(t, dir, 300*time.Second, 50*time.Second)
	if err == nil {
		t.Fatal("a sweep resumed a cell with a different warm-up, so two measured windows would be pooled as one")
	}
	if !strings.Contains(err.Error(), "25s") || !strings.Contains(err.Error(), "50s") {
		t.Errorf("the refusal does not name both warm-ups: %v", err)
	}
}

// A warm-up of zero is a real setting, not an absence. What marks a cell as
// having recorded its geometry is its length, which no cell can have at zero, so
// a recorded cell with no warm-up is compared — and refused against a sweep that
// has one. Treating zero warm-up as unrecorded, the way think time treats zero,
// would leave exactly this hole open.
func TestACellRunWithNoWarmUpIsComparedRatherThanTreatedAsUnrecorded(t *testing.T) {
	dir := t.TempDir()
	writeCachedCell(t, dir, cachedCellOfGeometry(300*time.Second, 0))

	// The refusal's own wording, not merely an error: a sweep that got past the
	// cached-cell checks fails on the router it was never given, so a bare
	// non-nil would pass whether or not the guard exists.
	err := sweepAtGeometry(t, dir, 300*time.Second, 50*time.Second)
	if err == nil || !strings.Contains(err.Error(), "length or warm-up") {
		t.Fatalf("a cell run with no warm-up was resumed into a sweep with one, as though it had recorded nothing: %v", err)
	}
}

func TestASweepOfTheCellsOwnLengthAndWarmUpResumes(t *testing.T) {
	dir := t.TempDir()
	writeCachedCell(t, dir, cachedCellOfGeometry(300*time.Second, 50*time.Second))

	// Past the cached-cell checks, so whatever it fails on next is the router it
	// was never given — not the geometry.
	err := sweepAtGeometry(t, dir, 300*time.Second, 50*time.Second)
	if err != nil && strings.Contains(err.Error(), "length") {
		t.Fatalf("a cell of the sweep's own length and warm-up was refused: %v", err)
	}
}

// Every cell recorded before these columns existed — #18's whole grid among
// them — carries no length. Refusing them would invalidate every measurement
// already made over a field nobody wrote, so an unrecorded length is not
// compared.
func TestCellsRecordedBeforeTheLengthWasWrittenStillResume(t *testing.T) {
	dir := t.TempDir()
	writeCachedCell(t, dir, cachedCellOfGeometry(0, 0))

	err := sweepAtGeometry(t, dir, 300*time.Second, 50*time.Second)
	if err != nil && strings.Contains(err.Error(), "length") {
		t.Fatalf("a cell recorded before its length was written was refused: %v", err)
	}
}
