package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"
)

// Re-scoring: recomputing a recorded cell's summary from its recorded rows.
//
// The rows are the system of record and the summary is arithmetic over them
// (see result.go), which is what makes a change to that arithmetic checkable
// against everything already measured instead of only against the next run. #33
// changes how a cell is judged to have been still warming up, and 216 recorded
// open-loop cells were decided by the old rule; this reads their rows back and
// says which of them the new rule moves.
//
// It only reads, and it writes nothing back into the cell records. A recorded
// cell says what was concluded at the time it was run, and a re-score that
// edited it in place would erase the thing it is reporting on.

// Rescored is one recorded cell put beside itself as the current check scores
// it.
type Rescored struct {
	// Dir is the sweep directory the cell was read from, so a report over
	// several runs says which run each row belongs to.
	Dir    string `json:"dir"`
	Cell   string `json:"cell"`
	Policy string `json:"policy"`
	Driver Driver `json:"driver"`
	// ArrivalRate and ThinkTime are the two numbers the visit period is made of.
	ArrivalRate float64       `json:"arrival_rate"`
	ThinkTime   time.Duration `json:"think_time"`
	// VisitPeriod is how long this cell takes to walk a conversation from its
	// first turn to its last, and PeriodsMeasured how many of those its measured
	// window held. Zero when the workload states no turns per session or the
	// cell no think time, which is every closed-loop cell.
	VisitPeriod     time.Duration
	PeriodsMeasured float64

	// Was is the summary the run recorded; Now is the summary the current check
	// computes from the same rows.
	Was Summary
	Now Summary
}

// Changed reports whether the drift check's verdict on this cell moved. The
// whole verdict and not a flagged/clear bit: a cell that stays flagged for a
// different cause has still had its remedy changed, which is half of what the
// re-score exists to report.
func (r Rescored) Changed() bool {
	return r.Was.WarmupDriftVerdict() != r.Now.WarmupDriftVerdict()
}

// Reproduces reports whether everything the drift change does not touch came
// back the same. A cell where it did not is one whose rows are not the rows its
// record was written from, and no verdict read off them means anything.
func (r Rescored) Reproduces() bool {
	return r.Was.Requests == r.Now.Requests &&
		r.Was.Successes == r.Now.Successes &&
		r.Was.Warmup == r.Now.Warmup &&
		r.Was.WindowNs == r.Now.WindowNs &&
		r.Was.TTFTP50Ns == r.Now.TTFTP50Ns &&
		r.Was.TTFTP99Ns == r.Now.TTFTP99Ns &&
		r.Was.SLOViolations == r.Now.SLOViolations
}

// Rescore reads every cell in dirs and re-scores it from its own rows.
//
// threshold is the drift threshold to judge against, for the reason the runs
// themselves take one: it is a judgement, and a re-score that hard-coded it
// could not show what a different one would have decided.
func Rescore(dirs []string, threshold float64) ([]Rescored, error) {
	var out []Rescored
	for _, dir := range dirs {
		cells, err := LoadCells(dir)
		if err != nil {
			return nil, err
		}
		rows, err := LoadCellRows(dir)
		if err != nil {
			return nil, err
		}
		for _, cell := range cells {
			of, ok := rows[cell.ID]
			if !ok {
				return nil, fmt.Errorf("bench: %s: cell %s has a record but no rows, so it cannot be re-scored", dir, cell.ID)
			}
			out = append(out, rescoreCell(dir, cell, of, threshold))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bench: no cells in %v", dirs)
	}
	return out, nil
}

func rescoreCell(dir string, cell Cell, rows []Result, threshold float64) Rescored {
	// Every threshold but the drift one is taken from what the cell recorded, so
	// that a summary which comes back different came back different for the
	// reason being tested and not because it was judged against another rule.
	turns, _ := turnsPerSession(cell.Workload)
	now := Summarize(rows, SummaryOptions{
		SLO:                  SLO{TTFT: time.Duration(cell.Summary.SLOTTFTNs), ITL: time.Duration(cell.Summary.SLOITLNs)},
		FailureThreshold:     cell.Summary.FailureThreshold,
		WarmupDriftThreshold: threshold,
		ScheduleLagThreshold: time.Duration(cell.Summary.ScheduleLagThresholdNs),
		TurnsPerSession:      turns,
	})

	r := Rescored{
		Dir:         dir,
		Cell:        cell.ID,
		Policy:      cell.Policy,
		Driver:      cell.Driver,
		ArrivalRate: cell.ArrivalRate,
		ThinkTime:   time.Duration(cell.ThinkTimeNs),
		Was:         cell.Summary,
		Now:         now,
	}
	if turns > 0 && cell.ThinkTimeNs > 0 {
		r.VisitPeriod = time.Duration(turns) * time.Duration(cell.ThinkTimeNs)
		if r.VisitPeriod > 0 {
			r.PeriodsMeasured = float64(now.WindowNs) / float64(r.VisitPeriod)
		}
	}
	return r
}

// turnsPerSessionPattern is where a workload's name states its turns per
// session.
//
// Off the name rather than re-derived, because the name is the run's only record
// of what it offered and a re-score that rebuilt the generator would be
// reporting on a workload it had constructed rather than the one that ran.
var turnsPerSessionPattern = regexp.MustCompile(`turns=(\d+)`)

func turnsPerSession(workload string) (int, bool) {
	m := turnsPerSessionPattern.FindStringSubmatch(workload)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// LoadCellRows reads a sweep's per-request rows, keyed by the cell that produced
// them, from whichever form the directory keeps them in.
//
// Both forms, because a sweep keeps both and the repository keeps only one of
// them. The run writes one JSONL file per cell, flushed per request, and
// compaction rewrites all of them as a single requests.parquet (ADR-0002); a
// measurement published into docs/ has usually had the JSONL left behind on the
// box. A re-score that could read only the live form could not re-score anything
// already published, which is the entire point of it.
func LoadCellRows(dir string) (map[string][]Result, error) {
	perCell, err := filepath.Glob(filepath.Join(dir, "cells", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("bench: list cell rows in %s: %w", dir, err)
	}
	if len(perCell) > 0 {
		rows := map[string][]Result{}
		for _, path := range perCell {
			of, err := decodeFile[Result](path)
			if err != nil {
				return nil, err
			}
			id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
			rows[id] = of
		}
		return rows, nil
	}

	compacted := filepath.Join(dir, RequestsParquet)
	if _, err := os.Stat(compacted); err != nil {
		return nil, fmt.Errorf("bench: %s holds neither cells/*.jsonl nor %s, so its cells cannot be re-scored from their rows", dir, RequestsParquet)
	}
	all, err := parquet.ReadFile[Result](compacted)
	if err != nil {
		return nil, fmt.Errorf("bench: read %s: %w", compacted, err)
	}
	rows := map[string][]Result{}
	for _, row := range all {
		rows[row.CellID] = append(rows[row.CellID], row)
	}
	return rows, nil
}
