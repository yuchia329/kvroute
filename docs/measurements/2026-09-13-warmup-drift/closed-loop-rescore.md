Re-scored 300 cells against a 25% drift threshold.

## Closed-loop cells

300 cells: 11 were flagged by the drift check, 23 are now, 17 change verdict.

## Verdict transitions

| recorded verdict | current verdict | cells |
|---|---|---:|
| clear | clear | 275 |
| cold opening | cold opening | 8 |
| clear | cold opening | 5 |
| clear | fleet degrading | 5 |
| clear | partial visits + fleet degrading | 4 |
| cold opening | clear | 2 |
| cold opening | fleet degrading | 1 |

## Cells whose verdict changed

| run | cell | rate | think | periods | recorded drift | current drift | basis | compared | confined | thin | backlog | recorded verdict | current verdict |
|---|---|---:|---:|---:|---:|---:|---|---:|---:|---:|---:|---|---|
| docs/measurements/2026-09-08-policy-comparison/concurrency | least_outstanding-c32-r2 | — | — | — | -0.346 | -0.368 | pooled | 1 | 0 | 0 | 0% | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | least_outstanding-c256-r1 | — | — | — | -0.683 | -0.637 | turn | 2 | 1 | 1 | 0% | clear | partial visits + fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | least_outstanding-c256-r2 | — | — | — | -0.551 | -0.663 | turn | 2 | 2 | 0 | 0% | clear | partial visits + fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | least_outstanding-c256-r3 | — | — | — | -0.628 | -0.703 | turn | 2 | 0 | 2 | 0% | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | round_robin-c128-r2 | — | — | — | +0.533 | -0.016 | turn | 4 | 0 | 0 | 0% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | round_robin-c256-r1 | — | — | — | -0.774 | -0.686 | turn | 2 | 1 | 1 | 0% | clear | partial visits + fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | round_robin-c256-r2 | — | — | — | -0.587 | -0.613 | turn | 3 | 0 | 1 | 0% | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | round_robin-c256-r3 | — | — | — | -0.729 | -0.628 | turn | 2 | 1 | 1 | 0% | clear | partial visits + fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | round_robin-c64-r3 | — | — | — | +0.363 | +0.091 | turn | 4 | 0 | 0 | 0% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | session_affinity-c128-r2 | — | — | — | +0.216 | +0.376 | turn | 4 | 0 | 0 | 0% | clear | cold opening |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | session_affinity-c128-r3 | — | — | — | +0.235 | +0.279 | turn | 4 | 0 | 0 | 0% | clear | cold opening |
| docs/measurements/2026-09-08-three-policy-multiturn/concurrency | session_affinity-c256-r1 | — | — | — | +3.588 | -0.817 | turn | 4 | 0 | 0 | 0% | cold opening | fleet degrading |
| docs/measurements/2026-09-11-pressure-grid/grid/ws0.25-skew1 | least_outstanding-c32-r3 | — | — | — | -0.460 | -0.271 | turn | 4 | 0 | 0 | 0% | clear | fleet degrading |
| docs/measurements/2026-09-11-pressure-grid/grid/ws0.25-skew1 | round_robin-c32-r3 | — | — | — | +0.004 | +0.296 | turn | 4 | 0 | 0 | 0% | clear | cold opening |
| docs/measurements/2026-09-11-pressure-grid/grid/ws1-skew1.4 | round_robin-c32-r2 | — | — | — | +0.197 | +0.316 | turn | 4 | 0 | 0 | 0% | clear | cold opening |
| docs/measurements/2026-09-11-pressure-grid/grid/ws3-skew1 | session_affinity-c32-r2 | — | — | — | +0.077 | +0.322 | turn | 4 | 0 | 0 | 0% | clear | cold opening |
| docs/measurements/2026-09-11-pressure-grid/spilloff/ws3-skew1 | prefix_affinity-c32-r2 | — | — | — | -0.513 | -0.351 | turn | 4 | 0 | 0 | 0% | clear | fleet degrading |

17 of 300 cells changed verdict.

## Re-score integrity

Every one of the 300 cells reproduced its recorded requests, successes, window, goodput and TTFT percentiles from its own rows. Only the drift fields moved.

