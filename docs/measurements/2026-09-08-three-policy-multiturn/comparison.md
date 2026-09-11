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
percentile, pooled the way the goodput above is. The prefix cache hit rate is vLLM's own
counters, summed across those repetitions rather than averaged, because a rate is a ratio of counts.
An em dash is no usable cell; a prefix cache hit rate of — is a fleet whose counters were not
read, which is not the same as a cache that never hit.

The last four columns are the physical work, and the requests column before them is
their denominator. Prompt tokens recomputed is what the GPUs actually prefilled;
redundant prefill is what a policy computed over and above the policy that computed
least on the same bytes, which is the work cache-aware routing removed. The policy
that computed least per request is the floor the column is measured from and is
marked best.

Both are compared **per request**, and the totals are printed beside them only so the
two can be told apart. Under the closed-loop driver a virtual user sends its next turn
when its last one returns, so a policy that answers faster offers more prompts in the
same window: an identical workload guarantees both policies the same generator, not the
same number of prompts. A column of absolute totals therefore rises with throughput and
credits the slower policy with having wasted less. A redundant-prefill total here is
that policy's per-request excess times its *own* requests, never a difference of two
policies' totals.

A prefix cache hit rate and a token count can disagree, and idea.md §1 predicts two
published results where they did.

| driver | load | policy | TTFT p50 | TTFT p90 | TTFT p99 | prefix cache hit rate | requests | prompt tokens recomputed | recomputed / request | redundant prefill / request | redundant prefill, tokens |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| closed-loop | 1 users | round_robin | 640ms | — | 1285ms | 6.0% | 159 | 473189 | 2976.0 | +1758.8 | +279643 |
| closed-loop | 1 users | least_outstanding | 637ms | — | 1286ms | 6.0% | 158 | 468323 | 2964.1 | +1746.8 | +275994 |
| closed-loop | 1 users | session_affinity | 353ms | — | 402ms | 60.4% | 241 | 293362 | 1217.3 | best | best |
| closed-loop | 4 users | round_robin | 617ms | — | 1346ms | 27.4% | 674 | 1529774 | 2269.7 | +1323.6 | +892105 |
| closed-loop | 4 users | least_outstanding | 382ms | — | 1329ms | 40.5% | 790 | 1468698 | 1859.1 | +913.0 | +721282 |
| closed-loop | 4 users | session_affinity | 355ms | — | 746ms | 69.6% | 832 | 787152 | 946.1 | best | best |
| closed-loop | 8 users | round_robin | 422ms | — | 1723ms | 36.1% | 657 | 1315354 | 2002.1 | +1108.3 | +728126 |
| closed-loop | 8 users | least_outstanding | 392ms | — | 1590ms | 46.1% | 1179 | 1990659 | 1688.4 | +794.6 | +936866 |
| closed-loop | 8 users | session_affinity | 365ms | — | 872ms | 71.3% | 1362 | 1217359 | 893.8 | best | best |
| closed-loop | 16 users | round_robin | 641ms | — | 2622ms | 36.6% | 1159 | 2299972 | 1984.4 | +1159.2 | +1343553 |
| closed-loop | 16 users | least_outstanding | 415ms | — | 1632ms | 59.1% | 1775 | 2273530 | 1280.9 | +455.7 | +808782 |
| closed-loop | 16 users | session_affinity | 376ms | — | 1239ms | 73.5% | 1880 | 1551395 | 825.2 | best | best |
| closed-loop | 32 users | round_robin | 745ms | — | 5160ms | 32.2% | 1175 | 2485239 | 2115.1 | +1328.3 | +1560726 |
| closed-loop | 32 users | least_outstanding | 487ms | — | 2084ms | 54.3% | 1933 | 2697014 | 1395.2 | +608.4 | +1176092 |
| closed-loop | 32 users | session_affinity | 419ms | — | 1573ms | 74.5% | 2268 | 1784506 | 786.8 | best | best |
| closed-loop | 64 users | round_robin | 971ms | — | 5500ms | 31.3% | 821 | 1790261 | 2180.6 | +1417.6 | +1163868 |
| closed-loop | 64 users | least_outstanding | 843ms | — | 2935ms | 50.8% | 2000 | 2944972 | 1472.5 | +709.5 | +1419044 |
| closed-loop | 64 users | session_affinity | 441ms | — | 2369ms | 75.3% | 2457 | 1874602 | 763.0 | best | best |
| closed-loop | 128 users | round_robin | 1956ms | — | 12701ms | 28.9% | 850 | 2035180 | 2394.3 | +1710.3 | +1453764 |
| closed-loop | 128 users | least_outstanding | 1240ms | — | 4646ms | 50.2% | 1182 | 1951770 | 1651.2 | +967.2 | +1143260 |
| closed-loop | 128 users | session_affinity | 633ms | — | 2646ms | 78.0% | 3282 | 2244950 | 684.0 | best | best |
| closed-loop | 256 users | round_robin | 7067ms | — | 30234ms | 22.9% | 1432 | 3938171 | 2750.1 | — | — |
| closed-loop | 256 users | least_outstanding | 13002ms | — | 31759ms | 28.5% | 1419 | 4096361 | 2886.8 | — | — |
| closed-loop | 256 users | session_affinity | — | — | — | — | — | — | — | — | — |
| open-loop | 2 req/s | round_robin | — | — | — | — | — | — | — | — | — |
| open-loop | 2 req/s | least_outstanding | 604ms | — | 1308ms | 23.1% | 130 | 301329 | 2317.9 | — | — |
| open-loop | 2 req/s | session_affinity | 349ms | — | 410ms | 63.2% | 390 | 434108 | 1113.1 | — | — |
| open-loop | 4 req/s | round_robin | — | — | — | — | — | — | — | — | — |
| open-loop | 4 req/s | least_outstanding | 616ms | — | 2101ms | 27.6% | 260 | 568984 | 2188.4 | — | — |
| open-loop | 4 req/s | session_affinity | 354ms | — | 557ms | 67.2% | 780 | 772668 | 990.6 | — | — |
| open-loop | 6 req/s | round_robin | 666ms | — | 3054ms | 28.7% | 1173 | 2527082 | 2154.4 | +1220.2 | +1431305 |
| open-loop | 6 req/s | least_outstanding | 665ms | — | 3124ms | 28.9% | 782 | 1679825 | 2148.1 | +1213.9 | +949307 |
| open-loop | 6 req/s | session_affinity | 363ms | — | 842ms | 69.1% | 1173 | 1095777 | 934.2 | best | best |
| open-loop | 8 req/s | round_robin | 6545ms | — | 16322ms | 29.6% | 1560 | 3428102 | 2197.5 | +1362.6 | +2125599 |
| open-loop | 8 req/s | least_outstanding | 6830ms | — | 15803ms | 31.3% | 1560 | 3366122 | 2157.8 | +1322.8 | +2063619 |
| open-loop | 8 req/s | session_affinity | 368ms | — | 1229ms | 72.4% | 1560 | 1302503 | 834.9 | best | best |
| open-loop | 10 req/s | round_robin | 20456ms | — | 35057ms | 30.8% | 1950 | 4374664 | 2243.4 | +1445.3 | +2818272 |
| open-loop | 10 req/s | least_outstanding | 21310ms | — | 42693ms | 30.9% | 1950 | 4424548 | 2269.0 | +1470.8 | +2868156 |
| open-loop | 10 req/s | session_affinity | 373ms | — | 2022ms | 73.7% | 1950 | 1556392 | 798.1 | best | best |
| open-loop | 12 req/s | round_robin | 33440ms | — | 62256ms | 31.8% | 2343 | 5391341 | 2301.0 | +1530.1 | +3585018 |
| open-loop | 12 req/s | least_outstanding | 33962ms | — | 61362ms | 32.5% | 2343 | 5359552 | 2287.5 | +1516.5 | +3553229 |
| open-loop | 12 req/s | session_affinity | 426ms | — | 2773ms | 74.5% | 2343 | 1806323 | 770.9 | best | best |
| open-loop | 14 req/s | round_robin | 48134ms | — | 83018ms | 32.5% | 2733 | 6412355 | 2346.3 | +1631.3 | +4458233 |
| open-loop | 14 req/s | least_outstanding | 47929ms | — | 79508ms | 33.7% | 2733 | 6278831 | 2297.4 | +1582.4 | +4324709 |
| open-loop | 14 req/s | session_affinity | 422ms | — | 11170ms | 76.6% | 1822 | 1302748 | 715.0 | best | best |
| open-loop | 16 req/s | round_robin | 63853ms | — | 105838ms | 32.4% | 3120 | 7395296 | 2370.3 | +1634.9 | +5100851 |
| open-loop | 16 req/s | least_outstanding | 64403ms | — | 113659ms | 31.9% | 3120 | 7492044 | 2401.3 | +1665.9 | +5197599 |
| open-loop | 16 req/s | session_affinity | 2529ms | — | 29525ms | 76.9% | 2080 | 1529630 | 735.4 | best | best |
| open-loop | 20 req/s | round_robin | 100020ms | — | 162347ms | 29.3% | 3900 | 10003986 | 2565.1 | +1811.3 | +7064255 |
| open-loop | 20 req/s | least_outstanding | 101395ms | — | 161645ms | 29.6% | 3900 | 9928161 | 2545.7 | +1791.9 | +6988430 |
| open-loop | 20 req/s | session_affinity | 11934ms | — | 46664ms | 76.5% | 3900 | 2939731 | 753.8 | best | best |

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
