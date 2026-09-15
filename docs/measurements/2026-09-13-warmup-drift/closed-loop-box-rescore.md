Re-scored 228 cells against a 25% drift threshold.

## Closed-loop cells

228 cells: 13 were flagged by the drift check, 25 are now, 12 change verdict.

## Verdict transitions

| recorded verdict | current verdict | cells |
|---|---|---:|
| clear | clear | 203 |
| cold opening | cold opening | 13 |
| clear | fleet degrading | 7 |
| clear | cold opening | 5 |

## Cells whose verdict changed

| run | cell | rate | think | periods | recorded drift | current drift | basis | compared | confined | thin | backlog | recorded verdict | current verdict |
|---|---|---:|---:|---:|---:|---:|---|---:|---:|---:|---:|---|---|
| docs/measurements/2026-09-11-pressure-grid/residency/m0.70/ws0.25-skew0 | prefix_affinity-c32-r3 | — | — | — | -0.393 | -0.286 | turn | 4 | 0 | 0 | 0% | clear | fleet degrading |
| docs/measurements/2026-09-11-pressure-grid/residency/m0.55/ws1-skew0 | prefix_affinity-c32-r1 | — | — | — | +0.128 | +0.323 | turn | 4 | 0 | 0 | 0% | clear | cold opening |
| docs/measurements/2026-09-11-pressure-grid/residency/m0.55/ws1-skew0 | prefix_affinity-c32-r2 | — | — | — | -0.395 | -0.334 | turn | 4 | 0 | 0 | 0% | clear | fleet degrading |
| docs/measurements/2026-09-12-exact-residency/grid/ws1-skew1 | exact_residency-c32-r1 | — | — | — | -0.472 | -0.452 | turn | 4 | 0 | 0 | 0% | clear | fleet degrading |
| docs/measurements/2026-09-12-exact-residency/grid/ws1-skew1 | exact_residency-c32-r2 | — | — | — | -0.434 | -0.336 | turn | 4 | 0 | 0 | 0% | clear | fleet degrading |
| docs/measurements/2026-09-12-exact-residency/grid/ws1-skew1 | exact_residency-c32-r3 | — | — | — | -0.216 | -0.265 | turn | 4 | 0 | 0 | 0% | clear | fleet degrading |
| docs/measurements/2026-09-11-pressure-grid/residency/m0.70/ws1-skew1 | prefix_affinity-c32-r2 | — | — | — | +0.238 | +0.269 | turn | 4 | 0 | 0 | 0% | clear | cold opening |
| docs/measurements/2026-09-12-exact-residency/grid/ws1-skew1.4 | session_affinity-c32-r1 | — | — | — | +0.234 | +0.266 | turn | 4 | 0 | 0 | 0% | clear | cold opening |
| docs/measurements/2026-09-11-pressure-grid/residency/m0.62/ws3-skew1 | prefix_affinity-c32-r3 | — | — | — | +0.216 | +0.271 | turn | 4 | 0 | 0 | 0% | clear | cold opening |
| docs/measurements/2026-09-11-pressure-grid/residency/m0.70/ws3-skew1 | prefix_affinity-c32-r3 | — | — | — | +0.128 | +0.272 | turn | 4 | 0 | 0 | 0% | clear | cold opening |
| docs/measurements/2026-09-11-pressure-grid/residency/m0.62/ws8-skew1 | prefix_affinity-c32-r2 | — | — | — | -0.305 | -0.311 | turn | 4 | 0 | 0 | 0% | clear | fleet degrading |
| docs/measurements/2026-09-11-pressure-grid/residency/m0.62/ws8-skew1 | prefix_affinity-c32-r3 | — | — | — | -0.363 | -0.372 | turn | 4 | 0 | 0 | 0% | clear | fleet degrading |

12 of 228 cells changed verdict.

## Re-score integrity

Every one of the 228 cells reproduced its recorded requests, successes, window, goodput and TTFT percentiles from its own rows. Only the drift fields moved.

