# Policy comparison — goodput against the derived SLO

SLO: TTFT < 990ms, inter-token p50 < 24ms. Goodput is requests per second that met it,
so a policy that completed more requests can still score lower.

Workload, the same for every cell here — both policies sent the same bytes at the same
load point, which is what makes them comparable at all:

    multiturn(sessions=307,skew=0,turns=4,prompt=448t,output=64t,system=0.3x128t,branch=0.3x8fam1t,bpt=4,seed=1)

Each figure is the median of that cell's repetitions, with the range across them. A
difference smaller than those ranges is a difference between a policy and itself.

| driver | load | round_robin | least_outstanding | session_affinity | Δ least_outstanding vs round_robin | Δ session_affinity vs round_robin |
|---|---:|---:|---:|---:|---:|---:|
| closed-loop | 1 users | 0.62 (0.62–0.62, n=3) | 0.62 (0.62–0.63, n=3) | 1.22 (1.22–1.26, n=3) | +0.3% (within spread) | +97.1% |
| closed-loop | 4 users | 2.88 (2.73–2.89, n=3) | 3.54 (3.33–3.76, n=3) | 4.15 (4.14–4.38, n=3) | +23.0% | +44.2% |
| closed-loop | 8 users | 3.86 (3.86–3.89, n=2) | 4.97 (4.88–5.01, n=3) | 6.81 (6.72–7.09, n=3) | +28.9% | +76.5% |
| closed-loop | 16 users | 4.06 (3.88–4.10, n=3) | 8.22 (7.94–8.32, n=3) | 9.59 (7.91–10.01, n=3) | +102.7% | +136.6% |
| closed-loop | 32 users | 3.29 (3.16–3.34, n=3) | 7.26 (7.19–7.51, n=3) | 9.90 (6.95–11.36, n=3) | +120.6% | +201.1% |
| closed-loop | 64 users | 2.63 (2.63–2.77, n=2) | 5.56 (5.05–5.57, n=3) | 7.08 (5.91–7.48, n=3) | +111.7% | +169.6% |
| closed-loop | 128 users | 1.33 (1.33–1.37, n=2) | 0.03 (0.03–0.12, n=2) | 4.17 (3.37–6.09, n=3) | -97.8% | +212.5% |
| closed-loop | 256 users | 0.05 (0.02–0.25, n=3) | 0.00 (0.00–0.00, n=3) | — | -100.0% | — |
| open-loop | 2 req/s | 2.00 (2.00–2.00, n=3) | 1.74 (1.74–1.80, n=3) | 2.00 (2.00–2.00, n=3) | -13.1% | +0.0% (within spread) |
| open-loop | 4 req/s | 4.00 (4.00–4.00, n=3) | — | 3.98 (3.97–4.00, n=3) | — | -0.4% (within spread) |
| open-loop | 6 req/s | 6.00 (6.00–6.00, n=3) | 3.71 (3.71–3.73, n=2) | 5.97 (5.97–5.97, n=3) | -38.1% | -0.5% |
| open-loop | 8 req/s | 8.00 (8.00–8.00, n=3) | 0.00 (0.00–0.05, n=3) | 7.89 (7.77–7.94, n=3) | -100.0% | -1.3% |
| open-loop | 10 req/s | 10.00 (10.00–10.00, n=3) | 0.00 (0.00–0.00, n=3) | 5.15 (5.15–8.83, n=2) | -100.0% | -48.5% |
| open-loop | 12 req/s | 5.64 (5.24–6.31, n=3) | 0.00 (0.00–0.00, n=3) | 9.05 (8.93–10.20, n=3) | -100.0% | +60.5% |
| open-loop | 14 req/s | 0.00 (0.00–0.00, n=3) | 0.00 (0.00–0.00, n=3) | 9.24 (9.24–9.25, n=2) | +0.00/s over a baseline of zero | +9.24/s over a baseline of zero |
| open-loop | 16 req/s | 0.00 (0.00–0.00, n=3) | 0.00 (0.00–0.00, n=3) | 4.02 (4.02–4.78, n=2) | +0.00/s over a baseline of zero | +4.02/s over a baseline of zero |
| open-loop | 20 req/s | 0.00 (0.00–0.00, n=3) | 0.00 (0.00–0.00, n=3) | 2.97 (1.17–3.28, n=3) | +0.00/s over a baseline of zero | +2.97/s over a baseline of zero |

Excluded from every figure above — §6 discards these rather than averaging them in:

- `least_outstanding-a4-r1`: still warming up: the first half of the measured window was 51% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `least_outstanding-a4-r2`: still warming up: the first half of the measured window was 50% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `least_outstanding-a4-r3`: still warming up: the first half of the measured window was 68% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `least_outstanding-a6-r1`: still warming up: the first half of the measured window was 27% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `least_outstanding-c128-r1`: still warming up: the first half of the measured window was 46% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c128-r2`: still warming up: the first half of the measured window was 53% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c64-r3`: still warming up: the first half of the measured window was 36% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c8-r3`: still warming up: the first half of the measured window was 53% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-a10-r2`: still warming up: the first half of the measured window was 26% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-a14-r2`: still warming up: the first half of the measured window was 135% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-a16-r3`: still warming up: the first half of the measured window was 458% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c256-r1`: still warming up: the first half of the measured window was 359% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c256-r2`: still warming up: the first half of the measured window was 252% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c256-r3`: still warming up: the first half of the measured window was 78% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
