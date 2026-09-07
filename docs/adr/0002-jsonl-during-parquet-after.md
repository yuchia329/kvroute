# ADR-0002: JSONL during the run, Parquet after it

**Status:** Accepted · **Date:** 2026-09-06

## Context

`idea.md` §6 settles that the harness, not Prometheus, is the system of record: Prometheus is a
sampled TSDB at 1–15 s resolution and the wrong shape for per-request TTFT p99 across ~220 cells.
That leaves the question of what the harness writes, and the two requirements pull opposite ways.

**During a run**, the format has to survive a crash. A sweep is hours long on a box that is shared
from Sep 20, and a run that dies in its eleventh hour has to leave the ten before it readable. That
means one flushed record per request, appended, with no footer or index that only becomes valid at
the end.

**After a run**, the format has to be scanned by column. Hundreds of cells' worth of per-request
rows is a columnar workload — "TTFT p99 by policy and concurrency" touches three columns of
millions of rows — and every plotting tool the analysis phase will use reads Parquet natively.

No single format is good at both. Parquet's metadata is written at the end, so a killed writer
leaves an unreadable file. JSONL parses one row at a time whether you wanted one column or all of
them.

## Decision

Write both, and be explicit about which is which.

1. **JSONL is the system of record.** `internal/record.Writer` appends one line per row and flushes
   it, so a crash loses at most the row in flight. Every published figure has to be recomputable
   from these rows.

2. **Parquet is a derived artifact**, written by `bench.Compact` after the sweep and rebuildable
   from the JSONL at any time. Compaction never deletes the rows it read.

3. **A cell's rows are renamed into place**, not written under their final name. Rows stream to
   `<cell>.jsonl.partial` and are renamed only once the cell finishes; the cell record
   `<cell>.json` is written last and is what marks the cell complete. A crashed cell therefore
   leaves readable partial data under a visibly incomplete name, and can never be mistaken for a
   cached cell on resume.

   **An interrupted cell is not a cell.** If the sweep's context is cancelled while a cell is
   running, neither rename happens and no cell record is written, so the next pass re-runs it. The
   `.partial` rows are left behind for forensics. Without this the SIGINT that stops a sweep would
   silently bake a truncated cell into the results, which is worse than losing it: it is a cache
   entry that looks finished.

4. **Compaction reads `cells/*.jsonl` and `cells/*.json`**, which cannot match a `.partial` suffix.
   A crashed cell's rows are therefore excluded from the compacted output by construction, without
   a filter that could be wrong.

5. **A `Result` row is flat scalars.** No nested objects, no `time.Time` — timestamps are Unix
   nanoseconds. The Parquet schema then falls out of the Go struct with no mapping layer, and every
   column is one a plotting tool can group by.

   A **`Cell`** is flat scalars *plus two repeated string columns*: `flag_reasons` and
   `foreign_procs`. This is a deliberate exception, not an oversight. Both hold sentences — why a
   cell must not be averaged in, and which process was on which card holding how much — and both
   are naturally zero-or-many. Parquet expresses a repeated string natively and every reader
   presents it as a list, whereas joining them into one delimited string would make the field
   unparseable at exactly the moment someone needs to know why a cell was excluded.

6. **Resume is a `stat` of the cell record, with two exceptions**, both of which exist so that
   "cached" never means "stuck":

   - A **contaminated** cached cell (a foreign process was seen, or a probe failed) is moved to
     `discarded/` and re-run. `idea.md` §6 requires such a cell to be discarded and re-run; leaving
     it cached would make that mean "delete the file by hand first". It lands outside `cells/` so
     compaction cannot pick it up as a result.
   - A cached cell summarised against a **different SLO** is resummarised from its own rows and
     written back, without re-running. The SLO is derived from the measured concurrency-1 floor and
     so necessarily arrives after the cells it applies to; SLO violation is a property of the rows,
     which is the whole reason the rows are the system of record. Putting the SLO in the cell's
     cache key instead would cost hours of GPU time to change a threshold.

This takes `github.com/parquet-go/parquet-go`, which is the repo's **first dependency** and brings
nine modules with it. That is a real cost against a repo whose dependency-free-ness was worth
something. It is paid because the alternative is a bespoke columnar format nobody's tooling can
read, which would make the analysis phase carry a reader of its own — a much worse trade than nine
modules in `go.sum`.

## Consequences

- A sweep is resumable by construction. Resume is a `stat` of the cell record, not a bookkeeping
  file that could disagree with what is on disk.
- Disk holds both formats. At ~220 cells this is megabytes, and zstd on the Parquet side makes the
  derived copy the smaller of the two.
- Every figure stays auditable back to a per-request row.
- `go.sum` is no longer empty, and a `go mod tidy` on this repo now touches the network.

## Revisit if

- The analysis phase turns out to want a database rather than files, in which case the JSONL is
  still the load path and Parquet becomes the intermediate.
- Row volume grows enough that compaction cannot hold one cell's rows in memory, at which point
  `decodeFile` streams into the writer instead of returning a slice.
