Re-scored 216 cells against a 25% drift threshold.

## Open-loop cells

216 cells: 26 were flagged by the drift check, 111 are now, 118 change verdict.

## Verdict transitions

| recorded verdict | current verdict | cells |
|---|---|---:|
| clear | fleet degrading | 99 |
| clear | clear | 90 |
| cold opening | clear | 15 |
| cold opening | cold opening | 8 |
| cold opening | fractional visit periods | 2 |
| clear | cold opening | 1 |
| cold opening | fractional visit periods + fleet degrading | 1 |

## Cells whose verdict changed

| run | cell | rate | think | periods | recorded drift | current drift | basis | compared | confined | recorded verdict | current verdict |
|---|---|---:|---:|---:|---:|---:|---|---:|---:|---|---|
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a16-r1 | 16/s | — | — | -0.578 | -0.570 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a16-r2 | 16/s | — | — | -0.578 | -0.583 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a16-r3 | 16/s | — | — | -0.547 | -0.569 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a24-r1 | 24/s | — | — | +0.000 | -0.486 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a24-r2 | 24/s | — | — | +0.000 | -0.497 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a24-r3 | 24/s | — | — | -0.364 | -0.489 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a32-r1 | 32/s | — | — | +0.000 | -0.468 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a32-r2 | 32/s | — | — | +0.000 | -0.472 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a32-r3 | 32/s | — | — | +0.000 | -0.472 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a48-r1 | 48/s | — | — | +0.000 | -0.458 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a48-r2 | 48/s | — | — | +0.000 | -0.462 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a48-r3 | 48/s | — | — | +0.000 | -0.463 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a16-r1 | 16/s | — | — | -0.594 | -0.638 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a16-r2 | 16/s | — | — | -0.578 | -0.527 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a16-r3 | 16/s | — | — | -0.703 | -0.645 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a24-r1 | 24/s | — | — | +0.000 | -0.496 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a24-r2 | 24/s | — | — | +0.000 | -0.505 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a24-r3 | 24/s | — | — | +0.000 | -0.502 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a32-r1 | 32/s | — | — | +0.000 | -0.476 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a32-r2 | 32/s | — | — | +0.000 | -0.480 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a32-r3 | 32/s | — | — | +0.000 | -0.480 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a48-r1 | 48/s | — | — | +0.000 | -0.462 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a48-r2 | 48/s | — | — | +0.000 | -0.463 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a48-r3 | 48/s | — | — | +0.000 | -0.464 | pooled | 1 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a10-r1 | 10/s | — | — | -0.404 | -0.485 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a10-r2 | 10/s | — | — | -0.470 | -0.519 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a10-r3 | 10/s | — | — | -0.401 | -0.537 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a12-r1 | 12/s | — | — | -0.426 | -0.524 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a12-r2 | 12/s | — | — | -0.395 | -0.459 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a12-r3 | 12/s | — | — | -0.315 | -0.514 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a14-r1 | 14/s | — | — | +0.000 | -0.518 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a14-r2 | 14/s | — | — | +0.000 | -0.534 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a14-r3 | 14/s | — | — | +0.000 | -0.483 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a16-r1 | 16/s | — | — | +0.000 | -0.502 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a16-r2 | 16/s | — | — | +0.000 | -0.531 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a16-r3 | 16/s | — | — | +0.000 | -0.513 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a2-r1 | 2/s | — | — | +0.542 | +0.107 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a2-r3 | 2/s | — | — | +0.483 | -0.035 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a20-r1 | 20/s | — | — | +0.000 | -0.519 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a20-r2 | 20/s | — | — | +0.000 | -0.507 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a20-r3 | 20/s | — | — | +0.000 | -0.526 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a4-r1 | 4/s | — | — | +0.491 | -0.136 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a4-r2 | 4/s | — | — | +0.432 | +0.035 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a8-r1 | 8/s | — | — | -0.419 | -0.537 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a8-r2 | 8/s | — | — | -0.467 | -0.474 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a8-r3 | 8/s | — | — | -0.487 | -0.540 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a10-r1 | 10/s | — | — | -0.483 | -0.550 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a10-r2 | 10/s | — | — | -0.429 | -0.533 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a10-r3 | 10/s | — | — | -0.461 | -0.534 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a12-r1 | 12/s | — | — | +0.000 | -0.532 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a12-r2 | 12/s | — | — | +0.000 | -0.536 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a12-r3 | 12/s | — | — | -0.372 | -0.513 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a14-r1 | 14/s | — | — | +0.000 | -0.522 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a14-r2 | 14/s | — | — | +0.000 | -0.512 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a14-r3 | 14/s | — | — | +0.000 | -0.517 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a16-r1 | 16/s | — | — | +0.000 | -0.522 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a16-r2 | 16/s | — | — | +0.000 | -0.508 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a16-r3 | 16/s | — | — | +0.000 | -0.518 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a2-r1 | 2/s | — | — | +0.681 | -0.004 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a2-r2 | 2/s | — | — | +0.636 | +0.216 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a2-r3 | 2/s | — | — | +0.536 | -0.022 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a20-r1 | 20/s | — | — | +0.000 | -0.514 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a20-r2 | 20/s | — | — | +0.000 | -0.541 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a20-r3 | 20/s | — | — | +0.000 | -0.519 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a4-r1 | 4/s | — | — | +0.598 | +0.092 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a4-r2 | 4/s | — | — | +0.457 | -0.050 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a4-r3 | 4/s | — | — | +0.536 | -0.076 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a8-r1 | 8/s | — | — | -0.500 | -0.507 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a8-r2 | 8/s | — | — | -0.488 | -0.532 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a8-r3 | 8/s | — | — | -0.492 | -0.550 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a10-r3 | 10/s | — | — | +0.231 | +0.490 | turn | 4 | 0 | clear | cold opening |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a16-r1 | 16/s | — | — | -0.734 | -0.791 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a16-r2 | 16/s | — | — | -0.216 | -0.427 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a20-r1 | 20/s | — | — | -0.370 | -0.622 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a20-r2 | 20/s | — | — | -0.506 | -0.517 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a20-r3 | 20/s | — | — | -0.360 | -0.556 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a10-r1 | 10/s | — | — | -0.494 | -0.520 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a10-r2 | 10/s | — | — | -0.460 | -0.561 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a10-r3 | 10/s | — | — | -0.438 | -0.565 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a12-r1 | 12/s | — | — | -0.446 | -0.582 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a12-r2 | 12/s | — | — | -0.359 | -0.544 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a12-r3 | 12/s | — | — | -0.430 | -0.578 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a14-r1 | 14/s | — | — | +0.000 | -0.527 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a14-r2 | 14/s | — | — | +0.000 | -0.538 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a14-r3 | 14/s | — | — | +0.000 | -0.528 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a16-r1 | 16/s | — | — | +0.000 | -0.528 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a16-r2 | 16/s | — | — | +0.000 | -0.536 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a16-r3 | 16/s | — | — | +0.000 | -0.490 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a20-r1 | 20/s | — | — | +0.000 | -0.527 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a20-r2 | 20/s | — | — | +0.000 | -0.535 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a20-r3 | 20/s | — | — | +0.000 | -0.514 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a4-r1 | 4/s | — | — | +0.505 | -0.133 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a4-r2 | 4/s | — | — | +0.503 | -0.173 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a4-r3 | 4/s | — | — | +0.684 | +0.173 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a6-r1 | 6/s | — | — | +0.274 | +0.083 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a8-r1 | 8/s | — | — | -0.477 | -0.406 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a8-r2 | 8/s | — | — | -0.565 | -0.561 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a8-r3 | 8/s | — | — | -0.558 | -0.569 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a14-r1 | 14/s | — | — | -0.535 | -0.588 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a14-r2 | 14/s | — | — | -0.519 | -0.566 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a14-r3 | 14/s | — | — | -0.612 | -0.623 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a16-r1 | 16/s | — | — | -0.492 | -0.520 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a16-r2 | 16/s | — | — | -0.503 | -0.567 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a16-r3 | 16/s | — | — | -0.491 | -0.544 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a20-r1 | 20/s | — | — | +0.000 | -0.525 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a20-r2 | 20/s | — | — | +0.000 | -0.521 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a20-r3 | 20/s | — | — | +0.000 | -0.518 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a10-r2 | 10/s | — | — | +0.262 | +0.212 | turn | 4 | 0 | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a16-r1 | 16/s | — | — | -0.768 | -0.870 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a16-r2 | 16/s | — | — | -0.471 | -0.480 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a20-r1 | 20/s | — | — | -0.448 | -0.636 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a20-r2 | 20/s | — | — | -0.475 | -0.496 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a20-r3 | 20/s | — | — | -0.421 | -0.641 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-10-belief-divergence/recency/think75 | prefix_affinity-a8-r1 | 8/s | 1m15s | 1.05 | +3.246 | -0.056 | turn | 2 | 2 | cold opening | fractional visit periods |
| docs/measurements/2026-09-10-belief-divergence/recency/think75 | prefix_affinity-a8-r2 | 8/s | 1m15s | 1.05 | +4.248 | -0.228 | turn | 2 | 2 | cold opening | fractional visit periods |
| docs/measurements/2026-09-10-belief-divergence/recency/think75 | prefix_affinity-a8-r3 | 8/s | 1m15s | 1.05 | +0.514 | -0.324 | turn | 2 | 2 | cold opening | fractional visit periods + fleet degrading |
| docs/measurements/2026-09-12-recency-rerun/think30 | prefix_affinity-a8-r3 | 8/s | 30s | 2.00 | -0.145 | -0.280 | turn | 4 | 0 | clear | fleet degrading |
| docs/measurements/2026-09-12-recency-rerun/think75 | prefix_affinity-a8-r2 | 8/s | 1m15s | 2.00 | -0.690 | -0.254 | turn | 4 | 0 | clear | fleet degrading |

118 of 216 cells changed verdict.

## Re-score integrity

Every one of the 216 cells reproduced its recorded requests, successes, window, goodput and TTFT percentiles from its own rows. Only the drift fields moved.

