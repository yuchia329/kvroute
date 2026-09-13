# The warm-up drift check, rebuilt and re-scored — 2026-09-13

[#33](https://github.com/yuchia329/kvroute/issues/33). The check that decides whether a cell was
still warming up was one-sided, split on the wrong window, and compared two populations the
workload's own period had made different. It is now two-sided, splits on the arrival window, and
compares each turn index with itself.

**216 recorded open-loop cells were re-scored from their own rows.** No fleet, no GPU: the rows are
the system of record and a cell's summary is arithmetic over them, so a change to that arithmetic
can be held against everything already measured. 26 of the 216 were flagged before; 111 are now,
and 118 change verdict. Every one of the 216 reproduced its recorded requests, successes, window,
goodput and TTFT percentiles exactly — only the drift fields moved.

| | |
|---|---|
| `open-loop-rescore.md` | the 216 open-loop cells: transitions, and every cell whose verdict changed |
| `open-loop-every-cell.md` | the same, listing all 216 |
| `closed-loop-rescore.md` | 300 closed-loop cells whose rows are in this repository, as a control |

Rebuild either with `go run ./cmd/rescore <sweep dir>...`. The rows for `superseded-goodput` and the
four recency directories live on the box rather than in this repository, so those runs were scored
against rows copied down from `~/kvroute/runs/{mt-comparison,recency,recency-rerun}`; the integrity
check above is what confirms the copies are the rows those cells were written from.

## What was wrong, and what each defect cost

### It was one-sided

`flag` tested `drift > threshold`, so it fired only on a first half that was *slower*. A cell that
got two and a half times slower across its window was published carrying no flag at all.

**99 of the 216 cells go from clear to flagged on this alone**, and the verdict is a clean monotone
function of offered rate:

| offered rate | 2–4/s | 6/s | 8–14/s | 16/s | 20/s and up |
|---|---:|---:|---:|---:|---:|
| cells now flagged as the fleet slowing | 0 of 42 | 0 of 18 | 42 of 96 | 22 of 24 | 36 of 36 |

That is saturation, not a mystery. The three-policy comparison puts the cache-blind policies' knee
at 6–7 requests per second and session affinity's at 12–14; past its knee an open-loop cell is
offered more than the fleet can serve, its queue grows for as long as the cell runs, and its
latency percentiles are a transient rather than a steady state. The flag now says so. It does not
touch those cells' goodput, which is counted over the arrival window and is exactly the figure a
past-the-knee cell exists to report.

### It split on the wrong window

`last` was the maximum `EndedAt()` over the measured rows — the final *response*, not the final
arrival — so the midpoint sat half the cell's drain past the arrival midpoint.

The drain is not a rounding error. Across these 216 cells it runs to **4 m 13 s** past the last
arrival, and in **49 of them the old midpoint fell so far past the last arrival that the late half
held fewer than five successes**. Below that floor the check returns zero, so those 49 cells
recorded a drift of `+0.000` and no flag: the check was not lenient there, it was blind, at exactly
the load levels where the headline goodput number is taken.

    round_robin-a4-r1    arrivals 1m5s   rows 1m6s    drain 1s      split +33s    early/late 132/128
    round_robin-a16-r1   arrivals 1m5s   rows 1m32s   drain 27s     split +46s    early/late 733/307
    round_robin-a24-r1   arrivals 1m5s   rows 2m25s   drain 1m20s   split +1m13s  early/late 1561/0
    round_robin-a48-r1   arrivals 1m5s   rows 5m18s   drain 4m13s   split +2m39s  early/late 3121/0

### It was blind to what the halves were made of

Under the open-loop rotation the turn index is the round, so the whole conversation pool advances
together and rolls over every `TurnsPerSession` rounds. TTFT is periodic with period
`TurnsPerSession × think time`, and a window holding a fractional number of those periods compares
two different workloads.

Comparing within each turn index cancels that for any cell length, any warm-up and any rate, with
no geometry arithmetic. **15 cells go from flagged to clear on this alone**, and ten of them are
the ones the three-policy measurement had already written off as a warm-up that was too short:

| run | cell | recorded drift | per-index drift |
|---|---|---:|---:|
| `goodput` | `round_robin-a2-r1` | +0.681 | −0.004 |
| `goodput` | `round_robin-a2-r2` | +0.636 | +0.216 |
| `goodput` | `round_robin-a2-r3` | +0.536 | −0.022 |
| `goodput` | `round_robin-a4-r1` | +0.598 | +0.092 |
| `goodput` | `round_robin-a4-r2` | +0.457 | −0.050 |
| `goodput` | `round_robin-a4-r3` | +0.536 | −0.076 |
| `goodput` | `least_outstanding-a2-r1` | +0.542 | +0.107 |
| `goodput` | `least_outstanding-a2-r3` | +0.483 | −0.035 |
| `goodput` | `least_outstanding-a4-r1` | +0.491 | −0.136 |
| `goodput` | `least_outstanding-a4-r2` | +0.432 | +0.035 |
| `superseded-goodput` | `least_outstanding-a4-r1` | +0.505 | −0.133 |
| `superseded-goodput` | `least_outstanding-a4-r2` | +0.503 | −0.173 |
| `superseded-goodput` | `least_outstanding-a4-r3` | +0.684 | +0.173 |
| `superseded-goodput` | `least_outstanding-a6-r1` | +0.274 | +0.083 |
| `superseded-goodput` | `session_affinity-a10-r2` | +0.262 | +0.212 |

At rate 2 the pool is 10 conversations and a visit period is 20 s, so a 65 s measured window holds
3.25 of them: each half draws a different mix of turn indices, and the cheap first turns landing
disproportionately in one of them is the whole of the 48–68% "slowdown". All four turn indices
appear on both sides of the split in these cells, so the composition cancels and nothing is left.

## The two runs the ticket asked about by name

**#17's six recency cells do not stay where they are, and they move the way #29 said they would.**
`docs/measurements/2026-09-12-recency-rerun/` derived by hand that think 30 s was a genuinely cold
opening period and think 75 s was a window that could not hold one visit period. The check now
reaches both conclusions from the rows, with no geometry stated to it:

| cell | periods measured | recorded drift | per-index drift | compared | confined | recorded verdict | current verdict |
|---|---:|---:|---:|---:|---:|---|---|
| `recency/think30` r1 | 1.88 | +0.936 | +1.035 | 4 | 0 | cold opening | cold opening |
| `recency/think30` r2 | 1.88 | +2.311 | +1.505 | 4 | 0 | cold opening | cold opening |
| `recency/think30` r3 | 1.88 | +1.933 | +1.578 | 4 | 0 | cold opening | cold opening |
| `recency/think75` r1 | 1.05 | +3.246 | −0.056 | 2 | 2 | cold opening | fractional visit periods |
| `recency/think75` r2 | 1.05 | +4.248 | −0.228 | 2 | 2 | cold opening | fractional visit periods |
| `recency/think75` r3 | 1.05 | +0.514 | −0.324 | 2 | 2 | cold opening | fractional visit periods + fleet degrading |

The think 75 s cells' 1.05 periods leave two of the four turn indices wholly on one side of the
split, which the check reports as `warmup_drift_turns_confined: 2` and names in the flag. Their remaining
per-index drift is −0.06 to −0.32 against the +0.51 to +4.25 the pooled comparison recorded: the
fleet was not three times slower at the start, the two halves were different conversations.

**#29's six re-run cells mostly stay clear, and two do not.** The re-run's recorded drifts of
−0.571, −0.690 and −0.562 at think 75 s were largely the drain-shifted split, as that run's own
design note predicted. Split on arrivals and compared per index they shrink to −0.145, −0.254 and
−0.055, and every one of the six has all four turn indices on both sides of the split, which is
exactly what its geometry was chosen for.

| cell | periods measured | recorded drift | per-index drift | confined | current verdict |
|---|---:|---:|---:|---:|---|
| `recency-rerun/think30` r1 | 2.00 | −0.079 | −0.233 | 0 | clear |
| `recency-rerun/think30` r2 | 2.00 | −0.115 | −0.235 | 0 | clear |
| `recency-rerun/think30` r3 | 2.00 | −0.145 | −0.280 | 0 | **fleet degrading** |
| `recency-rerun/think75` r1 | 2.00 | −0.571 | −0.145 | 0 | clear |
| `recency-rerun/think75` r2 | 2.00 | −0.690 | −0.254 | 0 | **fleet degrading** |
| `recency-rerun/think75` r3 | 2.00 | −0.562 | −0.055 | 0 | clear |

Two of the six now sit just past the 25% threshold in the direction the old check could not see:
−0.280 and −0.254. Both are prefix affinity at 8 requests per second with a two-visit window, and
both are single repetitions whose siblings are clear, so the recency curve #29 published rests on
four unflagged cells rather than six. That is a narrowing, not a reversal — see the correction
noted in that measurement.

## The repetition triple the ticket cites

`superseded-goodput` held three repetitions of one configuration at drifts of −0.768, −0.471 and
+4.581: one flagged and two not, on nothing but where the split happened to fall.

| cell | recorded drift | per-index drift | recorded verdict | current verdict |
|---|---:|---:|---|---|
| `session_affinity-a16-r1` | −0.768 | −0.870 | clear | fleet degrading |
| `session_affinity-a16-r2` | −0.471 | −0.480 | clear | fleet degrading |
| `session_affinity-a16-r3` | +4.581 | +9.675 | cold opening | cold opening |

All three are now flagged. They still disagree about the direction, which is a real disagreement
between three runs at 16 requests per second — past session affinity's 12–14 knee — and not an
artefact of the check.

## The closed-loop control

300 closed-loop cells whose rows are in this repository were re-scored as a control: 11 were
flagged, 24 are now, 15 change verdict. The one group worth naming is the multi-turn concurrency
sweep at 256 virtual users, where six cells report one to three turn indices confined to one side
of the split. At that concurrency the fleet is slow enough that a virtual user barely completes a
visit inside the measured window, so the cell's percentiles are over first turns and the check says
so. Those cells were already excluded from the published comparison for other reasons.

## What this does not settle

- **The recorded cell records still carry their old summaries.** This re-score deliberately writes
  nothing back: a recorded cell says what was concluded when it ran. Generated tables that filter on
  the recorded flag — `comparison.md` and the figures built from it — therefore still show the old
  verdicts, and the conclusions that change are corrected in prose in the affected measurements
  rather than by regenerating those tables. Re-summarising the recorded cells and rebuilding
  everything downstream of them is its own piece of work.
- **A window shorter than one visit period still pools.** The check is told the workload's turns per
  session and compares within each, but it cannot compare an index that the window never offered
  twice. Such a cell reports every missing index as confined to one half, which is the right
  verdict, but its drift figure rests on whatever indices did appear.
