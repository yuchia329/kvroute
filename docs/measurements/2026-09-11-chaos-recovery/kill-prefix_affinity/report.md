# Chaos: replica-2 killed under prefix_affinity

Open-loop at 6 req/s with a 5s think time, offering multiturn(sessions=307,skew=0,turns=4,prompt=448t,output=64t,system=0.3x128t,branch=0.3x8fam1t,bpt=4,seed=1,usage=on). SLO: TTFT < 990ms, inter-token p50 < 24ms. The fault came 1m40s in, after a 50s warm-up, and the replica was started again 2m40s in. Goodput is bucketed every 5s by when requests were offered, and read from the fault.
Spill point: KV high-water 0, load imbalance factor 2.

## What happened to replica-2

| from the fault | event | detail |
|---:|---|---|
| +0.00s | fault injected | killed |
| +0.50s | out of rotation | ejected |
| +0.65s | stopped |  |
| +60.00s | restart begun |  |
| +110.86s | restarted |  |
| +112.00s | back in rotation | readmitted |

## Requests

1 of 1501 measured requests were dropped — 1 was streaming when their replica was lost, and 0 were never placed — and 2 were rerouted before their first token. A reroute is transparent only because nothing had reached its client when replica-2 died. A request already streaming at that instant cannot be rerouted and is counted as dropped, so the drop count reflects how many requests happened to be mid-stream when the replica was killed, and the reroute count is what the router saved.

Of the rest, 1500 succeeded, 184 of them outside the SLO; 0 failed with an error their replica answered with; 0 were cancelled by their client.

## Recovery

- Baseline goodput before the fault: 5.33/s
- Lowest from the fault on: 2.00/s, in the bucket at +35.00s
- Deficit: 92 requests that would have met the SLO at the baseline did not
- Back within 10% of the baseline from +160.00s, and there to the end of the run

## Curve

| from the fault | offered | goodput/s | rerouted | dropped | failed | SLO violations |
|---:|---:|---:|---:|---:|---:|---:|
| -50.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -45.00s | 30 | 3.00 | 0 | 0 | 0 | 15 |
| -40.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -35.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -30.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -25.00s | 30 | 4.00 | 0 | 0 | 0 | 10 |
| -20.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| -15.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -10.00s | 30 | 5.20 | 0 | 0 | 0 | 4 |
| -5.00s | 30 | 4.00 | 2 | 1 | 0 | 9 |
| +0.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| +5.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +10.00s | 30 | 5.20 | 0 | 0 | 0 | 4 |
| +15.00s | 30 | 3.80 | 0 | 0 | 0 | 11 |
| +20.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| +25.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +30.00s | 30 | 4.60 | 0 | 0 | 0 | 7 |
| +35.00s | 30 | 2.00 | 0 | 0 | 0 | 20 |
| +40.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +45.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +50.00s | 30 | 4.40 | 0 | 0 | 0 | 8 |
| +55.00s | 30 | 2.20 | 0 | 0 | 0 | 19 |
| +60.00s | 30 | 5.20 | 0 | 0 | 0 | 4 |
| +65.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +70.00s | 30 | 4.80 | 0 | 0 | 0 | 6 |
| +75.00s | 30 | 4.20 | 0 | 0 | 0 | 9 |
| +80.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +85.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +90.00s | 30 | 4.80 | 0 | 0 | 0 | 6 |
| +95.00s | 30 | 5.00 | 0 | 0 | 0 | 5 |
| +100.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +105.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +110.00s | 30 | 4.40 | 0 | 0 | 0 | 8 |
| +115.00s | 30 | 2.80 | 0 | 0 | 0 | 16 |
| +120.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +125.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +130.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +135.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +140.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +145.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +150.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +155.00s | 30 | 4.60 | 0 | 0 | 0 | 7 |
| +160.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +165.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +170.00s | 30 | 5.40 | 0 | 0 | 0 | 3 |
| +175.00s | 30 | 5.40 | 0 | 0 | 0 | 3 |
| +180.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +185.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +190.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| +195.00s | 31 | 5.00 | 0 | 0 | 0 | 6 |
