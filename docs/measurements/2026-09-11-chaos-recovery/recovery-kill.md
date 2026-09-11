# Recovery: replica-2 killed, session_affinity against prefix_affinity

Open-loop at 6 req/s with a 5s think time, offering multiturn(sessions=307,skew=0,turns=4,prompt=448t,output=64t,system=0.3x128t,branch=0.3x8fam1t,bpt=4,seed=1,usage=on). SLO: TTFT < 990ms, inter-token p50 < 24ms. The fault came 1m40s in, after a 50s warm-up, and the replica was started again 2m40s in. Goodput is bucketed every 5s by when requests were offered, and read from the fault.

| | session_affinity | prefix_affinity |
|---|---:|---:|
| left rotation | +0.50s | +0.50s |
| back in rotation | +111.00s | +112.00s |
| goodput before the fault | 5.98/s | 5.33/s |
| lowest from the fault on | 5.20/s at +110.00s | 2.00/s at +35.00s |
| deficit | 7 requests | 92 requests |
| back within 10% for good | from +115.00s | from +160.00s |
| rerouted | 0 | 2 |
| dropped mid-stream | 1 | 1 |
| dropped, never placed | 0 | 0 |

## Drops

- **session_affinity**: 1 of 1501 measured requests were dropped — 1 was streaming when their replica was lost, and 0 were never placed — and 0 were rerouted before their first token. A reroute is transparent only because nothing had reached its client when replica-2 died. A request already streaming at that instant cannot be rerouted and is counted as dropped, so the drop count reflects how many requests happened to be mid-stream when the replica was killed, and the reroute count is what the router saved.
- **prefix_affinity**: 1 of 1501 measured requests were dropped — 1 was streaming when their replica was lost, and 0 were never placed — and 2 were rerouted before their first token. A reroute is transparent only because nothing had reached its client when replica-2 died. A request already streaming at that instant cannot be rerouted and is counted as dropped, so the drop count reflects how many requests happened to be mid-stream when the replica was killed, and the reroute count is what the router saved.

## Curves

| from the fault | session_affinity goodput/s | prefix_affinity goodput/s |
|---:|---:|---:|
| -50.00s | 6.00 | 6.00 |
| -45.00s | 6.00 | 3.00 |
| -40.00s | 6.00 | 6.00 |
| -35.00s | 6.00 | 6.00 |
| -30.00s | 6.00 | 6.00 |
| -25.00s | 6.00 | 4.00 |
| -20.00s | 6.00 | 5.80 |
| -15.00s | 5.80 | 6.00 |
| -10.00s | 6.00 | 5.20 |
| -5.00s | 5.80 | 4.00 |
| +0.00s | 6.00 | 5.80 |
| +5.00s | 5.80 | 6.00 |
| +10.00s | 6.00 | 5.20 |
| +15.00s | 6.00 | 3.80 |
| +20.00s | 6.00 | 5.80 |
| +25.00s | 6.00 | 6.00 |
| +30.00s | 6.00 | 4.60 |
| +35.00s | 6.00 | 2.00 |
| +40.00s | 6.00 | 6.00 |
| +45.00s | 6.00 | 6.00 |
| +50.00s | 6.00 | 4.40 |
| +55.00s | 6.00 | 2.20 |
| +60.00s | 6.00 | 5.20 |
| +65.00s | 6.00 | 6.00 |
| +70.00s | 6.00 | 4.80 |
| +75.00s | 6.00 | 4.20 |
| +80.00s | 6.00 | 6.00 |
| +85.00s | 6.00 | 6.00 |
| +90.00s | 6.00 | 4.80 |
| +95.00s | 6.00 | 5.00 |
| +100.00s | 6.00 | 6.00 |
| +105.00s | 6.00 | 6.00 |
| +110.00s | 5.20 | 4.40 |
| +115.00s | 6.00 | 2.80 |
| +120.00s | 6.00 | 6.00 |
| +125.00s | 6.00 | 6.00 |
| +130.00s | 6.00 | 6.00 |
| +135.00s | 6.00 | 6.00 |
| +140.00s | 6.00 | 6.00 |
| +145.00s | 6.00 | 6.00 |
| +150.00s | 6.00 | 6.00 |
| +155.00s | 6.00 | 4.60 |
| +160.00s | 6.00 | 6.00 |
| +165.00s | 6.00 | 6.00 |
| +170.00s | 5.80 | 5.40 |
| +175.00s | 5.80 | 5.40 |
| +180.00s | 6.00 | 6.00 |
| +185.00s | 6.00 | 6.00 |
| +190.00s | 6.00 | 5.80 |
| +195.00s | 6.20 | 5.00 |
