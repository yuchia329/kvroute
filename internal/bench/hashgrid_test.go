package bench_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/router"
)

// A grid point is a label a cell carries, so it has to survive the round trip
// through the spec a command takes it as.
func TestAHashPointSurvivesItsSpec(t *testing.T) {
	for _, weight := range bench.HashWeightGrid {
		want := policy.HashPoint{LeadingBlocks: bench.HashLeadingBlocks, HashWeight: weight}
		got, err := bench.ParseHash(bench.FormatHash(want))
		if err != nil {
			t.Fatalf("ParseHash(%q): %v", bench.FormatHash(want), err)
		}
		if got != want {
			t.Errorf("%v round-tripped to %v", want, got)
		}
	}
}

func TestAnUnreadableHashPointIsRefused(t *testing.T) {
	for _, spec := range []string{"nonsense", "16", "16/4/2", "16/-1", "-1/4", "0/4", "sixteen/4", "16/heavy"} {
		if _, err := bench.ParseHash(spec); err == nil {
			t.Errorf("%q was accepted as a hash grid point", spec)
		}
	}
}

// The window has to sit inside what every turn of a conversation resends
// unchanged, and past the shared opening that a third of the sessions send
// identically. Both ends are the workload's geometry rather than a preference,
// so both are checked against it.
func TestTheStatedWindowFitsTheWorkloadItIsRunAgainst(t *testing.T) {
	const declaredBytesPerToken = 4
	window := policy.HashPoint{LeadingBlocks: bench.HashLeadingBlocks}.WindowBytes()

	// The shared system prompt is the one opening many unrelated sessions send
	// byte for byte. A window inside it would hash them to one key and
	// concentrate exactly the traffic idea.md §5 says to scatter.
	sharedOpening := 128 * declaredBytesPerToken
	if window <= sharedOpening {
		t.Errorf("the window is %d bytes and the shared system prompt is %d, so every session carrying one hashes to a single key",
			window, sharedOpening)
	}
	// The first turn is what every later turn of the session resends unchanged.
	// A window past it would move a conversation between its own turns.
	firstTurn := 448 * declaredBytesPerToken
	if window >= firstTurn {
		t.Errorf("the window is %d bytes and a first turn contributes %d, so turn 2 of a conversation would hash somewhere else than turn 1",
			window, firstTurn)
	}
}

// The two ends of the weight axis are the controls the middle is read against:
// no hash at all, and no load term at all.
func TestTheWeightAxisCarriesBothOfItsControls(t *testing.T) {
	if !slices.Contains(bench.HashWeightGrid, 0.0) {
		t.Error("the weight axis has no zero, which is the least-outstanding control the hash has to beat")
	}
	// The sweep runs at one concurrency, so no replica can be more than that many
	// requests out of line: a weight at or above it is the pure-hash end.
	top := slices.Max(bench.HashWeightGrid)
	if top < 32 {
		t.Errorf("the weight axis tops out at %v, below the concurrency the sweep runs at, so it never reaches the pure-hash end", top)
	}
	for _, weight := range bench.HashWeightGrid {
		if err := (policy.HashPoint{LeadingBlocks: bench.HashLeadingBlocks, HashWeight: weight}).Validate(); err != nil {
			t.Errorf("the axis carries a point the router would refuse: %v", err)
		}
	}
}

// The same argument the spill point's check rests on. The window and the weight
// reach the router as its own flags and the sweep is only told what they were,
// so nothing else stands between a mistyped flag and an axis whose cells all ran
// at one point.
func TestASweepRefusesARouterAtADifferentHashPoint(t *testing.T) {
	running := policy.HashPoint{LeadingBlocks: 16, HashWeight: 4}
	labelled := policy.HashPoint{LeadingBlocks: 16, HashWeight: 12}

	err := hashCheckUnderTest(t, policy.PrefixHashName, running, labelled)
	if err == nil {
		t.Fatal("a sweep was allowed to label its cells with a hash point the router is not running")
	}
	if !strings.Contains(err.Error(), "4.0") || !strings.Contains(err.Error(), "12.0") {
		t.Errorf("the refusal does not name the two points: %v", err)
	}
}

func TestASweepAtTheRoutersOwnHashPointIsAllowed(t *testing.T) {
	running := policy.HashPoint{LeadingBlocks: 16, HashWeight: 4}
	if err := hashCheckUnderTest(t, policy.PrefixHashName, running, running); err != nil {
		t.Fatalf("a matching hash point was refused: %v", err)
	}
}

// A policy that hashes nothing reports no point, and a sweep of it must not name
// one — a cell labelled with a window nothing applied is the same fiction the
// spill check refuses.
func TestASweepMayNotLabelAPolicyThatHashesNothing(t *testing.T) {
	if err := hashCheckUnderTest(t, policy.RoundRobinName, policy.HashPoint{}, policy.HashPoint{}); err != nil {
		t.Fatalf("a policy with no hash point was refused: %v", err)
	}
	err := hashCheckUnderTest(t, policy.RoundRobinName, policy.HashPoint{}, policy.HashPoint{LeadingBlocks: 16, HashWeight: 4})
	if err == nil {
		t.Fatal("round robin's cells were allowed to name a hash point, and it hashes nothing")
	}
}

// The cell carries the point it ran at, because the weight axis is a table
// indexed by it.
func TestACellRecordsTheHashPointItRanAt(t *testing.T) {
	point := policy.HashPoint{LeadingBlocks: 16, HashWeight: 12}
	dir := t.TempDir()
	if err := hashSweepInto(t, dir, point, point); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	cell := oneCellIn(t, dir)
	if cell.HashLeadingBlocks != point.LeadingBlocks || cell.HashWeight != point.HashWeight {
		t.Errorf("the cell records %d blocks at weight %v, and it ran at %v",
			cell.HashLeadingBlocks, cell.HashWeight, point)
	}
}

// Five weights at one workload point differ in nothing a cell id carries, so
// without this the four sweeps after the first would report themselves cached,
// send nothing, and draw a weight axis that is one weight plotted five times.
func TestASweepRefusesToResumeCellsOfAnotherHashPoint(t *testing.T) {
	dir := t.TempDir()
	first := policy.HashPoint{LeadingBlocks: 16, HashWeight: 4}
	if err := hashSweepInto(t, dir, first, first); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	second := policy.HashPoint{LeadingBlocks: 16, HashWeight: 12}
	err := hashSweepInto(t, dir, second, second)
	if err == nil {
		t.Fatal("a sweep at a second weight resumed the first one's cells, so its axis would be one point plotted twice")
	}
	if !strings.Contains(err.Error(), "hash grid point") {
		t.Errorf("the refusal does not say what disagreed: %v", err)
	}
}

// hashCheckUnderTest starts a router at one hash point and asks a sweep labelled
// with another to run against it, returning what the sweep refused to do.
func hashCheckUnderTest(t *testing.T, policyName string, running, labelled policy.HashPoint) error {
	t.Helper()
	return hashSweepAgainst(t, t.TempDir(), policyName, running, labelled)
}

func hashSweepInto(t *testing.T, dir string, running, labelled policy.HashPoint) error {
	t.Helper()
	return hashSweepAgainst(t, dir, policy.PrefixHashName, running, labelled)
}

func hashSweepAgainst(t *testing.T, dir, policyName string, running, labelled policy.HashPoint) error {
	t.Helper()
	_, replicaURL := fakeReplicaServer(t)
	target := routerHashing(t, policyName, running, "replica-0="+replicaURL)

	_, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir:           dir,
		Target:        target,
		Policy:        policyName,
		HashPoint:          labelled,
		Concurrencies: []int{1},
		Repetitions:   1,
		CellDuration:  20 * time.Millisecond,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 4096, OutputTokens: 1}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return err
}

// routerHashing serves a router at a named policy and hash point.
func routerHashing(t *testing.T, policyName string, point policy.HashPoint, specs ...string) string {
	t.Helper()
	replicas, err := fleet.ParseSpecs(specs)
	if err != nil {
		t.Fatalf("parse replica specs: %v", err)
	}
	f, err := fleet.New(replicas)
	if err != nil {
		t.Fatalf("new fleet: %v", err)
	}
	index, err := prefix.New(prefix.Config{NodeCap: 4096, TTL: time.Hour})
	if err != nil {
		t.Fatalf("prefix.New: %v", err)
	}
	chosen, err := policy.ByName(policyName, policy.Options{PrefixIndex: index, HashPoint: point})
	if err != nil {
		t.Fatalf("policy.ByName: %v", err)
	}
	rt, err := router.New(router.Config{
		Fleet:   f,
		Policy:  chosen,
		Records: record.NewWriter[record.Request](nopWriter{}),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	srv := httptest.NewServer(rt.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// oneCellIn reads the single cell a one-point sweep wrote.
func oneCellIn(t *testing.T, dir string) bench.Cell {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "cells", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("expected one cell in %s, found %v (%v)", dir, paths, err)
	}
	contents, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("read %s: %v", paths[0], err)
	}
	var cell bench.Cell
	if err := json.Unmarshal(contents, &cell); err != nil {
		t.Fatalf("unmarshal %s: %v", paths[0], err)
	}
	return cell
}
