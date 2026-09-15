# Chaos: replica-2 killed under session_affinity

Open-loop at 6 req/s with a 5s think time, offering multiturn(sessions=307,skew=0,turns=4,prompt=448t,output=64t,system=0.3x128t,branch=0.3x8fam1t,bpt=4,seed=1,usage=on). SLO: TTFT < 990ms, inter-token p50 < 24ms. The fault came 1m40s in, after a 50s warm-up, and the replica was started again 2m40s in. Goodput is bucketed every 5s by when requests were offered, and read from the fault.

## What happened to replica-2

| from the fault | event | detail |
|---:|---|---|
| +0.00s | fault injected | killed |
| +0.50s | out of rotation | ejected |
| +0.63s | stopped |  |
| +60.00s | restart begun |  |
| +110.88s | restarted |  |
| +111.00s | back in rotation | readmitted |

## Requests

1 of 1501 measured requests were dropped — 1 was streaming when their replica was lost, and 0 were never placed — and 0 were rerouted before their first token. A reroute is transparent only because nothing had reached its client when replica-2 died. A request already streaming at that instant cannot be rerouted and is counted as dropped, so the drop count reflects how many requests happened to be mid-stream when the replica was killed, and the reroute count is what the router saved.

Of the rest, 1500 succeeded, 8 of them outside the SLO; 0 failed with an error their replica answered with; 0 were cancelled by their client.

## Recovery

- Baseline goodput before the fault: 5.98/s
- Lowest from the fault on: 5.20/s, in the bucket at +110.00s
- Deficit: 7 requests that would have met the SLO at the baseline did not
- Back within 10% of the baseline from +115.00s, and there to the end of the run

## Curve

| from the fault | offered | goodput/s | rerouted | dropped | failed | SLO violations |
|---:|---:|---:|---:|---:|---:|---:|
| -50.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -45.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -40.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -35.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -30.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -25.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -20.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -15.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| -10.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| -5.00s | 30 | 5.80 | 0 | 1 | 0 | 0 |
| +0.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +5.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| +10.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +15.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +20.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +25.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +30.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +35.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +40.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +45.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +50.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +55.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +60.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +65.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +70.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +75.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +80.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +85.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +90.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +95.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +100.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +105.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +110.00s | 30 | 5.20 | 0 | 0 | 0 | 4 |
| +115.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +120.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +125.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +130.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +135.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +140.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +145.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +150.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +155.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +160.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +165.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +170.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| +175.00s | 30 | 5.80 | 0 | 0 | 0 | 1 |
| +180.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +185.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +190.00s | 30 | 6.00 | 0 | 0 | 0 | 0 |
| +195.00s | 31 | 6.20 | 0 | 0 | 0 | 0 |
