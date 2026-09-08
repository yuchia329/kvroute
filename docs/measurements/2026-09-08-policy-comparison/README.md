# Round-robin against least-outstanding, 2026-09-07/08

The first two-policy comparison: 45 cells per policy across both load axes, ~122,500 requests
through the router, on six RTX 3090s. Everything here is recomputable from the rows in this
directory.

## The result

**Least-outstanding completes more requests than round-robin at every load level, and meets the
SLO on fewer of them.** Throughput is up to 28% higher; goodput is up to 32% lower.

| driver | load | round_robin | least_outstanding | Δ |
|---|---:|---:|---:|---:|
| closed-loop | 1 users | 1.30 (1.30–1.31) | 1.28 (1.26–1.34) | −1.7% (within spread) |
| closed-loop | 4 users | 5.01 (4.98–5.08) | 4.99 (4.93–5.08) | −0.3% (within spread) |
| closed-loop | 8 users | 7.50 (7.48–7.58) | **8.01 (7.92–8.03)** | **+6.8%** |
| closed-loop | 16 users | 10.34 (10.31–10.35) | **10.56 (10.52–10.67)** | **+2.2%** |
| closed-loop | 32 users | **12.34 (12.33–12.37)** | 10.28 (9.84–10.57) | **−16.7%** |
| closed-loop | 64 users | **9.80 (9.78–9.84)** | 8.21 (8.17–9.31) | **−16.2%** |
| closed-loop | 128 users | **9.44 (9.42–9.47)** | 6.41 (5.53–6.64) | **−32.1%** |
| closed-loop | 256 users | — (all three cells excluded) | 0.37 (0.35–0.50) | — |
| open-loop | 4 req/s | 4.00 | 4.00 | +0.0% (within spread) |
| open-loop | 8 req/s | 8.00 | 8.00 | +0.0% (within spread) |
| open-loop | 12 req/s | **12.00 (12.00–12.00)** | 11.19 (10.72–11.39) | **−6.8%** |
| open-loop | 16 req/s | 2.69 (0.02–3.46) | 0.05 (0.02–0.37) | −98.3% (within spread) |
| open-loop | 24, 32, 48 req/s | 0.00 | 0.00 | both collapsed |

Median of three repetitions, range in brackets. Goodput is requests per second meeting
**TTFT < 990 ms and inter-token p50 < 24 ms**, read from the
[characterization record](../2026-09-07-characterization/) rather than retyped — three times the
measured floor.

The crossover sits between **16 and 32 virtual users**. Below it, least-outstanding's balancing is
a small win. At and above it, round-robin wins and the margin grows with load.

## Why: a slow replica is worth more as a queue than as a peer

The fleet is not homogeneous. GPU 3 thermally throttles under sustained six-card load
([#25](../2026-09-07-gpu3-thermal/)), and that single fact drives the whole result.

Per-replica behaviour at concurrency 128:

| replica | round_robin TTFT p50 | round_robin met SLO | least_outstanding TTFT p50 | least_outstanding met SLO |
|---|---:|---:|---:|---:|
| replica-0 | 381 ms | 92.6% | 1217 ms | 43.5% |
| replica-1 | 363 ms | 100.0% | 1016 ms | 47.0% |
| replica-2 | 376 ms | 99.8% | 1087 ms | 45.7% |
| **replica-3** | **6748 ms** | **0.0%** | 1479 ms | 28.8% |
| replica-4 | 363 ms | 100.0% | 1040 ms | 46.1% |
| replica-5 | 358 ms | 100.0% | 1148 ms | 40.0% |
| **fleet** | | **82.1%** | | **42.2%** |

Round-robin sends the throttling card exactly its 1/6 and lets the consequences pool there. That
replica becomes a 6.7-second queue which passes nothing — and which, by holding those requests,
keeps them out of everyone else's way. The healthy five run at ~370 ms and pass essentially
everything. The fleet loses the sixth of its traffic that landed on the bad card and keeps the rest.

Least-outstanding does exactly what it is designed to do: it sees inflight climbing on replica-3
and routes away, sending it 14.70% of traffic against round-robin's exact 16.67%. But the work does
not disappear. It moves onto the healthy five, whose TTFT rises from ~370 ms to ~1100 ms — across
the 990 ms threshold. Balancing converted "five replicas comfortably inside the SLO and one hopeless"
into "six replicas all just outside it".

**Goodput counts requests under a threshold, so a bimodal distribution can beat a balanced one.**
Least-outstanding minimises the maximum queue, which is the right objective for a mean or for a
fleet-wide p99. It is the wrong objective for a threshold, on a fleet with one bad member.

Throughput and goodput therefore point in opposite directions, which is why this project counts
them separately:

| level | round_robin tput → goodput | least_outstanding tput → goodput |
|---|---:|---:|
| c8 | 7.50 → 7.50 | 8.01 → 8.01 |
| c16 | 10.34 → 10.34 | 10.73 → 10.56 |
| c32 | 12.34 → 12.34 | 13.02 → 10.28 |
| c64 | 11.74 → 9.80 | 14.32 → 8.21 |
| c128 | 11.49 → 9.44 | **14.72** → 6.41 |

## What this result does not say

**It is not "least-outstanding is a worse policy".** It is a measurement of two policies on a fleet
with one thermally throttling card, under a threshold metric. On a symmetric fleet there is no
sacrificial queue to exploit and no reason to expect this ordering; the mechanism above requires the
heterogeneity. Anyone quoting the −32% must quote GPU 3 with it.

**It does not separate the two cache-aware policies.** Neither policy here tracks prefixes, so this
says nothing yet about the project's actual question.

**The 16 req/s open-loop rung is not resolved.** Goodput falls from 12.00 to ~2.7 between 12 and
16 req/s and to zero by 24, so the knee is bracketed but not located. Its round-robin repetitions
range from 0.02 to 3.46, which is why the −98.3% carries "within spread" and should not be read as a
result. A ladder between 12 and 16 would locate it.

**Concurrency 256 has no round-robin figure.** All three of its cells were flagged as still warming
up — the first half of the measured window ran 30–57% slower than the second, over the 25%
threshold — so they are excluded rather than averaged in. A 25-second warm-up does not clear the
initial 256-request thundering herd. Recovering that point means re-running it for both policies
with a longer warm-up, and, because both policies send identical bytes, a fleet restart before each.

## Conditions

- **Fleet**: six replicas, vLLM 0.28.0, `Meta-Llama-3.1-8B-Instruct-AWQ-INT4`, one per RTX 3090,
  brought up under `ops/fleet.sh` behind the preflight. Engine settings pinned in
  `ops/versions.env` and identical across both policies.
- **Workload**: `fixed(prompt=2048B,output=64t,seed=1)` — the same generator, and the same bytes at
  each load point, for both policies. That is what makes them comparable.
- **Only the policy varied.** Two routers in turn against a fleet whose engine configuration never
  changed.
- **The fleet was restarted between the two policy passes**, per
  [ADR-0004](../../adr/0004-every-measurement-sends-unseen-bytes.md). Both policies send identical
  prompts, so the second pass would otherwise have read the caches the first left warm.
- **Cleanliness**: all 90 cells recorded zero foreign processes across all six GPUs, sampled
  throughout each cell. The three excluded cells were excluded for warm-up drift, not contamination.

## Evidence that the measurement is not reading its own cache

The engine's own prefix-cache counters, scraped from all six replicas before and after each pass:

| pass | queries | hits | hit rate |
|---|---:|---:|---:|
| round_robin | 77,985,271 | 1,017,888 | **1.305%** |
| least_outstanding | 79,707,459 | 1,038,528 | **1.303%** |

Summed across all six replicas, each pass's counters differenced end against start. Least-outstanding
started from **exactly zero on every replica**, which is the fleet restart working: the second policy
prefilled every prompt from cold rather than reading what the first pass left behind. (Round-robin
started at 528 queries — the contract test and the fleet warm-up, before its first cell.)

ADR-0004 rejects a floor measured above a 5% hit rate. 1.30% matches the characterization's own
figure and is the chat template's irreducible leading block.

This closes, for this run only, the gap ADR-0004 leaves open — cells still do not carry their own
hit rate, so this was scraped around each pass by hand.

## Router overhead

| policy | requests | p50 | p99 | max |
|---|---:|---:|---:|---:|
| round_robin | 60,595 | 132.8 µs | 231.1 µs | 29.1 ms |
| least_outstanding | 61,934 | 134.6 µs | 236.8 µs | 17.3 ms |

Counting inflight and choosing the least-loaded replica costs about **6 µs at p99** over a stateless
rotation, against a fleet whose own TTFT is measured in hundreds of milliseconds. The max includes
first-connection setup.

## Files

- `comparison.md` — the table as `cmd/compare` emitted it.
- `concurrency/`, `goodput/` — one cell record per cell (`cells/*.json`), and every per-request row
  compacted to Parquet (`requests.parquet`, `cells.parquet`). The raw per-cell JSONL is not kept:
  at 64 MB it is the same rows the Parquet holds, which is what ADR-0002 prescribes.
- `router-*.jsonl.gz` — the router's own row per request, carrying the inflight the decision was
  weighed on and the replica it chose. This is what the per-replica analysis above is computed from.
- `evidence/` — both benches' logs, both routers' logs, the fleet restart, and the prefix-cache
  counters before and after each pass.
