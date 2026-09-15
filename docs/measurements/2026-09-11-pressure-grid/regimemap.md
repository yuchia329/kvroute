# The regime map — which policy wins at each recorded point

SLO: TTFT < 990ms, inter-token p50 < 24ms. Goodput is requests per second that met it.

Each tile names the policy with the highest pooled goodput at that point and its margin
over the runner-up, qualified the way the pressure map qualifies its own deltas: a margin
marked *within spread* is smaller than the run-to-run range behind it, which is a
difference between a policy and itself. Nothing here is a new reduction of cells — every
figure is read off the comparison already built, so pooling, exclusion and the one-SLO
check are inherited rather than repeated.

## The map — winner and margin

| WS \ skew | 0 | 1 | 1.4 |
|---|---|---|---|
| **0.25** | session_affinity, +7.3% (within spread) | prefix_affinity, +93.1% | prefix_affinity, +37.2% |
| **1** | prefix_affinity, +66.2% | prefix_affinity, +38.6% | prefix_affinity, +44.9% |
| **3** | prefix_affinity, +24.6% | prefix_affinity, +51.4% | prefix_affinity, +46.2% |
| **8** | prefix_affinity, +21.0% | prefix_affinity, +25.3% | prefix_affinity, +44.2% |

## Goodput behind the map

Each figure is the median of that point's repetitions with the range across them, and the
spread beneath it as a share of that median. An em dash is a policy with no usable cell at
that point, which is not a zero.

| WS | skew | load | round_robin | least_outstanding | session_affinity | prefix_affinity |
|---|---|---|---:|---:|---:|---:|
| 0.25 | 0 | 32 users | 5.40 (5.07–5.42, n=3)<br>±6% | 11.23 (10.99–11.38, n=3)<br>±3% | 34.37 (30.11–34.77, n=3)<br>±14% | 32.04 (32.03–34.02, n=3)<br>±6% |
| 0.25 | 1 | 32 users | 9.87 (9.87–10.06, n=2)<br>±2% | 17.84 (17.84–18.96, n=2)<br>±6% | 8.83 (8.83–24.61, n=2)<br>±179% | 34.43 (34.20–34.94, n=3)<br>±2% |
| 0.25 | 1.4 | 32 users | 16.27 (16.04–16.57, n=3)<br>±3% | 25.35 (24.33–26.02, n=3)<br>±7% | 7.36 (5.15–13.86, n=3)<br>±118% | 34.79 (34.67–35.70, n=3)<br>±3% |
| 1 | 0 | 32 users | 3.51 (3.37–3.55, n=3)<br>±5% | 7.84 (7.71–8.16, n=3)<br>±6% | 8.99 (8.19–10.24, n=3)<br>±23% | 14.94 (14.72–15.10, n=3)<br>±3% |
| 1 | 1 | 32 users | 6.41 (6.33–6.42, n=3)<br>±1% | 13.49 (13.31–13.74, n=3)<br>±3% | 16.96 (12.56–19.07, n=3)<br>±38% | 23.51 (23.02–23.66, n=3)<br>±3% |
| 1 | 1.4 | 32 users | 11.22 (11.22–11.66, n=2)<br>±4% | 21.01 (20.48–21.27, n=3)<br>±4% | 11.23 (10.89–13.48, n=3)<br>±23% | 30.45 (30.31–31.42, n=3)<br>±4% |
| 3 | 0 | 32 users | 3.24 (3.16–3.30, n=3)<br>±4% | 7.57 (7.50–7.75, n=3)<br>±3% | 9.32 (8.31–9.58, n=3)<br>±14% | 11.61 (11.54–11.64, n=3)<br>±1% |
| 3 | 1 | 32 users | 5.15 (5.08–5.28, n=3)<br>±4% | 11.76 (11.71–11.94, n=3)<br>±2% | 12.53 (12.53–14.35, n=2)<br>±15% | 18.97 (18.85–19.07, n=3)<br>±1% |
| 3 | 1.4 | 32 users | 11.23 (10.86–11.53, n=3)<br>±6% | 19.69 (19.59–19.97, n=3)<br>±2% | 6.44 (5.92–11.84, n=3)<br>±92% | 28.79 (28.49–29.31, n=3)<br>±3% |
| 8 | 0 | 32 users | 3.21 (3.20–3.24, n=3)<br>±1% | 7.09 (7.02–7.44, n=3)<br>±6% | 8.83 (8.55–9.50, n=3)<br>±11% | 10.68 (10.46–10.70, n=3)<br>±2% |
| 8 | 1 | 32 users | 4.74 (4.61–4.87, n=3)<br>±5% | 11.05 (10.81–11.20, n=3)<br>±4% | 13.49 (12.82–15.09, n=3)<br>±17% | 16.91 (16.81–17.05, n=3)<br>±1% |
| 8 | 1.4 | 32 users | 10.21 (10.00–10.22, n=3)<br>±2% | 19.44 (19.03–19.45, n=3)<br>±2% | 10.69 (6.77–11.12, n=3)<br>±41% | 28.04 (27.73–28.27, n=3)<br>±2% |

## What this map does not rest on

Cells excluded from every figure above — §6 discards these rather than averaging them in:

- `least_outstanding-c32-r3`: the fleet slowed across the measured window: TTFT p50 was 27% slower late than early, compared within each of 4 turn indices, over a 25% threshold. A longer warm-up is not the fix: this cell's latency percentiles are a transient rather than a steady state. Look for a queue that never settled — an open-loop cell offered more than the fleet can serve never reaches one — or a throttled card, a replica lost, or a cache growing
- `round_robin-c32-r3`: still warming up: TTFT p50 was 30% slower early in the measured window than late, compared within each of 4 turn indices, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c32-r2`: still warming up: TTFT p50 was 52% slower early in the measured window than late, compared within each of 4 turn indices, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c32-r2`: still warming up: TTFT p50 was 32% slower early in the measured window than late, compared within each of 4 turn indices, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c32-r2`: still warming up: TTFT p50 was 32% slower early in the measured window than late, compared within each of 4 turn indices, over a 25% threshold. Lengthen the warm-up and re-run

## Contested — where a policy beats prefix affinity beyond the spread

No point contests prefix affinity beyond the spread.
