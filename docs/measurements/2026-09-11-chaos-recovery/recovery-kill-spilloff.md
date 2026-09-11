# Recovery: replica-2 killed, session_affinity against prefix_affinity

Open-loop at 6 req/s with a 5s think time, offering multiturn(sessions=307,skew=0,turns=4,prompt=448t,output=64t,system=0.3x128t,branch=0.3x8fam1t,bpt=4,seed=1,usage=on). SLO: TTFT < 990ms, inter-token p50 < 24ms. The fault came 1m40s in, after a 50s warm-up, and the replica was started again 2m40s in. Goodput is bucketed every 5s by when requests were offered, and read from the fault.

| | session_affinity | prefix_affinity |
|---|---:|---:|
| left rotation | +0.50s | +0.40s |
| back in rotation | +111.00s | +112.00s |
| goodput before the fault | 5.98/s | 5.98/s |
| lowest from the fault on | 5.20/s at +110.00s | 3.00/s at +70.00s |
| deficit | 7 requests | 43 requests |
| back within 10% for good | from +115.00s | from +80.00s |
| rerouted | 0 | 1 |
| dropped mid-stream | 1 | 1 |
| dropped, never placed | 0 | 0 |

## Drops

- **session_affinity**: 1 of 1501 measured requests were dropped — 1 was streaming when their replica was lost, and 0 were never placed — and 0 were rerouted before their first token. A reroute is transparent only because nothing had reached its client when replica-2 died. A request already streaming at that instant cannot be rerouted and is counted as dropped, so the drop count reflects how many requests happened to be mid-stream when the replica was killed, and the reroute count is what the router saved.
- **prefix_affinity**: 1 of 1501 measured requests were dropped — 1 was streaming when their replica was lost, and 0 were never placed — and 1 was rerouted before their first token. A reroute is transparent only because nothing had reached its client when replica-2 died. A request already streaming at that instant cannot be rerouted and is counted as dropped, so the drop count reflects how many requests happened to be mid-stream when the replica was killed, and the reroute count is what the router saved.

## Curves

| from the fault | session_affinity goodput/s | prefix_affinity goodput/s |
|---:|---:|---:|
| -50.00s | 6.00 | 6.00 |
| -45.00s | 6.00 | 5.80 |
| -40.00s | 6.00 | 6.00 |
| -35.00s | 6.00 | 6.00 |
| -30.00s | 6.00 | 6.00 |
| -25.00s | 6.00 | 6.00 |
| -20.00s | 6.00 | 6.00 |
| -15.00s | 5.80 | 6.00 |
| -10.00s | 6.00 | 6.00 |
| -5.00s | 5.80 | 6.00 |
| +0.00s | 6.00 | 5.80 |
| +5.00s | 5.80 | 5.80 |
| +10.00s | 6.00 | 6.00 |
| +15.00s | 6.00 | 6.00 |
| +20.00s | 6.00 | 6.00 |
| +25.00s | 6.00 | 6.00 |
| +30.00s | 6.00 | 6.00 |
| +35.00s | 6.00 | 6.00 |
| +40.00s | 6.00 | 6.00 |
| +45.00s | 6.00 | 6.00 |
| +50.00s | 6.00 | 6.00 |
| +55.00s | 6.00 | 5.60 |
| +60.00s | 6.00 | 5.60 |
| +65.00s | 6.00 | 5.00 |
| +70.00s | 6.00 | 3.00 |
| +75.00s | 6.00 | 3.00 |
| +80.00s | 6.00 | 5.40 |
| +85.00s | 6.00 | 6.00 |
| +90.00s | 6.00 | 6.00 |
| +95.00s | 6.00 | 6.00 |
| +100.00s | 6.00 | 6.00 |
| +105.00s | 6.00 | 6.00 |
| +110.00s | 5.20 | 6.00 |
| +115.00s | 6.00 | 6.00 |
| +120.00s | 6.00 | 6.00 |
| +125.00s | 6.00 | 6.00 |
| +130.00s | 6.00 | 6.00 |
| +135.00s | 6.00 | 6.00 |
| +140.00s | 6.00 | 6.00 |
| +145.00s | 6.00 | 6.00 |
| +150.00s | 6.00 | 6.00 |
| +155.00s | 6.00 | 6.00 |
| +160.00s | 6.00 | 6.00 |
| +165.00s | 6.00 | 6.00 |
| +170.00s | 5.80 | 6.00 |
| +175.00s | 5.80 | 6.00 |
| +180.00s | 6.00 | 6.00 |
| +185.00s | 6.00 | 6.00 |
| +190.00s | 6.00 | 6.00 |
| +195.00s | 6.20 | 6.20 |
