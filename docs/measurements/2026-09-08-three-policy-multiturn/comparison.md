# Policy comparison — goodput against the derived SLO

SLO: TTFT < 990ms, inter-token p50 < 24ms. Goodput is requests per second that met it,
so a policy that completed more requests can still score lower.

Workload, the same for every cell here — both policies sent the same bytes at the same
load point, which is what makes them comparable at all:

    multiturn(sessions=307,skew=0,turns=4,prompt=448t,output=64t,system=0.3x128t,branch=0.3x8fam1t,bpt=4,seed=1)

Each figure is the median of that cell's repetitions, with the range across them. A
difference smaller than those ranges is a difference between a policy and itself.

Prompt bytes per token: **1.59 bytes per token**, measured as the 421975139 prompt bytes these
cells offered over the 265440151 prompt tokens the engines reported processing. A prefix match
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
| open-loop | 2 req/s | 1.74 (1.68–1.74, n=3) | 1.74 (1.68–1.75, n=3) | 2.00 (2.00–2.00, n=3) | +0.0% (within spread) | +15.0% |
| open-loop | 4 req/s | 3.45 (3.42–3.55, n=3) | 3.37 (3.31–3.49, n=3) | 4.00 (3.98–4.00, n=3) | -2.2% (within spread) | +16.1% |
| open-loop | 6 req/s | 3.90 (3.84–3.99, n=3) | 3.48 (3.48–3.82, n=2) | 5.98 (5.97–6.00, n=3) | -10.6% | +53.5% |
| open-loop | 8 req/s | 0.06 (0.00–0.11, n=3) ⚠ | 0.00 (0.00–0.00, n=3) ⚠ | 7.63 (7.31–7.80, n=3) | -100.0% (within spread) ⚠ | +12300.0% ⚠ |
| open-loop | 10 req/s | 0.00 (0.00–0.00, n=3) ⚠ | 0.00 (0.00–0.00, n=3) ⚠ | 8.80 (8.80–9.51, n=2) | +0.00/s over a baseline of zero ⚠ | +8.80/s over a baseline of zero ⚠ |
| open-loop | 12 req/s | 0.00 (0.00–0.00, n=3) ⚠ | 0.00 (0.00–0.00, n=3) ⚠ | 9.59 (7.96–9.65, n=3) | +0.00/s over a baseline of zero ⚠ | +9.59/s over a baseline of zero ⚠ |
| open-loop | 14 req/s | 0.00 (0.00–0.00, n=3) ⚠ | 0.00 (0.00–0.00, n=3) ⚠ | 8.05 (8.05–9.64, n=2) | +0.00/s over a baseline of zero ⚠ | +8.05/s over a baseline of zero ⚠ |
| open-loop | 16 req/s | 0.00 (0.00–0.00, n=3) ⚠ | 0.00 (0.00–0.00, n=3) ⚠ | 4.05 (3.88–6.82, n=3) ⚠ | +0.00/s over a baseline of zero ⚠ | +4.05/s over a baseline of zero ⚠ |
| open-loop | 20 req/s | 0.00 (0.00–0.00, n=3) ⚠ | 0.00 (0.00–0.00, n=3) ⚠ | 3.12 (1.20–3.14, n=3) ⚠ | +0.00/s over a baseline of zero ⚠ | +3.12/s over a baseline of zero ⚠ |

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
| open-loop | 2 req/s | round_robin | 596ms | — | 1298ms | 25.5% | 390 | 877724 | 2250.6 | +1137.5 | +443616 |
| open-loop | 2 req/s | least_outstanding | 601ms | — | 1299ms | 24.7% | 390 | 886620 | 2273.4 | +1160.3 | +452512 |
| open-loop | 2 req/s | session_affinity | 349ms | — | 410ms | 63.2% | 390 | 434108 | 1113.1 | best | best |
| open-loop | 4 req/s | round_robin | 608ms | — | 1409ms | 30.0% | 780 | 1650076 | 2115.5 | +1124.9 | +877408 |
| open-loop | 4 req/s | least_outstanding | 616ms | — | 1815ms | 29.8% | 780 | 1656044 | 2123.1 | +1132.5 | +883376 |
| open-loop | 4 req/s | session_affinity | 354ms | — | 557ms | 67.2% | 780 | 772668 | 990.6 | best | best |
| open-loop | 6 req/s | round_robin | 666ms | — | 3054ms | 28.7% | 1173 | 2527082 | 2154.4 | +1220.2 | +1431305 |
| open-loop | 6 req/s | least_outstanding | 665ms | — | 3124ms | 28.9% | 782 | 1679825 | 2148.1 | +1213.9 | +949307 |
| open-loop | 6 req/s | session_affinity | 363ms | — | 842ms | 69.1% | 1173 | 1095777 | 934.2 | best | best |
| open-loop | 8 req/s | round_robin | — | — | — | 29.6% | 1560 | 3428102 | 2197.5 | +1362.6 | +2125599 |
| open-loop | 8 req/s | least_outstanding | — | — | — | 31.3% | 1560 | 3366122 | 2157.8 | +1322.8 | +2063619 |
| open-loop | 8 req/s | session_affinity | 368ms | — | 1229ms | 72.4% | 1560 | 1302503 | 834.9 | best | best |
| open-loop | 10 req/s | round_robin | — | — | — | 30.8% | 1950 | 4374664 | 2243.4 | +1462.5 | +2851930 |
| open-loop | 10 req/s | least_outstanding | — | — | — | 30.9% | 1950 | 4424548 | 2269.0 | +1488.1 | +2901814 |
| open-loop | 10 req/s | session_affinity | 363ms | — | 1520ms | 74.2% | 1300 | 1015156 | 780.9 | best | best |
| open-loop | 12 req/s | round_robin | — | — | — | 31.8% | 2343 | 5391341 | 2301.0 | +1530.1 | +3585018 |
| open-loop | 12 req/s | least_outstanding | — | — | — | 32.5% | 2343 | 5359552 | 2287.5 | +1516.5 | +3553229 |
| open-loop | 12 req/s | session_affinity | 426ms | — | 2773ms | 74.5% | 2343 | 1806323 | 770.9 | best | best |
| open-loop | 14 req/s | round_robin | — | — | — | 32.5% | 2733 | 6412355 | 2346.3 | +1631.3 | +4458233 |
| open-loop | 14 req/s | least_outstanding | — | — | — | 33.7% | 2733 | 6278831 | 2297.4 | +1582.4 | +4324709 |
| open-loop | 14 req/s | session_affinity | 422ms | — | 11170ms | 76.6% | 1822 | 1302748 | 715.0 | best | best |
| open-loop | 16 req/s | round_robin | — | — | — | 32.4% | 3120 | 7395296 | 2370.3 | +1636.6 | +5106229 |
| open-loop | 16 req/s | least_outstanding | — | — | — | 31.9% | 3120 | 7492044 | 2401.3 | +1667.6 | +5202977 |
| open-loop | 16 req/s | session_affinity | — | — | — | 76.9% | 3120 | 2289067 | 733.7 | best | best |
| open-loop | 20 req/s | round_robin | — | — | — | 29.3% | 3900 | 10003986 | 2565.1 | +1811.3 | +7064255 |
| open-loop | 20 req/s | least_outstanding | — | — | — | 29.6% | 3900 | 9928161 | 2545.7 | +1791.9 | +6988430 |
| open-loop | 20 req/s | session_affinity | — | — | — | 76.5% | 3900 | 2939731 | 753.8 | best | best |

## How evenly it loaded the fleet

Counted from the rows — how many measured requests each replica served — and not read
off the router's per-decision inflight column, which recorded zero on every
session-affinity row ever written before 9311e68 (#27). Every row is counted whatever
its outcome: a request a replica accepted and then failed still occupied it.

**Nothing here counted its placements.** Every policy in this table has a repetition
recorded before 9311e68, so no imbalance figure can be pooled from it — and the inflight
column those cells do carry reads zero throughout, which means "not recorded" and not
"idle". Imbalance for these is still recoverable, by counting the per-request rows kept
beside them.


⚠ In the figures above, marked — these dropped or failed more of their requests than the
threshold allows, or fell behind the load they were offered. That is what the policy did to
the fleet at that load, not a broken measurement, so it is shown rather than dropped. A cell
that fell behind is in the goodput and not in the TTFT percentiles, which never settled:

- `least_outstanding-a10-r1`: the fleet fell behind its offered load: 75% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 48% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a10-r2`: the fleet fell behind its offered load: 70% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 52% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a10-r3`: the fleet fell behind its offered load: 75% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 54% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a12-r1`: the fleet fell behind its offered load: 79% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 52% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a12-r2`: the fleet fell behind its offered load: 80% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 46% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a12-r3`: the fleet fell behind its offered load: 80% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 51% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a14-r1`: the fleet fell behind its offered load: 86% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 52% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a14-r2`: the fleet fell behind its offered load: 83% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 53% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a14-r3`: the fleet fell behind its offered load: 87% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 48% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a16-r1`: the fleet fell behind its offered load: 93% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 50% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a16-r2`: the fleet fell behind its offered load: 91% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 53% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a16-r3`: the fleet fell behind its offered load: 91% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 51% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a20-r1`: the fleet fell behind its offered load: 99% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 52% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a20-r2`: the fleet fell behind its offered load: 98% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 51% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a20-r3`: the fleet fell behind its offered load: 98% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 53% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a8-r1`: the fleet fell behind its offered load: 56% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 54% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a8-r2`: the fleet fell behind its offered load: 55% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 47% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a8-r3`: the fleet fell behind its offered load: 57% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 54% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a10-r1`: the fleet fell behind its offered load: 73% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 55% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a10-r2`: the fleet fell behind its offered load: 76% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 53% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a10-r3`: the fleet fell behind its offered load: 73% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 53% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a12-r1`: the fleet fell behind its offered load: 81% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 53% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a12-r2`: the fleet fell behind its offered load: 80% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 54% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a12-r3`: the fleet fell behind its offered load: 80% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 51% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a14-r1`: the fleet fell behind its offered load: 86% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 52% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a14-r2`: the fleet fell behind its offered load: 85% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 51% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a14-r3`: the fleet fell behind its offered load: 86% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 52% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a16-r1`: the fleet fell behind its offered load: 91% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 52% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a16-r2`: the fleet fell behind its offered load: 91% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 51% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a16-r3`: the fleet fell behind its offered load: 93% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 52% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a20-r1`: the fleet fell behind its offered load: 99% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 51% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a20-r2`: the fleet fell behind its offered load: 97% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 54% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a20-r3`: the fleet fell behind its offered load: 98% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 52% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a8-r1`: the fleet fell behind its offered load: 58% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 51% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a8-r2`: the fleet fell behind its offered load: 54% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 53% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a8-r3`: the fleet fell behind its offered load: 56% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 55% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `session_affinity-a16-r1`: the fleet fell behind its offered load: 38% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 79% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `session_affinity-a16-r2`: the fleet fell behind its offered load: 32% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 43% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `session_affinity-a16-r3`: the fleet fell behind its offered load: 28% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 1009% slower early than late, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `session_affinity-a20-r1`: the fleet fell behind its offered load: 49% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 62% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `session_affinity-a20-r2`: the fleet fell behind its offered load: 43% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 52% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `session_affinity-a20-r3`: the fleet fell behind its offered load: 48% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 56% slower late than early, compared within each of 4 turn indices. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run

Excluded from every figure above — §6 discards these rather than averaging them in:

- `least_outstanding-a6-r2`: still warming up: TTFT p50 was 33% slower early in the measured window than late, compared within each of 4 turn indices, over a 25% threshold. Lengthen the warm-up and re-run
- `least_outstanding-c128-r1`: still warming up: the first half of the measured window was 46% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c128-r2`: still warming up: the first half of the measured window was 53% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c64-r3`: still warming up: the first half of the measured window was 36% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c8-r3`: still warming up: the first half of the measured window was 53% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-a10-r3`: still warming up: TTFT p50 was 49% slower early in the measured window than late, compared within each of 4 turn indices, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-a14-r2`: still warming up: TTFT p50 was 137% slower early in the measured window than late, compared within each of 4 turn indices, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c256-r1`: still warming up: the first half of the measured window was 359% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c256-r2`: still warming up: the first half of the measured window was 252% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c256-r3`: still warming up: the first half of the measured window was 78% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
