Re-scored 300 cells against a 25% drift threshold.

## Closed-loop cells

300 cells: 11 were flagged by the drift check, 24 are now, 15 change verdict.

## Verdict transitions

| recorded verdict | current verdict | cells |
|---|---|---:|
| clear | clear | 275 |
| cold opening | cold opening | 10 |
| clear | cold opening | 5 |
| clear | fleet degrading | 3 |
| clear | fractional visit periods | 3 |
| clear | fractional visit periods + fleet degrading | 3 |
| cold opening | clear | 1 |

## Cells whose verdict changed

| run | cell | rate | think | periods | recorded drift | current drift | basis | compared | confined | recorded verdict | current verdict |
|---|---|---:|---:|---:|---:|---:|---|---:|---:|---|---|
| docs/measurements/2026-09-08-policy-comparison/concurrency | least_outstanding-c32-r2 | — | — | — | -0.346 | -0.346 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | least_outstanding-c256-r1 | — | — | — | -0.683 | +0.160 | turn | 1 | 3 | clear | fractional visit periods |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | least_outstanding-c256-r2 | — | — | — | -0.551 | +0.058 | turn | 2 | 2 | clear | fractional visit periods |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | least_outstanding-c256-r3 | — | — | — | -0.628 | +0.035 | turn | 2 | 2 | clear | fractional visit periods |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | round_robin-c256-r1 | — | — | — | -0.774 | -0.534 | turn | 2 | 2 | clear | fractional visit periods + fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | round_robin-c256-r2 | — | — | — | -0.587 | -0.501 | turn | 3 | 1 | clear | fractional visit periods + fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | round_robin-c256-r3 | — | — | — | -0.729 | -0.611 | turn | 2 | 2 | clear | fractional visit periods + fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | round_robin-c64-r3 | — | — | — | +0.363 | +0.038 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | session_affinity-c128-r2 | — | — | — | +0.216 | +0.359 | turn | 4 | 0 | clear | cold opening |
| docs/measurements/2026-09-11-pressure-grid/grid/ws0.25-skew1 | least_outstanding-c32-r3 | — | — | — | -0.460 | -0.264 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-11-pressure-grid/grid/ws0.25-skew1 | round_robin-c32-r3 | — | — | — | +0.004 | +0.311 | turn | 4 | 0 | clear | cold opening |
| docs/measurements/2026-09-11-pressure-grid/grid/ws1-skew1.4 | round_robin-c32-r2 | — | — | — | +0.197 | +0.301 | turn | 4 | 0 | clear | cold opening |
| docs/measurements/2026-09-11-pressure-grid/grid/ws1-skew1.4 | session_affinity-c32-r3 | — | — | — | +0.243 | +0.258 | turn | 4 | 0 | clear | cold opening |
| docs/measurements/2026-09-11-pressure-grid/grid/ws3-skew1 | session_affinity-c32-r2 | — | — | — | +0.077 | +0.362 | turn | 4 | 0 | clear | cold opening |
| docs/measurements/2026-09-11-pressure-grid/spilloff/ws3-skew1 | prefix_affinity-c32-r2 | — | — | — | -0.513 | -0.357 | turn | 4 | 0 | clear | fleet degrading |

15 of 300 cells changed verdict.

## Re-score integrity

Every one of the 300 cells reproduced its recorded requests, successes, window, goodput and TTFT percentiles from its own rows. Only the drift fields moved.

