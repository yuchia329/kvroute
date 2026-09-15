# ADR-0004: Every measurement sends bytes the fleet has not seen

**Status:** Accepted · **Date:** 2026-09-07

## Context

The workload generator is deterministic in `(user, turn)` on purpose. A cell that is re-run has to
send the same bytes, or the cache it is measuring is not the same cache, and two policies compared
on different prompts are not being compared at all.

That determinism has a consequence nobody had drawn out. A cell's virtual users are numbered
`0..concurrency-1` and its turns from zero, so **every cell sends the same prompts as every other
cell** — the second repetition re-sends the first's, and concurrency 16 re-sends concurrency 8's.
The replicas run with `--enable-prefix-caching`, so the second time a prompt arrives it is not
prefilled at all.

This was caught on 2026-09-06 by the characterization pass, as a fleet that appeared to be running
seven times faster than it was:

| | TTFT p50 |
|---|---|
| Prompts the replica had not seen | **~325 ms** |
| The same prompts sent a second time | **~46 ms** |

Nothing in the latency says which of the two happened. The fast rows are clean, low-variance and
plausible; they simply describe the prefix cache rather than the hardware. The run that exposed it
had been interrupted and restarted, so the replicas were still holding the killed run's prompts,
and the number of "fast" requests on each replica matched exactly how many the killed run had sent
it — 40, 38, 39, 17, 0, 0.

Left alone, this would have put a seven-times-too-low floor under the SLO and made every
repetition after the first measure a warm cache.

## Decision

**1. The workload's user space is partitioned, and the partition is derived from the axes.**
`bench.Shifted` moves a workload's user space by an offset; `bench.CellWorkloadOffset(concurrency,
repetition)` computes a cell's offset from its own axes and **not** from its policy. So:

- two cells differing in repetition or concurrency send different bytes, and
- the same cell under two policies sends identical bytes, which is what makes policies comparable,
- and a resumed cell re-sends its own bytes, which is what ADR-0002's caching needs.

Each characterization probe likewise sends from its own slice, keyed on its position in the run.

**2. Every probe records the engine's own prefix-cache hit rate over its window.** The counters are
scraped before and after and the delta is kept on the probe. This is the evidence, not the
inference: a measurement that read the cache instead of prefilling is visible as a number rather
than deduced from a latency that looks suspicious.

**3. The latency floor refuses to be used if that rate is above 5%.** `Floor.Usable` fails, the
characterization is flagged, and the reason says what to do — restart the fleet, or send bytes it
has not seen. Five percent is far above the floor's irreducible hit, which is the chat template's
first block on every request, and far below anything that could move the median.

## Consequences

- A characterization is only valid against a fleet whose replicas have not already served its
  prompts. In practice that means restarting the fleet before a re-run, and the flag says so when
  it has not been done.
- Partitioning by axis means a full sweep touches a much larger span of the workload's user space.
  Nothing depends on that span being small.
- **Cells cached before this change were run on different bytes.** A cell's cache key is its id,
  which has not changed, so a resumed sweep will happily load an old cell beside a new one that
  sent different prompts. Any sweep directory that predates this must be deleted rather than
  resumed. Only the two 20-second bring-up cells are affected in practice, and they are kept as a
  reference measurement rather than resumed.
- The three counters this rests on — `vllm:prefix_cache_hits_total` and
  `vllm:prefix_cache_queries_total`, plus the `vllm:cache_config_info` labels — are declared in
  `internal/vllmmetrics` and asserted against every live replica by `test/contract`, so a version
  drift that removed them fails loudly rather than reporting a hit rate of zero.

## What this does not fix

Two gaps are left open deliberately.

Prompts do not cross cells any more, but they still cross **runs**: a second sweep against the same
fleet re-sends the first one's bytes. Bringing the fleet down between passes is the operating
procedure until something prevents it.

And the hit-rate evidence exists **only on characterization probes**, not on the sweep's cells. A
cell driven against a warm fleet would be just as wrong and would not say so. Extending the same
before-and-after scrape to cells is the obvious next step and is not done here; a cell scrapes all
six replicas rather than one, which is a different piece of work from this one.
