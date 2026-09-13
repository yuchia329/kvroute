# Belief divergence against the age of the belief — 2026-09-12

How far the router's prefix index was from what the engines actually held, plotted against how
long it had been since that session was last served. This is #29's re-run of the recency axis
[#17 measured on 2026-09-10](../2026-09-10-belief-divergence/) and could not publish, because
every one of its six cells was flagged as still warming up.

**The curve is published here.** The decay #17 recorded as indicative is real: it survives on
six cells that carry no flag of any kind.

| | |
|---|---|
| report | [`recency.md`](recency.md), and per configuration [`recency-think30.md`](recency-think30.md) / [`recency-think75.md`](recency-think75.md) |
| cells | 6 — 2 configurations × 3 repetitions, [`think30/`](think30/) and [`think75/`](think75/) |
| requests | 20,160 measured, **20,160 of which carried the engine's own account of their prompt** |
| policy | prefix affinity, **spill off** |
| workload | WS 3, skew 1, open-loop at 8 req/s |
| fleet | GPUs 0 1 2 4 5, KV cache events **off** — #17's engine configuration to the value |
| status | **clean; no cell carries a flag of any kind** |

## Why this is a new geometry and not just a longer warm-up

#29's first acceptance criterion asks for the same point re-run "changing only the warm-up".
The recorded rows of #17's run say that is half the story, and the other half decides the cell
length. TTFT p50 per **visit period**, three repetitions each:

```
think 30s (period 120s)   p0 288ms  p1  81ms  p2 61ms
                          p0 346ms  p1 252ms  p2 62ms
                          p0 340ms  p1 254ms  p2 65ms
think 75s (period 300s)   p0 291ms  p1  60ms
                          p0 292ms  p1  59ms
                          p0 296ms  p1 64ms
```

TTFT in those cells is not drifting down towards a steady state. It is a **sawtooth**, and its
period is the workload's own. The rotation maps the k-th arrival to turn k/pool, so the turn
index *is* the round: every conversation in the pool advances together, and every
`TurnsPerSession` rounds the whole pool rolls over to freshly drawn sessions at once. A round
takes a think time, so the pool walks a whole session every `TurnsPerSession × think time` —
the visit period — and TTFT climbs across each period as prompts grow with their history, then
drops back when the rollover returns every slot to a short first turn.

Two different defects follow from that, one per configuration:

- **think 30s** draws every turn index evenly across the drift check's split, so its sawtooth
  cancels. What flagged it is the genuinely cold opening period: the window opened 75 s into a
  120 s period, so 45 s of the coldest traffic in the cell sat in the first half and none of it
  in the second. Here a longer warm-up really is the whole fix, as #29 assumed.
- **think 75s** is the other case. A period is 300 s and the whole cell was 420 s, so the
  window could not hold even one: its halves shared **no turn index at all** — index 0 ran 600
  requests to nil across the split, index 2 nil to 600. **No warm-up clears that**, because it
  never decays. The check was comparing two different workloads and would have fired on a fleet
  that had been up for a week.

So both cells are sized to open on a visit boundary, after the periods the rows show were still
settling, and to measure exactly **two whole visit periods** — one per half of the drift check,
which leaves it comparing the fleet against itself:

| | think time | visit period | warm-up | cell | measured |
|---|---:|---:|---:|---:|---:|
| think30 | 30 s | 120 s | 240 s (2 periods) | 480 s | 240 s (2 periods) |
| think75 | 75 s | 300 s | 300 s (1 period) | 900 s | 600 s (2 periods) |

That arithmetic is derived against the generator with no fleet running, in
`internal/bench/recencywindow_test.go`, which also re-asserts that the longer cells still
spread the axis they exist to plot — the way of "fixing" the flag that would have bought
nothing. Everything else is #17's point to the value.

⚠️ **These cells are therefore not poolable with #17's six.** A cell's measured window is its
length less its warm-up, so two geometries are two measurements and not two repetitions of one.
They are in their own directory for that reason, and #17's recency cells stand as the flagged
record of what a fractional window measures.

## The flags cleared, and the arithmetic is why

| cell | drift on 2026-09-10 | drift here | periods in the measured window |
|---|---:|---:|---:|
| think30 r1 / r2 / r3 | +0.936 / +2.311 / +1.933 | **−0.079 / −0.115 / −0.145** | 2.00 / 2.00 / 2.00 |
| think75 r1 / r2 / r3 | +3.246 / +4.248 / +0.514 | **−0.571 / −0.690 / −0.562** | 2.00 / 2.00 / 2.00 |

Every drift is now **negative** — the first half slightly faster than the second — which is the
residual sawtooth across two whole periods showing up in the direction the check does not fire
on. The warm-up sizing is confirmed by the same rows that set it: think30's p0 came in at
291–309 ms and its p1 at 80–82 ms, so two periods of warm-up were both needed and sufficient,
and think75's p1 was already at steady state, so one was.

## Result: the belief decays, and it decays at the TTL

| since last served | requests | honoured | mean tokens over-predicted |
|---|---:|---:|---:|
| first turn | 1,029 | 98.5% | 50 |
| <1 s | 5,523 | 98.9% | 859 |
| 1 s – 2 s | 1,242 | 99.8% | 401 |
| 2 s – 5 s | 1,964 | 99.4% | 603 |
| 5 s – 10 s | 1,699 | 99.1% | 461 |
| 10 s – 30 s | 3,495 | 97.6% | 1,084 |
| 30 s – 1 m | 2,656 | **86.0%** | **1,793** |
| 1 m – 2 m | 2,082 | 98.1% | 56 |
| ≥ 2 m | 470 | 98.1% | 71 |

The index's TTL is measured at **57 s** from the engines' own idle-before-evict tail, and the
trough lands in the bucket beneath it. Past the TTL the index has dropped the belief, so it
claims very little — predicted/req falls from 1,818 to 481 — and what little it still claims is
honoured again. The shape is the TTL, read from both sides.

**This half of the curve is new.** #17's think30 cells put 8 requests in the 1 m – 2 m bucket;
there are 2,082 here, because think75's longer think time is what reaches past the TTL at all.
The recovery is no longer resting on a handful of requests.

### Two configurations, read separately

Both were run so the shape at one pressure could be checked against the shape at another —
think30's pool is 0.78× the fleet's KV and think75's is 1.95×.

| since last served | think30 | think75 |
|---|---:|---:|
| 1 s – 2 s | 100.0% | 99.7% |
| 10 s – 30 s | 97.2% | 97.8% |
| 30 s – 1 m | **82.8%** | **88.3%** |
| 1 m – 2 m | *(8 requests)* | 98.1% |

Same shape, same trough, at pressures a factor of 2.5 apart. The absolute depth differs by 5.5
points and the ordering is not the naive one — the *less* pressured configuration decays
further — which is worth not over-reading: these are two cells' worth of fleet, not a pressure
axis. The pressure axis is [the working-set one](../2026-09-10-belief-divergence/working-set.md).

### The pressure trend inside think75's cells does not reach the curve

think75's two measured visit periods differ in TTFT p50 by a factor of two to three — 94–167 ms
then 281–297 ms. The drift check does not fire on it, because the cell gets *slower* rather than
faster, but it asks a different question than the flag does: at 1.95× the fleet's KV, eviction
pressure accumulates as more distinct sessions pass through, and if the later period were also
contributing unevenly to the recency buckets then the curve would be mixing recency with
pressure — the confound the single-cell design exists to exclude.

Recomputing the curve **inside each measured period separately** says it does not:

| since last served | p1 | p2 |
|---|---:|---:|
| 1 s – 2 s | 100.0% (n=454) | 99.4% (n=417) |
| 10 s – 30 s | 97.9% (n=1,086) | 97.7% (n=939) |
| 30 s – 1 m | **88.8% (n=772)** | **87.7% (n=760)** |
| 1 m – 2 m | 98.2% (n=486) | 98.0% (n=580) |

Every bucket matches within about a point, and the buckets are populated comparably from both
periods. The decay is a within-period fact and the trend across periods does not reach it.

## What else the run says

**The index was cap-bound.** It reached 16,311 of its 16,311 nodes on every cell, so the cap
remains a candidate for what the over-prediction is measuring rather than something the run
exonerates.

**The two accounts of computed prefill agree to 2.4%** — 14,899,153 tokens summed off the
per-request rows against 15,261,989 off the fleet's own counters over the same measured windows.
They cover slightly different windows and are printed rather than reconciled. It is a wider gap
than #17 saw (0.1% on its recency cells, 0.5% on its working-set ones) and far from a gross
disagreement, but it is the looser of the two cross-checks recorded so far and is written down
rather than smoothed.

**Every cell is clean on contamination as well as drift**: the GPUs were sampled throughout and
no foreign process held memory on any of the five.

## What is here

Cell records and the reports. The per-request rows behind every figure above are ~26 MB per
configuration and stay on the box under `runs/recency-rerun/`; each figure is recomputable from
them with `cmd/divergence`, which reads only and needs no fleet and no GPU.

Two of the tables above are not `cmd/divergence`'s, so the scripts that drew them are in
[`evidence/`](evidence/), byte for byte as they ran on the box:
[`period-check.py`](evidence/period-check.py) for the per-visit-period TTFT and the split the
drift check made, and [`period-confound.py`](evidence/period-confound.py) for the curve recomputed
inside each period. Both read the rows only. `period-check.py` run against #17's cells reproduces
the 288/81/61 ms that set this run's geometry, which is the check that it is reading what it
claims to.

The driver is [`ops/box/run-recency-rerun.sh`](../../../ops/box/run-recency-rerun.sh), committed
before the run rather than after it, so the geometry could be reviewed before the fleet time was
spent.

## The WS 3 rung

The driver's third stage completes the working-set axis rather than this one, so it is written up
where it belongs: [`../2026-09-10-belief-divergence/`](../2026-09-10-belief-divergence/), whose
axis now has all four of its points and whose node-cap calibration has been re-derived over the
population that changed.

It took two attempts. On the first, another user took all six cards during the twenty seconds
`fleet_cycle` releases them for between stages (ADR-0004) and the fleet could not come back up; the
driver's own `fatal` released ours and wrote nothing further, and the two recency configurations
were already recorded and unaffected. It ran on the second attempt about two hours later, clean on
all three cells.

That release window is worth naming, because no gate closes it: between stages a driver has
deliberately given the cards up, and on a shared box they may be gone when it asks for them back.
The cost is bounded — a stage, not a run — precisely because each stage writes its cells before the
next one cycles.
