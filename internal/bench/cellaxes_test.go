package bench_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parquet-go/parquet-go"
	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/record"
)

// axisCell is a cell carrying every column #42 adds, set to values no default
// would produce, so a column dropped anywhere in the record path shows up as a
// zero rather than as a value that happens to match.
func axisCell() bench.Cell {
	return bench.Cell{
		ID:                           "axes-c32-r1",
		Policy:                       "bounded_session_affinity",
		Driver:                       bench.ClosedLoopDriver,
		Concurrency:                  32,
		Repetition:                   1,
		Workload:                     "multiturn",
		WorkingSet:                   3,
		Skew:                         1.4,
		TurnsPerSession:              16,
		PromptTokensPerTurn:          2048,
		SessionPool:                  369,
		InflightBound:                0.25,
		EffectiveLoadImbalanceFactor: 6.5,
	}
}

// The record is written as JSON and read back by re-score, compare and every
// report, so a column that does not survive that trip is a column no figure
// will ever be drawn from.
func TestTheConfiguredAxesSurviveTheCellRecord(t *testing.T) {
	want := axisCell()

	contents, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got bench.Cell
	if err := json.Unmarshal(contents, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.TurnsPerSession != want.TurnsPerSession || got.PromptTokensPerTurn != want.PromptTokensPerTurn {
		t.Errorf("turn geometry = %d turns of %d tokens, want %d of %d",
			got.TurnsPerSession, got.PromptTokensPerTurn, want.TurnsPerSession, want.PromptTokensPerTurn)
	}
	if got.SessionPool != want.SessionPool {
		t.Errorf("session pool = %d, want %d", got.SessionPool, want.SessionPool)
	}
	if got.InflightBound != want.InflightBound {
		t.Errorf("inflight bound = %v, want %v", got.InflightBound, want.InflightBound)
	}
	if got.EffectiveLoadImbalanceFactor != want.EffectiveLoadImbalanceFactor {
		t.Errorf("effective load imbalance factor = %v, want %v",
			got.EffectiveLoadImbalanceFactor, want.EffectiveLoadImbalanceFactor)
	}
}

// The JSONL half of the record, which is the system of record the rows are
// flushed to per request and every Parquet file is derived from.
func TestTheConfiguredAxesSurviveJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cells.jsonl")
	rows, err := record.Open[bench.Cell](path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	want := axisCell()
	if err := rows.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got bench.Cell
	if err := json.Unmarshal(contents, &got); err != nil {
		t.Fatalf("decode line: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the cell changed on the way through JSONL:\n %+v\n %+v", want, got)
	}
}

// The analysis reads Parquet, so evidence that only survives in the JSONL is
// evidence no figure will ever be drawn from — the reason the throttle
// evidence has a compaction test of its own.
func TestTheConfiguredAxesSurviveCompaction(t *testing.T) {
	dir := t.TempDir()
	cellDir := filepath.Join(dir, "cells")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want := axisCell()
	contents, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cellDir, want.ID+".json"), contents, 0o644); err != nil {
		t.Fatalf("write cell: %v", err)
	}

	compacted, err := bench.Compact(dir)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	cells, err := parquet.ReadFile[bench.Cell](compacted.CellsPath)
	if err != nil {
		t.Fatalf("read %s: %v", compacted.CellsPath, err)
	}
	if len(cells) != 1 {
		t.Fatalf("%s holds %d cells, want 1", compacted.CellsPath, len(cells))
	}

	got := cells[0]
	if got.TurnsPerSession != want.TurnsPerSession || got.PromptTokensPerTurn != want.PromptTokensPerTurn ||
		got.SessionPool != want.SessionPool || got.InflightBound != want.InflightBound ||
		got.EffectiveLoadImbalanceFactor != want.EffectiveLoadImbalanceFactor {
		t.Errorf("the configured axes did not survive compaction:\n want %d/%d turns, pool %d, bound %v, effective %v\n got  %d/%d turns, pool %d, bound %v, effective %v",
			want.TurnsPerSession, want.PromptTokensPerTurn, want.SessionPool, want.InflightBound, want.EffectiveLoadImbalanceFactor,
			got.TurnsPerSession, got.PromptTokensPerTurn, got.SessionPool, got.InflightBound, got.EffectiveLoadImbalanceFactor)
	}
}

// Zero is an absence on every one of these columns, not the bottom of an axis.
//
// Every cell already on disk predates them and decodes as zero, and three of
// the five columns apply to policies that do not exist yet. A report that read
// a zero turn count as "one-turn conversations", or a zero bound as "a bound of
// nothing", would put cells on an axis they were never run on — which is the
// mistake GridPoint.Stated exists to prevent on the working set axis.
func TestZeroOnTheNewColumnsIsAnAbsence(t *testing.T) {
	// A cell recorded before these columns existed: the JSON has no such
	// fields at all, and decoding must leave them unset rather than guessing.
	var old bench.Cell
	if err := json.Unmarshal([]byte(`{"id":"pre-42","policy":"prefix_affinity","working_set":1,"skew":0}`), &old); err != nil {
		t.Fatalf("decode a cell recorded before the columns existed: %v", err)
	}
	if old.TurnsPerSession != 0 || old.PromptTokensPerTurn != 0 || old.SessionPool != 0 ||
		old.InflightBound != 0 || old.EffectiveLoadImbalanceFactor != 0 {
		t.Errorf("a cell recorded before these columns came back with values: %+v", old)
	}
	// And the columns the record already has are untouched by their arrival,
	// which is what keeps every published cell readable.
	if old.WorkingSet != 1 || old.Policy != "prefix_affinity" {
		t.Errorf("adding the columns changed how an existing cell decodes: %+v", old)
	}
}

// The columns are only worth having if the sweep fills them, and it fills them
// from the workload it actually ran rather than from the name it wrote down.
//
// A cell that stated its geometry only in the workload name would be readable
// by a report that parsed the name, which is the coupling #35 rules out: the
// name gains a field the first time any workload knob is added, and every
// report that parsed it breaks at once.
func TestASweepRecordsTheGeometryAndPoolItRan(t *testing.T) {
	cfg := multiTurnConfig()
	// Deliberately off the published geometry, so a column filled in from the
	// generator's defaults rather than from the trace shows up as a mismatch.
	cfg.TurnsPerSession = 8
	cfg.PromptTokens = 896
	cfg.Sessions = 12

	cells, _ := sweepUnderTest(t, t.TempDir(), bench.SweepConfig{
		Workload: newMultiTurn(t, cfg),
	})
	if len(cells) == 0 {
		t.Fatal("the sweep recorded no cells")
	}
	for _, c := range cells {
		if c.TurnsPerSession != cfg.TurnsPerSession || c.PromptTokensPerTurn != cfg.PromptTokens {
			t.Errorf("cell %s ran %d turns of %d tokens and recorded %d of %d",
				c.ID, cfg.TurnsPerSession, cfg.PromptTokens, c.TurnsPerSession, c.PromptTokensPerTurn)
		}
		if c.SessionPool != cfg.Sessions {
			t.Errorf("cell %s drew from %d sessions and recorded %d", c.ID, cfg.Sessions, c.SessionPool)
		}
	}
}

// A cell of a workload with no conversations records no geometry at all, which
// is what keeps the fixed workload's cells off both new axes rather than at the
// foot of them.
func TestASweepOfAWorkloadWithNoConversationsRecordsNoGeometry(t *testing.T) {
	cells, _ := sweepUnderTest(t, t.TempDir(), bench.SweepConfig{})
	if len(cells) == 0 {
		t.Fatal("the sweep recorded no cells")
	}
	for _, c := range cells {
		if c.TurnsPerSession != 0 || c.PromptTokensPerTurn != 0 || c.SessionPool != 0 {
			t.Errorf("cell %s has no conversations and recorded %d turns of %d tokens over %d sessions",
				c.ID, c.TurnsPerSession, c.PromptTokensPerTurn, c.SessionPool)
		}
	}
}
