# Chaos: replica-2 drained under prefix_affinity

Open-loop at 6 req/s with a 5s think time, offering multiturn(sessions=307,skew=0,turns=4,prompt=448t,output=64t,system=0.3x128t,branch=0.3x8fam1t,bpt=4,seed=1,usage=on). SLO: TTFT < 990ms, inter-token p50 < 24ms. The fault came 1m40s in, after a 50s warm-up, and the replica was started again 2m40s in. Goodput is bucketed every 5s by when requests were offered, and read from the fault.
Spill point: KV high-water 0, load imbalance factor 2.

## What happened to replica-2

| from the fault | event | detail |
|---:|---|---|
| +0.00s | fault injected | drain requested |
| +0.10s | out of rotation | draining |
| +0.51s | drained |  |
| +1.54s | stopped |  |
| +60.00s | restart begun |  |
| +111.00s | restarted |  |
| +111.10s | back in rotation | restored |

## Requests

0 of 1501 measured requests were dropped and 0 were rerouted before their first token. replica-2 was drained before it was stopped, so every request it was serving finished on it and every request after the drain went elsewhere: the drop count measures the drain, and a kill would not match it.

Of the rest, 1501 succeeded, 235 of them outside the SLO; 0 failed with an error their replica answered with; 0 were cancelled by their client.

## Recovery

- Baseline goodput before the fault: 5.40/s
- Lowest from the fault on: 2.20/s, in the bucket at +55.00s
- Deficit: 145 requests that would have met the SLO at the baseline did not
- Not back within 10% of the baseline for good before the run ended

## Curve

| from the fault | offered | goodput/s | rerouted | dropped | failed | SLO violations |
|---:|---:|---:|---:|---:|---:|---:|
| -50.00s | 30 | 4.40 | 0 | 0 | 0 | 8 |
| -45.00s | 30 | 2.80 | 0 | 0 | 0 | 16 |
| -40.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| -35.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| -30.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -25.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| -20.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -15.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -10.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -5.00s | 30 | 2.80 | 0 | 0 | 0 | 16 |
| +0.00s | 30 | 5.60 | 0 | 0 | 0 | 2 |
| +5.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +10.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| +15.00s | 30 | 4.20 | 0 | 0 | 0 | 9 |
| +20.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +25.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +30.00s | 30 | 4.60 | 0 | 0 | 0 | 7 |
| +35.00s | 30 | 3.60 | 0 | 0 | 0 | 12 |
| +40.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +45.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +50.00s | 30 | 4.80 | 0 | 0 | 0 | 6 |
| +55.00s | 30 | 2.20 | 0 | 0 | 0 | 19 |
| +60.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +65.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +70.00s | 30 | 5.40 | 0 | 0 | 0 | 3 |
| +75.00s | 30 | 3.00 | 0 | 0 | 0 | 15 |
| +80.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| +85.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +90.00s | 30 | 5.20 | 0 | 0 | 0 | 4 |
| +95.00s | 30 | 3.00 | 0 | 0 | 0 | 15 |
| +100.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +105.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +110.00s | 30 | 2.80 | 0 | 0 | 0 | 16 |
| +115.00s | 30 | 2.80 | 0 | 0 | 0 | 16 |
| +120.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| +125.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +130.00s | 30 | 4.80 | 0 | 0 | 0 | 6 |
| +135.00s | 30 | 3.00 | 0 | 0 | 0 | 15 |
| +140.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +145.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| +150.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +155.00s | 30 | 5.40 | 0 | 0 | 0 | 3 |
| +160.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +165.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +170.00s | 30 | 5.00 | 0 | 0 | 0 | 5 |
| +175.00s | 30 | 3.00 | 0 | 0 | 0 | 15 |
| +180.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +185.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +190.00s | 30 | 5.60 | 0 | 0 | 0 | 2 |
| +195.00s | 31 | 2.60 | 0 | 0 | 0 | 18 |
