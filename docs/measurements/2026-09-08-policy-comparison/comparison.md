# Policy comparison — goodput against the derived SLO

SLO: TTFT < 990ms, inter-token p50 < 24ms. Goodput is requests per second that met it,
so a policy that completed more requests can still score lower.

Workload, the same for every cell here — both policies sent the same bytes at the same
load point, which is what makes them comparable at all:

    fixed(prompt=2048B,output=64t,seed=1)

Each figure is the median of that cell's repetitions, with the range across them. A
difference smaller than those ranges is a difference between a policy and itself.

Prompt bytes per token: — . The engines' prompt-token counters were not read over these
cells, so the prefix match figures in the rows cannot be converted into tokens.

| driver | load | round_robin | least_outstanding | Δ least_outstanding vs round_robin |
|---|---:|---:|---:|---:|
| closed-loop | 1 users | 1.30 (1.30–1.31, n=3) | 1.28 (1.26–1.34, n=3) | -1.7% (within spread) |
| closed-loop | 4 users | 5.01 (4.98–5.08, n=3) | 4.99 (4.93–5.08, n=3) | -0.3% (within spread) |
| closed-loop | 8 users | 7.50 (7.48–7.58, n=3) | 8.01 (7.92–8.03, n=3) | +6.8% |
| closed-loop | 16 users | 10.34 (10.31–10.35, n=3) | 10.56 (10.52–10.67, n=3) | +2.2% |
| closed-loop | 32 users | 12.34 (12.33–12.37, n=3) | 10.28 (10.28–10.57, n=2) | -16.7% |
| closed-loop | 64 users | 9.80 (9.78–9.84, n=3) | 8.21 (8.17–9.31, n=3) | -16.2% |
| closed-loop | 128 users | 9.44 (9.42–9.47, n=3) | 6.41 (5.53–6.64, n=3) | -32.1% |
| closed-loop | 256 users | — | 0.37 (0.35–0.50, n=3) | — |
| open-loop | 4 req/s | 4.00 (4.00–4.00, n=3) | 4.00 (4.00–4.00, n=3) | +0.0% (within spread) |
| open-loop | 8 req/s | 8.00 (8.00–8.00, n=3) | 8.00 (8.00–8.00, n=3) | +0.0% (within spread) |
| open-loop | 12 req/s | 12.00 (12.00–12.00, n=3) | 11.19 (10.72–11.39, n=3) | -6.8% |
| open-loop | 16 req/s | 2.69 (0.02–3.46, n=3) ⚠ | 0.05 (0.02–0.37, n=3) ⚠ | -98.3% (within spread) ⚠ |
| open-loop | 24 req/s | 0.00 (0.00–0.00, n=3) ⚠ | 0.00 (0.00–0.00, n=3) ⚠ | +0.00/s over a baseline of zero ⚠ |
| open-loop | 32 req/s | 0.00 (0.00–0.00, n=3) ⚠ | 0.00 (0.00–0.00, n=3) ⚠ | +0.00/s over a baseline of zero ⚠ |
| open-loop | 48 req/s | 0.00 (0.00–0.00, n=3) ⚠ | 0.00 (0.00–0.00, n=3) ⚠ | +0.00/s over a baseline of zero ⚠ |

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
| closed-loop | 1 users | round_robin | 325ms | — | 335ms | — | 255 | — | — | — | — |
| closed-loop | 1 users | least_outstanding | 325ms | — | 335ms | — | 252 | — | — | — | — |
| closed-loop | 4 users | round_robin | 332ms | — | 383ms | — | 988 | — | — | — | — |
| closed-loop | 4 users | least_outstanding | 332ms | — | 387ms | — | 983 | — | — | — | — |
| closed-loop | 8 users | round_robin | 364ms | — | 470ms | — | 1480 | — | — | — | — |
| closed-loop | 8 users | least_outstanding | 360ms | — | 694ms | — | 1576 | — | — | — | — |
| closed-loop | 16 users | round_robin | 371ms | — | 461ms | — | 2041 | — | — | — | — |
| closed-loop | 16 users | least_outstanding | 386ms | — | 1041ms | — | 2131 | — | — | — | — |
| closed-loop | 32 users | round_robin | 372ms | — | 540ms | — | 2439 | — | — | — | — |
| closed-loop | 32 users | least_outstanding | 542ms | — | 1602ms | — | 1726 | — | — | — | — |
| closed-loop | 64 users | round_robin | 371ms | — | 1202ms | — | 2368 | — | — | — | — |
| closed-loop | 64 users | least_outstanding | 874ms | — | 2567ms | — | 2853 | — | — | — | — |
| closed-loop | 128 users | round_robin | 371ms | — | 8678ms | — | 2609 | — | — | — | — |
| closed-loop | 128 users | least_outstanding | 1077ms | — | 2788ms | — | 2940 | — | — | — | — |
| closed-loop | 256 users | round_robin | — | — | — | — | — | — | — | — | — |
| closed-loop | 256 users | least_outstanding | 1570ms | — | 3195ms | — | 2891 | — | — | — | — |
| open-loop | 4 req/s | round_robin | 332ms | — | 347ms | — | 780 | — | — | — | — |
| open-loop | 4 req/s | least_outstanding | 331ms | — | 352ms | — | 780 | — | — | — | — |
| open-loop | 8 req/s | round_robin | 366ms | — | 481ms | — | 1560 | — | — | — | — |
| open-loop | 8 req/s | least_outstanding | 360ms | — | 637ms | — | 1560 | — | — | — | — |
| open-loop | 12 req/s | round_robin | 371ms | — | 495ms | — | 2343 | — | — | — | — |
| open-loop | 12 req/s | least_outstanding | 525ms | — | 1230ms | — | 2343 | — | — | — | — |
| open-loop | 16 req/s | round_robin | — | — | — | — | 3120 | — | — | — | — |
| open-loop | 16 req/s | least_outstanding | — | — | — | — | 3120 | — | — | — | — |
| open-loop | 24 req/s | round_robin | — | — | — | — | 4683 | — | — | — | — |
| open-loop | 24 req/s | least_outstanding | — | — | — | — | 4683 | — | — | — | — |
| open-loop | 32 req/s | round_robin | — | — | — | — | 6240 | — | — | — | — |
| open-loop | 32 req/s | least_outstanding | — | — | — | — | 6240 | — | — | — | — |
| open-loop | 48 req/s | round_robin | — | — | — | — | 9363 | — | — | — | — |
| open-loop | 48 req/s | least_outstanding | — | — | — | — | 9363 | — | — | — | — |

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

- `least_outstanding-a16-r1`: the fleet fell behind its offered load: 58% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 57% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a16-r2`: the fleet fell behind its offered load: 53% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 58% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a16-r3`: the fleet fell behind its offered load: 51% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 57% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a24-r1`: the fleet fell behind its offered load: 86% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 49% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a24-r2`: the fleet fell behind its offered load: 85% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 50% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a24-r3`: the fleet fell behind its offered load: 84% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 49% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a32-r1`: the fleet fell behind its offered load: 97% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 47% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a32-r2`: the fleet fell behind its offered load: 97% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 47% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a32-r3`: the fleet fell behind its offered load: 96% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 47% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a48-r1`: the fleet fell behind its offered load: 100% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 46% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a48-r2`: the fleet fell behind its offered load: 100% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 46% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `least_outstanding-a48-r3`: the fleet fell behind its offered load: 100% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 46% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a16-r1`: the fleet fell behind its offered load: 57% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 64% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a16-r2`: the fleet fell behind its offered load: 40% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 53% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a16-r3`: the fleet fell behind its offered load: 43% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 64% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a24-r1`: the fleet fell behind its offered load: 84% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 50% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a24-r2`: the fleet fell behind its offered load: 83% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 50% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a24-r3`: the fleet fell behind its offered load: 83% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 50% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a32-r1`: the fleet fell behind its offered load: 96% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 48% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a32-r2`: the fleet fell behind its offered load: 96% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 48% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a32-r3`: the fleet fell behind its offered load: 95% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 48% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a48-r1`: the fleet fell behind its offered load: 100% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 46% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a48-r2`: the fleet fell behind its offered load: 100% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 46% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run
- `round_robin-a48-r3`: the fleet fell behind its offered load: 100% of the requests offered were still unanswered when arrivals stopped, over a 20% backlog, and TTFT p50 was 46% slower late than early, compared over every success rather than within each turn index. The fleet was offered more than it can serve, so this cell's latency percentiles are a transient and are not pooled; its goodput is the result, and there is nothing to re-run

Excluded from every figure above — §6 discards these rather than averaging them in:

- `least_outstanding-c32-r2`: the fleet slowed across the measured window: TTFT p50 was 37% slower late than early, compared over every success rather than within each turn index, over a 25% threshold. A longer warm-up is not the fix: this cell's latency percentiles are a transient rather than a steady state. Look for a queue that never settled — an open-loop cell offered more than the fleet can serve never reaches one — or a throttled card, a replica lost, or a cache growing
- `round_robin-c256-r1`: still warming up: TTFT p50 was 36% slower early in the measured window than late, compared over every success rather than within each turn index, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c256-r2`: still warming up: TTFT p50 was 59% slower early in the measured window than late, compared over every success rather than within each turn index, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c256-r3`: still warming up: TTFT p50 was 33% slower early in the measured window than late, compared over every success rather than within each turn index, over a 25% threshold. Lengthen the warm-up and re-run
