package bench

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/parquet-go/parquet-go"

	"github.com/yuchia329/kvroute/internal/record"
)

// Names of the two compacted files a sweep produces.
const (
	RequestsParquet = "requests.parquet"
	CellsParquet    = "cells.parquet"
	RouterParquet   = "router.parquet"
	// RouterRows is where the router is told to append its own rows, relative
	// to the sweep directory. They join to the harness rows by request id.
	RouterRows = "router.jsonl"
)

// Compaction reports what a compaction pass produced.
type Compaction struct {
	Requests     int    `json:"requests"`
	Cells        int    `json:"cells"`
	RouterRows   int    `json:"router_rows"`
	RequestsPath string `json:"requests_path"`
	CellsPath    string `json:"cells_path"`
	RouterPath   string `json:"router_path,omitempty"`
}

// Compact rewrites a sweep's JSONL rows and cell records as Parquet.
//
// The two formats are not redundant, they are the two halves of one decision.
// JSONL is what the run writes: one line, flushed per request, so a sweep that
// dies in its eleventh hour still leaves ten hours of readable rows. Parquet is
// what the analysis reads: hundreds of cells' worth of per-request rows is a
// columnar scan, and every plotting tool reads Parquet natively.
//
// The JSONL is left in place. It is the system of record; this is a derived
// artifact and can be rebuilt from it at any time.
func Compact(dir string) (Compaction, error) {
	cellDir := filepath.Join(dir, "cells")

	// The globs exclude a crashed cell's rows by construction: those are written
	// as <cell>.jsonl.partial and renamed into place only once the cell
	// finishes, and neither pattern matches a .partial suffix.
	rowFiles, err := filepath.Glob(filepath.Join(cellDir, "*.jsonl"))
	if err != nil {
		return Compaction{}, fmt.Errorf("bench: list cell rows: %w", err)
	}
	cellFiles, err := filepath.Glob(filepath.Join(cellDir, "*.json"))
	if err != nil {
		return Compaction{}, fmt.Errorf("bench: list cell records: %w", err)
	}

	result := Compaction{
		RequestsPath: filepath.Join(dir, RequestsParquet),
		CellsPath:    filepath.Join(dir, CellsParquet),
	}
	if result.Requests, err = compact[Result](rowFiles, result.RequestsPath); err != nil {
		return Compaction{}, err
	}
	if result.Cells, err = compact[Cell](cellFiles, result.CellsPath); err != nil {
		return Compaction{}, err
	}

	// The router writes its own rows: accept-to-dispatch overhead and its view
	// of each outcome, keyed by the same request id the harness recorded. They
	// are a second view of the same requests, so they are compacted too — a
	// figure that needs both sides should not have to parse JSONL for one of
	// them. Absent when the router was run without -records.
	routerRows := filepath.Join(dir, RouterRows)
	if _, err := os.Stat(routerRows); err == nil {
		result.RouterPath = filepath.Join(dir, RouterParquet)
		if result.RouterRows, err = compact[record.Request]([]string{routerRows}, result.RouterPath); err != nil {
			return Compaction{}, err
		}
	}
	return result, nil
}

// compact decodes every JSON value in the given files and writes them to one
// Parquet file. Each source file holds either one value per line or a single
// indented value, and a stream decoder reads both.
func compact[T any](sources []string, dest string) (int, error) {
	out, err := os.Create(dest)
	if err != nil {
		return 0, fmt.Errorf("bench: create %s: %w", dest, err)
	}
	defer out.Close()

	// Zstd because these files are long-lived and mostly repeated strings —
	// cell ids, policy names, replica names — which compress to almost nothing.
	writer := parquet.NewGenericWriter[T](out, parquet.Compression(&parquet.Zstd))

	written := 0
	batch := make([]T, 0, 1024)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := writer.Write(batch); err != nil {
			return fmt.Errorf("bench: write %s: %w", dest, err)
		}
		batch = batch[:0]
		return nil
	}

	for _, source := range sources {
		rows, err := decodeFile[T](source)
		if err != nil {
			return 0, err
		}
		for _, row := range rows {
			batch = append(batch, row)
			written++
			if len(batch) == cap(batch) {
				if err := flush(); err != nil {
					return 0, err
				}
			}
		}
	}
	if err := flush(); err != nil {
		return 0, err
	}
	if err := writer.Close(); err != nil {
		return 0, fmt.Errorf("bench: close %s: %w", dest, err)
	}
	return written, nil
}

func decodeFile[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bench: open %s: %w", path, err)
	}
	defer f.Close()

	var rows []T
	decoder := json.NewDecoder(bufio.NewReader(f))
	for {
		var row T
		if err := decoder.Decode(&row); err != nil {
			if errors.Is(err, io.EOF) {
				return rows, nil
			}
			return nil, fmt.Errorf("bench: decode %s: %w", path, err)
		}
		rows = append(rows, row)
	}
}
