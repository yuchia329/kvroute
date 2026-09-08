# Policy comparison — goodput against the derived SLO

SLO: TTFT < 990ms, inter-token p50 < 24ms. Goodput is requests per second that met it,
so a policy that completed more requests can still score lower.

Workload, the same for every cell here — both policies sent the same bytes at the same
load point, which is what makes them comparable at all:

    multiturn(sessions=307,skew=0,turns=4,prompt=448t,output=64t,system=0.3x128t,branch=0.3x8fam1t,bpt=4,seed=1)

Each figure is the median of that cell's repetitions, with the range across them. A
difference smaller than those ranges is a difference between a policy and itself.

Prompt bytes per token: **1.59 bytes per token**, measured as the 410024191 prompt bytes these
cells offered over the 258235143 prompt tokens the engines reported processing. A prefix match
is recorded in bytes, and this is what converts it — it is not assumed.

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
| open-loop | 2 req/s | — | 1.74 (n=1) | 2.00 (2.00–2.00, n=3) | — | — |
| open-loop | 4 req/s | — | 3.31 (n=1) | 4.00 (3.98–4.00, n=3) | — | — |
| open-loop | 6 req/s | 3.90 (3.84–3.99, n=3) | 3.48 (3.48–3.82, n=2) | 5.98 (5.97–6.00, n=3) | -10.6% | +53.5% |
| open-loop | 8 req/s | 0.06 (0.00–0.11, n=3) | 0.00 (0.00–0.00, n=3) | 7.63 (7.31–7.80, n=3) | -100.0% (within spread) | +12300.0% |
| open-loop | 10 req/s | 0.00 (0.00–0.00, n=3) | 0.00 (0.00–0.00, n=3) | 8.80 (5.00–9.51, n=3) | +0.00/s over a baseline of zero | +8.80/s over a baseline of zero |
| open-loop | 12 req/s | 0.00 (0.00–0.00, n=3) | 0.00 (0.00–0.00, n=3) | 9.59 (7.96–9.65, n=3) | +0.00/s over a baseline of zero | +9.59/s over a baseline of zero |
| open-loop | 14 req/s | 0.00 (0.00–0.00, n=3) | 0.00 (0.00–0.00, n=3) | 8.05 (8.05–9.64, n=2) | +0.00/s over a baseline of zero | +8.05/s over a baseline of zero |
| open-loop | 16 req/s | 0.00 (0.00–0.00, n=3) | 0.00 (0.00–0.00, n=3) | 3.88 (3.88–4.05, n=2) | +0.00/s over a baseline of zero | +3.88/s over a baseline of zero |
| open-loop | 20 req/s | 0.00 (0.00–0.00, n=3) | 0.00 (0.00–0.00, n=3) | 3.12 (1.20–3.14, n=3) | +0.00/s over a baseline of zero | +3.12/s over a baseline of zero |

## What produced it — TTFT percentiles, prefix cache hit rate and prefill work

The percentiles are each the median across a cell's repetitions of that repetition's own
percentile, pooled the way the goodput above is. The hit rate is vLLM's own counters,
summed across those repetitions rather than averaged, because a rate is a ratio of counts.
An em dash is no usable cell; a hit rate of — is a fleet whose counters were not read,
which is not the same as a cache that never hit.

The last two columns are the physical work. Prompt tokens recomputed is what the GPUs
actually prefilled; redundant prefill is what a policy computed over and above the policy
that computed least on the same bytes, which is the work cache-aware routing removed.
The policy that computed least is the floor the column is measured from and is marked
best. A hit rate and a token count can disagree, and idea.md §1 predicts two published
results where they did.

| driver | load | policy | TTFT p50 | TTFT p99 | prefix cache hit rate | prompt tokens recomputed | redundant prefill |
|---|---:|---|---:|---:|---:|---:|---:|
| closed-loop | 1 users | round_robin | 640ms | 1285ms | 6.0% | 473189 | +179827 |
| closed-loop | 1 users | least_outstanding | 637ms | 1286ms | 6.0% | 468323 | +174961 |
| closed-loop | 1 users | session_affinity | 353ms | 402ms | 60.4% | 293362 | best |
| closed-loop | 4 users | round_robin | 617ms | 1346ms | 27.4% | 1529774 | +742622 |
| closed-loop | 4 users | least_outstanding | 382ms | 1329ms | 40.5% | 1468698 | +681546 |
| closed-loop | 4 users | session_affinity | 355ms | 746ms | 69.6% | 787152 | best |
| closed-loop | 8 users | round_robin | 422ms | 1723ms | 36.1% | 1315354 | +97995 |
| closed-loop | 8 users | least_outstanding | 392ms | 1590ms | 46.1% | 1990659 | +773300 |
| closed-loop | 8 users | session_affinity | 365ms | 872ms | 71.3% | 1217359 | best |
| closed-loop | 16 users | round_robin | 641ms | 2622ms | 36.6% | 2299972 | +748577 |
| closed-loop | 16 users | least_outstanding | 415ms | 1632ms | 59.1% | 2273530 | +722135 |
| closed-loop | 16 users | session_affinity | 376ms | 1239ms | 73.5% | 1551395 | best |
| closed-loop | 32 users | round_robin | 745ms | 5160ms | 32.2% | 2485239 | +700733 |
| closed-loop | 32 users | least_outstanding | 487ms | 2084ms | 54.3% | 2697014 | +912508 |
| closed-loop | 32 users | session_affinity | 419ms | 1573ms | 74.5% | 1784506 | best |
| closed-loop | 64 users | round_robin | 971ms | 5500ms | 31.3% | 1790261 | best |
| closed-loop | 64 users | least_outstanding | 843ms | 2935ms | 50.8% | 2944972 | +1154711 |
| closed-loop | 64 users | session_affinity | 441ms | 2369ms | 75.3% | 1874602 | +84341 |
| closed-loop | 128 users | round_robin | 1956ms | 12701ms | 28.9% | 2035180 | +83410 |
| closed-loop | 128 users | least_outstanding | 1240ms | 4646ms | 50.2% | 1951770 | best |
| closed-loop | 128 users | session_affinity | 633ms | 2646ms | 78.0% | 2244950 | +293180 |
| closed-loop | 256 users | round_robin | 7067ms | 30234ms | 22.9% | 3938171 | best |
| closed-loop | 256 users | least_outstanding | 13002ms | 31759ms | 28.5% | 4096361 | +158190 |
| closed-loop | 256 users | session_affinity | — | — | — | — | — |
| open-loop | 2 req/s | round_robin | — | — | — | — | — |
| open-loop | 2 req/s | least_outstanding | 604ms | 1308ms | 23.1% | 301329 | best |
| open-loop | 2 req/s | session_affinity | 349ms | 410ms | 63.2% | 434108 | +132779 |
| open-loop | 4 req/s | round_robin | — | — | — | — | — |
| open-loop | 4 req/s | least_outstanding | 616ms | 2101ms | 27.6% | 568984 | best |
| open-loop | 4 req/s | session_affinity | 354ms | 557ms | 67.2% | 772668 | +203684 |
| open-loop | 6 req/s | round_robin | 666ms | 3054ms | 28.7% | 2527082 | +1431305 |
| open-loop | 6 req/s | least_outstanding | 665ms | 3124ms | 28.9% | 1679825 | +584048 |
| open-loop | 6 req/s | session_affinity | 363ms | 842ms | 69.1% | 1095777 | best |
| open-loop | 8 req/s | round_robin | 6545ms | 16322ms | 29.6% | 3428102 | +2125599 |
| open-loop | 8 req/s | least_outstanding | 6830ms | 15803ms | 31.3% | 3366122 | +2063619 |
| open-loop | 8 req/s | session_affinity | 368ms | 1229ms | 72.4% | 1302503 | best |
| open-loop | 10 req/s | round_robin | 20456ms | 35057ms | 30.8% | 4374664 | +2818272 |
| open-loop | 10 req/s | least_outstanding | 21310ms | 42693ms | 30.9% | 4424548 | +2868156 |
| open-loop | 10 req/s | session_affinity | 373ms | 2022ms | 73.7% | 1556392 | best |
| open-loop | 12 req/s | round_robin | 33440ms | 62256ms | 31.8% | 5391341 | +3585018 |
| open-loop | 12 req/s | least_outstanding | 33962ms | 61362ms | 32.5% | 5359552 | +3553229 |
| open-loop | 12 req/s | session_affinity | 426ms | 2773ms | 74.5% | 1806323 | best |
| open-loop | 14 req/s | round_robin | 48134ms | 83018ms | 32.5% | 6412355 | +5109607 |
| open-loop | 14 req/s | least_outstanding | 47929ms | 79508ms | 33.7% | 6278831 | +4976083 |
| open-loop | 14 req/s | session_affinity | 422ms | 11170ms | 76.6% | 1302748 | best |
| open-loop | 16 req/s | round_robin | 63853ms | 105838ms | 32.4% | 7395296 | +5865666 |
| open-loop | 16 req/s | least_outstanding | 64403ms | 113659ms | 31.9% | 7492044 | +5962414 |
| open-loop | 16 req/s | session_affinity | 2529ms | 29525ms | 76.9% | 1529630 | best |
| open-loop | 20 req/s | round_robin | 100020ms | 162347ms | 29.3% | 10003986 | +7064255 |
| open-loop | 20 req/s | least_outstanding | 101395ms | 161645ms | 29.6% | 9928161 | +6988430 |
| open-loop | 20 req/s | session_affinity | 11934ms | 46664ms | 76.5% | 2939731 | best |

Excluded from every figure above — §6 discards these rather than averaging them in:

- `least_outstanding-a2-r1`: still warming up: the first half of the measured window was 54% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `least_outstanding-a2-r3`: still warming up: the first half of the measured window was 48% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `least_outstanding-a4-r1`: still warming up: the first half of the measured window was 49% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `least_outstanding-a4-r2`: still warming up: the first half of the measured window was 43% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `least_outstanding-a6-r2`: still warming up: the first half of the measured window was 41% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `least_outstanding-c128-r1`: still warming up: the first half of the measured window was 46% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-a2-r1`: still warming up: the first half of the measured window was 68% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-a2-r2`: still warming up: the first half of the measured window was 64% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-a2-r3`: still warming up: the first half of the measured window was 54% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-a4-r1`: still warming up: the first half of the measured window was 60% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-a4-r2`: still warming up: the first half of the measured window was 46% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-a4-r3`: still warming up: the first half of the measured window was 54% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c128-r2`: still warming up: the first half of the measured window was 53% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c64-r3`: still warming up: the first half of the measured window was 36% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c8-r3`: still warming up: the first half of the measured window was 53% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-a14-r2`: still warming up: the first half of the measured window was 33% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-a16-r3`: still warming up: the first half of the measured window was 619% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c256-r1`: still warming up: the first half of the measured window was 359% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c256-r2`: still warming up: the first half of the measured window was 252% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c256-r3`: still warming up: the first half of the measured window was 78% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
