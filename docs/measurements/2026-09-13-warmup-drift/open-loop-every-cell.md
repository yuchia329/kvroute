Re-scored 216 cells against a 25% drift threshold.

## Open-loop cells

216 cells: 26 were flagged by the drift check, 111 are now, 120 change verdict.

## Verdict transitions

| recorded verdict | current verdict | cells |
|---|---|---:|
| clear | past saturation | 97 |
| clear | clear | 90 |
| cold opening | clear | 15 |
| cold opening | cold opening | 6 |
| clear | fleet degrading | 2 |
| cold opening | fractional visit periods | 2 |
| cold opening | past saturation | 2 |
| clear | cold opening | 1 |
| cold opening | fractional visit periods + fleet degrading | 1 |

## Every cell

| run | cell | rate | think | periods | recorded drift | current drift | basis | compared | confined | backlog | recorded verdict | current verdict |
|---|---|---:|---:|---:|---:|---:|---|---:|---:|---:|---|---|
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a12-r1 | 12/s | — | — | -0.033 | -0.042 | pooled | 1 | 0 | 3% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a12-r2 | 12/s | — | — | +0.211 | +0.211 | pooled | 1 | 0 | 3% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a12-r3 | 12/s | — | — | +0.233 | +0.230 | pooled | 1 | 0 | 3% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a16-r1 | 16/s | — | — | -0.578 | -0.570 | pooled | 1 | 0 | 58% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a16-r2 | 16/s | — | — | -0.578 | -0.583 | pooled | 1 | 0 | 53% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a16-r3 | 16/s | — | — | -0.547 | -0.569 | pooled | 1 | 0 | 51% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a24-r1 | 24/s | — | — | +0.000 | -0.486 | pooled | 1 | 0 | 86% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a24-r2 | 24/s | — | — | +0.000 | -0.497 | pooled | 1 | 0 | 85% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a24-r3 | 24/s | — | — | -0.364 | -0.489 | pooled | 1 | 0 | 84% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a32-r1 | 32/s | — | — | +0.000 | -0.468 | pooled | 1 | 0 | 97% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a32-r2 | 32/s | — | — | +0.000 | -0.472 | pooled | 1 | 0 | 97% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a32-r3 | 32/s | — | — | +0.000 | -0.472 | pooled | 1 | 0 | 96% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a4-r1 | 4/s | — | — | -0.004 | -0.002 | pooled | 1 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a4-r2 | 4/s | — | — | -0.004 | -0.003 | pooled | 1 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a4-r3 | 4/s | — | — | -0.002 | -0.002 | pooled | 1 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a48-r1 | 48/s | — | — | +0.000 | -0.458 | pooled | 1 | 0 | 100% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a48-r2 | 48/s | — | — | +0.000 | -0.462 | pooled | 1 | 0 | 100% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a48-r3 | 48/s | — | — | +0.000 | -0.463 | pooled | 1 | 0 | 100% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a8-r1 | 8/s | — | — | -0.006 | -0.007 | pooled | 1 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a8-r2 | 8/s | — | — | -0.007 | -0.009 | pooled | 1 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | least_outstanding-a8-r3 | 8/s | — | — | -0.006 | -0.005 | pooled | 1 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a12-r1 | 12/s | — | — | -0.006 | -0.007 | pooled | 1 | 0 | 3% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a12-r2 | 12/s | — | — | -0.007 | -0.007 | pooled | 1 | 0 | 4% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a12-r3 | 12/s | — | — | -0.001 | +0.001 | pooled | 1 | 0 | 4% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a16-r1 | 16/s | — | — | -0.594 | -0.638 | pooled | 1 | 0 | 57% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a16-r2 | 16/s | — | — | -0.578 | -0.527 | pooled | 1 | 0 | 40% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a16-r3 | 16/s | — | — | -0.703 | -0.645 | pooled | 1 | 0 | 43% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a24-r1 | 24/s | — | — | +0.000 | -0.496 | pooled | 1 | 0 | 84% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a24-r2 | 24/s | — | — | +0.000 | -0.505 | pooled | 1 | 0 | 83% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a24-r3 | 24/s | — | — | +0.000 | -0.502 | pooled | 1 | 0 | 83% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a32-r1 | 32/s | — | — | +0.000 | -0.476 | pooled | 1 | 0 | 96% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a32-r2 | 32/s | — | — | +0.000 | -0.480 | pooled | 1 | 0 | 96% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a32-r3 | 32/s | — | — | +0.000 | -0.480 | pooled | 1 | 0 | 95% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a4-r1 | 4/s | — | — | -0.006 | -0.007 | pooled | 1 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a4-r2 | 4/s | — | — | -0.003 | -0.002 | pooled | 1 | 0 | 0% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a4-r3 | 4/s | — | — | -0.000 | -0.000 | pooled | 1 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a48-r1 | 48/s | — | — | +0.000 | -0.462 | pooled | 1 | 0 | 100% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a48-r2 | 48/s | — | — | +0.000 | -0.463 | pooled | 1 | 0 | 100% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a48-r3 | 48/s | — | — | +0.000 | -0.464 | pooled | 1 | 0 | 100% | clear | past saturation |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a8-r1 | 8/s | — | — | -0.010 | -0.010 | pooled | 1 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a8-r2 | 8/s | — | — | -0.014 | -0.014 | pooled | 1 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-policy-comparison/goodput | round_robin-a8-r3 | 8/s | — | — | -0.008 | -0.008 | pooled | 1 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a10-r1 | 10/s | — | — | -0.404 | -0.485 | turn | 4 | 0 | 75% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a10-r2 | 10/s | — | — | -0.470 | -0.519 | turn | 4 | 0 | 70% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a10-r3 | 10/s | — | — | -0.401 | -0.537 | turn | 4 | 0 | 75% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a12-r1 | 12/s | — | — | -0.426 | -0.524 | turn | 4 | 0 | 79% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a12-r2 | 12/s | — | — | -0.395 | -0.459 | turn | 4 | 0 | 80% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a12-r3 | 12/s | — | — | -0.315 | -0.514 | turn | 4 | 0 | 80% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a14-r1 | 14/s | — | — | +0.000 | -0.518 | turn | 4 | 0 | 86% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a14-r2 | 14/s | — | — | +0.000 | -0.534 | turn | 4 | 0 | 83% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a14-r3 | 14/s | — | — | +0.000 | -0.483 | turn | 4 | 0 | 87% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a16-r1 | 16/s | — | — | +0.000 | -0.502 | turn | 4 | 0 | 93% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a16-r2 | 16/s | — | — | +0.000 | -0.531 | turn | 4 | 0 | 91% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a16-r3 | 16/s | — | — | +0.000 | -0.513 | turn | 4 | 0 | 91% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a2-r1 | 2/s | — | — | +0.542 | +0.107 | turn | 4 | 0 | 2% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a2-r2 | 2/s | — | — | +0.023 | -0.029 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a2-r3 | 2/s | — | — | +0.483 | -0.035 | turn | 4 | 0 | 1% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a20-r1 | 20/s | — | — | +0.000 | -0.519 | turn | 4 | 0 | 99% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a20-r2 | 20/s | — | — | +0.000 | -0.507 | turn | 4 | 0 | 98% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a20-r3 | 20/s | — | — | +0.000 | -0.526 | turn | 4 | 0 | 98% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a4-r1 | 4/s | — | — | +0.491 | -0.136 | turn | 4 | 0 | 2% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a4-r2 | 4/s | — | — | +0.432 | +0.035 | turn | 4 | 0 | 2% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a4-r3 | 4/s | — | — | +0.027 | -0.043 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a6-r1 | 6/s | — | — | +0.243 | -0.075 | turn | 4 | 0 | 3% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a6-r2 | 6/s | — | — | +0.414 | +0.334 | turn | 4 | 0 | 2% | cold opening | cold opening |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a6-r3 | 6/s | — | — | +0.031 | -0.077 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a8-r1 | 8/s | — | — | -0.419 | -0.537 | turn | 4 | 0 | 56% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a8-r2 | 8/s | — | — | -0.467 | -0.474 | turn | 4 | 0 | 55% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | least_outstanding-a8-r3 | 8/s | — | — | -0.487 | -0.540 | turn | 4 | 0 | 57% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a10-r1 | 10/s | — | — | -0.483 | -0.550 | turn | 4 | 0 | 73% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a10-r2 | 10/s | — | — | -0.429 | -0.533 | turn | 4 | 0 | 76% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a10-r3 | 10/s | — | — | -0.461 | -0.534 | turn | 4 | 0 | 73% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a12-r1 | 12/s | — | — | +0.000 | -0.532 | turn | 4 | 0 | 81% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a12-r2 | 12/s | — | — | +0.000 | -0.536 | turn | 4 | 0 | 80% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a12-r3 | 12/s | — | — | -0.372 | -0.513 | turn | 4 | 0 | 80% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a14-r1 | 14/s | — | — | +0.000 | -0.522 | turn | 4 | 0 | 86% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a14-r2 | 14/s | — | — | +0.000 | -0.512 | turn | 4 | 0 | 85% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a14-r3 | 14/s | — | — | +0.000 | -0.517 | turn | 4 | 0 | 86% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a16-r1 | 16/s | — | — | +0.000 | -0.522 | turn | 4 | 0 | 91% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a16-r2 | 16/s | — | — | +0.000 | -0.508 | turn | 4 | 0 | 91% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a16-r3 | 16/s | — | — | +0.000 | -0.518 | turn | 4 | 0 | 93% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a2-r1 | 2/s | — | — | +0.681 | -0.004 | turn | 4 | 0 | 1% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a2-r2 | 2/s | — | — | +0.636 | +0.216 | turn | 4 | 0 | 1% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a2-r3 | 2/s | — | — | +0.536 | -0.022 | turn | 4 | 0 | 1% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a20-r1 | 20/s | — | — | +0.000 | -0.514 | turn | 4 | 0 | 99% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a20-r2 | 20/s | — | — | +0.000 | -0.541 | turn | 4 | 0 | 97% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a20-r3 | 20/s | — | — | +0.000 | -0.519 | turn | 4 | 0 | 98% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a4-r1 | 4/s | — | — | +0.598 | +0.092 | turn | 4 | 0 | 2% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a4-r2 | 4/s | — | — | +0.457 | -0.050 | turn | 4 | 0 | 1% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a4-r3 | 4/s | — | — | +0.536 | -0.076 | turn | 4 | 0 | 1% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a6-r1 | 6/s | — | — | +0.144 | +0.132 | turn | 4 | 0 | 3% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a6-r2 | 6/s | — | — | +0.049 | +0.011 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a6-r3 | 6/s | — | — | +0.021 | -0.071 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a8-r1 | 8/s | — | — | -0.500 | -0.507 | turn | 4 | 0 | 58% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a8-r2 | 8/s | — | — | -0.488 | -0.532 | turn | 4 | 0 | 54% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | round_robin-a8-r3 | 8/s | — | — | -0.492 | -0.550 | turn | 4 | 0 | 56% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a10-r1 | 10/s | — | — | +0.196 | +0.045 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a10-r2 | 10/s | — | — | +0.219 | +0.040 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a10-r3 | 10/s | — | — | +0.231 | +0.490 | turn | 4 | 0 | 13% | clear | cold opening |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a12-r1 | 12/s | — | — | +0.143 | +0.096 | turn | 4 | 0 | 3% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a12-r2 | 12/s | — | — | +0.059 | +0.032 | turn | 4 | 0 | 9% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a12-r3 | 12/s | — | — | +0.025 | -0.108 | turn | 4 | 0 | 9% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a14-r1 | 14/s | — | — | +0.125 | +0.147 | turn | 4 | 0 | 13% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a14-r2 | 14/s | — | — | +0.326 | +1.370 | turn | 4 | 0 | 16% | cold opening | cold opening |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a14-r3 | 14/s | — | — | -0.049 | -0.017 | turn | 4 | 0 | 14% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a16-r1 | 16/s | — | — | -0.734 | -0.791 | turn | 4 | 0 | 38% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a16-r2 | 16/s | — | — | -0.216 | -0.427 | turn | 4 | 0 | 32% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a16-r3 | 16/s | — | — | +6.192 | +10.090 | turn | 4 | 0 | 28% | cold opening | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a2-r1 | 2/s | — | — | +0.041 | -0.010 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a2-r2 | 2/s | — | — | +0.023 | -0.007 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a2-r3 | 2/s | — | — | +0.057 | +0.002 | turn | 4 | 0 | 0% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a20-r1 | 20/s | — | — | -0.370 | -0.622 | turn | 4 | 0 | 49% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a20-r2 | 20/s | — | — | -0.506 | -0.517 | turn | 4 | 0 | 43% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a20-r3 | 20/s | — | — | -0.360 | -0.556 | turn | 4 | 0 | 48% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a4-r1 | 4/s | — | — | +0.082 | +0.003 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a4-r2 | 4/s | — | — | +0.047 | +0.002 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a4-r3 | 4/s | — | — | +0.072 | +0.000 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a6-r1 | 6/s | — | — | +0.037 | +0.002 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a6-r2 | 6/s | — | — | +0.065 | -0.004 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a6-r3 | 6/s | — | — | +0.042 | +0.010 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a8-r1 | 8/s | — | — | +0.110 | +0.003 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a8-r2 | 8/s | — | — | +0.158 | +0.022 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/goodput | session_affinity-a8-r3 | 8/s | — | — | +0.184 | +0.012 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a10-r1 | 10/s | — | — | -0.494 | -0.520 | turn | 4 | 0 | 69% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a10-r2 | 10/s | — | — | -0.460 | -0.561 | turn | 4 | 0 | 72% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a10-r3 | 10/s | — | — | -0.438 | -0.565 | turn | 4 | 0 | 72% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a12-r1 | 12/s | — | — | -0.446 | -0.582 | turn | 4 | 0 | 74% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a12-r2 | 12/s | — | — | -0.359 | -0.544 | turn | 4 | 0 | 77% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a12-r3 | 12/s | — | — | -0.430 | -0.578 | turn | 4 | 0 | 78% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a14-r1 | 14/s | — | — | +0.000 | -0.527 | turn | 4 | 0 | 84% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a14-r2 | 14/s | — | — | +0.000 | -0.538 | turn | 4 | 0 | 83% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a14-r3 | 14/s | — | — | +0.000 | -0.528 | turn | 4 | 0 | 84% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a16-r1 | 16/s | — | — | +0.000 | -0.528 | turn | 4 | 0 | 90% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a16-r2 | 16/s | — | — | +0.000 | -0.536 | turn | 4 | 0 | 88% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a16-r3 | 16/s | — | — | +0.000 | -0.490 | turn | 4 | 0 | 88% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a2-r1 | 2/s | — | — | +0.042 | +0.049 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a2-r2 | 2/s | — | — | +0.030 | +0.091 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a2-r3 | 2/s | — | — | +0.033 | +0.122 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a20-r1 | 20/s | — | — | +0.000 | -0.527 | turn | 4 | 0 | 98% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a20-r2 | 20/s | — | — | +0.000 | -0.535 | turn | 4 | 0 | 98% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a20-r3 | 20/s | — | — | +0.000 | -0.514 | turn | 4 | 0 | 97% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a4-r1 | 4/s | — | — | +0.505 | -0.133 | turn | 4 | 0 | 2% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a4-r2 | 4/s | — | — | +0.503 | -0.173 | turn | 4 | 0 | 2% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a4-r3 | 4/s | — | — | +0.684 | +0.173 | turn | 4 | 0 | 2% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a6-r1 | 6/s | — | — | +0.274 | +0.083 | turn | 4 | 0 | 3% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a6-r2 | 6/s | — | — | +0.206 | -0.060 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a6-r3 | 6/s | — | — | +0.070 | -0.012 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a8-r1 | 8/s | — | — | -0.477 | -0.406 | turn | 4 | 0 | 55% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a8-r2 | 8/s | — | — | -0.565 | -0.561 | turn | 4 | 0 | 54% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | least_outstanding-a8-r3 | 8/s | — | — | -0.558 | -0.569 | turn | 4 | 0 | 49% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a10-r1 | 10/s | — | — | +0.056 | -0.003 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a10-r2 | 10/s | — | — | +0.058 | -0.004 | turn | 4 | 0 | 3% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a10-r3 | 10/s | — | — | +0.054 | -0.009 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a12-r1 | 12/s | — | — | -0.044 | -0.071 | turn | 4 | 0 | 23% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a12-r2 | 12/s | — | — | +0.088 | +0.018 | turn | 4 | 0 | 18% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a12-r3 | 12/s | — | — | -0.023 | -0.076 | turn | 4 | 0 | 17% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a14-r1 | 14/s | — | — | -0.535 | -0.588 | turn | 4 | 0 | 58% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a14-r2 | 14/s | — | — | -0.519 | -0.566 | turn | 4 | 0 | 56% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a14-r3 | 14/s | — | — | -0.612 | -0.623 | turn | 4 | 0 | 56% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a16-r1 | 16/s | — | — | -0.492 | -0.520 | turn | 4 | 0 | 57% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a16-r2 | 16/s | — | — | -0.503 | -0.567 | turn | 4 | 0 | 56% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a16-r3 | 16/s | — | — | -0.491 | -0.544 | turn | 4 | 0 | 56% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a2-r1 | 2/s | — | — | +0.007 | -0.005 | turn | 4 | 0 | 0% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a2-r2 | 2/s | — | — | +0.020 | -0.002 | turn | 4 | 0 | 0% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a2-r3 | 2/s | — | — | +0.014 | -0.004 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a20-r1 | 20/s | — | — | +0.000 | -0.525 | turn | 4 | 0 | 68% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a20-r2 | 20/s | — | — | +0.000 | -0.521 | turn | 4 | 0 | 69% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a20-r3 | 20/s | — | — | +0.000 | -0.518 | turn | 4 | 0 | 67% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a4-r1 | 4/s | — | — | +0.015 | -0.014 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a4-r2 | 4/s | — | — | +0.025 | -0.003 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a4-r3 | 4/s | — | — | +0.024 | -0.006 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a6-r1 | 6/s | — | — | +0.052 | -0.003 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a6-r2 | 6/s | — | — | +0.065 | +0.004 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a6-r3 | 6/s | — | — | +0.051 | -0.012 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a8-r1 | 8/s | — | — | +0.074 | -0.001 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a8-r2 | 8/s | — | — | +0.068 | +0.003 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | round_robin-a8-r3 | 8/s | — | — | +0.057 | +0.001 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a10-r1 | 10/s | — | — | +0.222 | +0.093 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a10-r2 | 10/s | — | — | +0.262 | +0.212 | turn | 4 | 0 | 2% | cold opening | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a10-r3 | 10/s | — | — | -0.089 | +0.141 | turn | 4 | 0 | 15% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a12-r1 | 12/s | — | — | +0.166 | +0.069 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a12-r2 | 12/s | — | — | +0.035 | -0.112 | turn | 4 | 0 | 10% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a12-r3 | 12/s | — | — | -0.009 | -0.097 | turn | 4 | 0 | 11% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a14-r1 | 14/s | — | — | -0.023 | -0.085 | turn | 4 | 0 | 11% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a14-r2 | 14/s | — | — | +1.348 | +1.692 | turn | 4 | 0 | 15% | cold opening | cold opening |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a14-r3 | 14/s | — | — | +0.006 | -0.070 | turn | 4 | 0 | 13% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a16-r1 | 16/s | — | — | -0.768 | -0.870 | turn | 4 | 0 | 37% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a16-r2 | 16/s | — | — | -0.471 | -0.480 | turn | 4 | 0 | 33% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a16-r3 | 16/s | — | — | +4.581 | +9.675 | turn | 4 | 0 | 30% | cold opening | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a2-r1 | 2/s | — | — | +0.022 | -0.009 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a2-r2 | 2/s | — | — | +0.026 | +0.002 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a2-r3 | 2/s | — | — | +0.032 | -0.006 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a20-r1 | 20/s | — | — | -0.448 | -0.636 | turn | 4 | 0 | 48% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a20-r2 | 20/s | — | — | -0.475 | -0.496 | turn | 4 | 0 | 43% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a20-r3 | 20/s | — | — | -0.421 | -0.641 | turn | 4 | 0 | 48% | clear | past saturation |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a4-r1 | 4/s | — | — | +0.045 | -0.012 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a4-r2 | 4/s | — | — | +0.063 | +0.003 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a4-r3 | 4/s | — | — | +0.063 | +0.009 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a6-r1 | 6/s | — | — | +0.080 | +0.005 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a6-r2 | 6/s | — | — | +0.057 | +0.001 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a6-r3 | 6/s | — | — | +0.071 | -0.002 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a8-r1 | 8/s | — | — | +0.040 | -0.047 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a8-r2 | 8/s | — | — | +0.108 | +0.035 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-08-three-policy-multiturn/superseded-goodput | session_affinity-a8-r3 | 8/s | — | — | +0.188 | +0.013 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-10-belief-divergence/recency/think30 | prefix_affinity-a8-r1 | 8/s | 30s | 1.88 | +0.936 | +1.035 | turn | 4 | 0 | 0% | cold opening | cold opening |
| docs/measurements/2026-09-10-belief-divergence/recency/think30 | prefix_affinity-a8-r2 | 8/s | 30s | 1.88 | +2.311 | +1.505 | turn | 4 | 0 | 0% | cold opening | cold opening |
| docs/measurements/2026-09-10-belief-divergence/recency/think30 | prefix_affinity-a8-r3 | 8/s | 30s | 1.88 | +1.933 | +1.578 | turn | 4 | 0 | 0% | cold opening | cold opening |
| docs/measurements/2026-09-10-belief-divergence/recency/think75 | prefix_affinity-a8-r1 | 8/s | 1m15s | 1.05 | +3.246 | -0.056 | turn | 2 | 2 | 0% | cold opening | fractional visit periods |
| docs/measurements/2026-09-10-belief-divergence/recency/think75 | prefix_affinity-a8-r2 | 8/s | 1m15s | 1.05 | +4.248 | -0.228 | turn | 2 | 2 | 0% | cold opening | fractional visit periods |
| docs/measurements/2026-09-10-belief-divergence/recency/think75 | prefix_affinity-a8-r3 | 8/s | 1m15s | 1.05 | +0.514 | -0.324 | turn | 2 | 2 | 0% | cold opening | fractional visit periods + fleet degrading |
| docs/measurements/2026-09-12-recency-rerun/think30 | prefix_affinity-a8-r1 | 8/s | 30s | 2.00 | -0.079 | -0.233 | turn | 4 | 0 | 2% | clear | clear |
| docs/measurements/2026-09-12-recency-rerun/think30 | prefix_affinity-a8-r2 | 8/s | 30s | 2.00 | -0.115 | -0.235 | turn | 4 | 0 | 1% | clear | clear |
| docs/measurements/2026-09-12-recency-rerun/think30 | prefix_affinity-a8-r3 | 8/s | 30s | 2.00 | -0.145 | -0.280 | turn | 4 | 0 | 2% | clear | fleet degrading |
| docs/measurements/2026-09-12-recency-rerun/think75 | prefix_affinity-a8-r1 | 8/s | 1m15s | 2.00 | -0.571 | -0.145 | turn | 4 | 0 | 3% | clear | clear |
| docs/measurements/2026-09-12-recency-rerun/think75 | prefix_affinity-a8-r2 | 8/s | 1m15s | 2.00 | -0.690 | -0.254 | turn | 4 | 0 | 2% | clear | fleet degrading |
| docs/measurements/2026-09-12-recency-rerun/think75 | prefix_affinity-a8-r3 | 8/s | 1m15s | 2.00 | -0.562 | -0.055 | turn | 4 | 0 | 3% | clear | clear |

120 of 216 cells changed verdict.

## Re-score integrity

Every one of the 216 cells reproduced its recorded requests, successes, window, goodput and TTFT percentiles from its own rows. Only the drift fields moved.

