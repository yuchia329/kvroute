# Chaos and recovery — 2026-09-11

What a fleet does when one of its replicas is killed under steady load, and what the router's
failure handling is worth in goodput. Ticket #19, criteria 1–8.

Four runs, all on the five-replica fleet (GPU 3 is out of the fleet, #25), all offering the same
bytes at the same load:

| run | policy | fault | directory |
|---|---|---|---|
| kill, session affinity | `session_affinity` | SIGKILL to replica-2 and its engine | [`kill-session_affinity/`](kill-session_affinity/) |
| kill, prefix affinity, spill rule **off** | `prefix_affinity` | the same kill | [`kill-prefix_affinity-spilloff/`](kill-prefix_affinity-spilloff/) |
| kill, prefix affinity at the settled spill point | `prefix_affinity`, KV high-water 0, load imbalance factor 2 | the same kill | [`kill-prefix_affinity/`](kill-prefix_affinity/) |
| drain, prefix affinity at the settled spill point | `prefix_affinity`, KV high-water 0, load imbalance factor 2 | drained through the router, then stopped | [`drain-prefix_affinity/`](drain-prefix_affinity/) |

The comparison of the two curves criterion 8 asks for is
[`recovery-kill-spilloff.md`](recovery-kill-spilloff.md): session affinity against prefix affinity
with the spill rule off. [`recovery-kill.md`](recovery-kill.md) is the same comparison against the
spill-on arm, which turned out to measure the spill rule rather than the fault — see "Why prefix
affinity ran twice".

## How it was measured

Open-loop at 6 req/s with a 5 s think time, offering the frozen multiturn workload
(`sessions=307, skew=0, turns=4, prompt=448t, output=64t, system=0.3x128t, branch=0.3x8fam1t, seed=1`)
against the SLO derived from the 2026-09-07 characterization: TTFT < 990 ms, inter-token p50 < 24 ms.
Each run is 300 s with a 50 s warm-up; replica-2 is killed 100 s in and started again at 160 s;
goodput is bucketed every 5 s by when each request was offered and read from the fault.

The fleet was brought down and up between runs, so no run found another's conversations in the
replicas' caches (ADR-0004). The router was the committed build at `e221eca`, carrying #19's health
checks, drain and reroute, #20's `/metrics` and #24's exact residency; the chaos and recovery
binaries for the spill-off run were `89442ef`. Prometheus scraped the router and every replica every
5 s throughout, equally for all four runs.

**Why 6 req/s.** `runs/definitive` measures both affinity policies open-loop on this fleet: at
8 req/s session affinity already misses the SLO on about 5% of requests (7.56/s of 8 offered,
7.26–7.89 across three repetitions) and prefix affinity on about 1%, while at 6 req/s both hold
(5.97/s and 5.98/s). A rate whose baseline already misses is a rate whose dip cannot be read.

## What the router did

| | session affinity | prefix affinity, spill off | prefix affinity, spill on | drain |
|---|---:|---:|---:|---:|
| out of rotation | +0.50 s, ejected | +0.40 s, ejected | +0.50 s, ejected | +0.10 s, draining |
| stopped | +0.63 s | +0.63 s | +0.65 s | +1.54 s, drained at +0.51 s |
| restarted | +110.88 s | +110.94 s | +110.86 s | +111.00 s |
| back in rotation | +111.00 s, readmitted | +112.00 s, readmitted | +112.00 s, readmitted | +111.10 s, restored |
| measured requests | 1501 | 1501 | 1501 | 1501 |
| dropped mid-stream | 1 | 1 | 1 | **0** |
| dropped, never placed | 0 | 0 | 0 | 0 |
| rerouted before a first token | 0 | 1 | 2 | 0 |
| failed with an error | 0 | 0 | 0 | 0 |
| outside the SLO | 8 | 44 | 184 | 235 |

Nothing was restarted but the replica itself: no router restart, no operator action, and the replica
was back in rotation about a second after it began answering again, on two passed health checks.

## The recovery curves

Session affinity against prefix affinity with the spill rule off, from
[`recovery-kill-spilloff.md`](recovery-kill-spilloff.md):

| | session affinity | prefix affinity, spill off |
|---|---:|---:|
| goodput before the fault | 5.98/s | 5.98/s |
| lowest from the fault on | 5.20/s at +110 s | 3.00/s at +70 s |
| deficit | 7 requests | 43 requests |
| back within 10% of the baseline for good | +115 s | +80 s |

The two policies lose goodput at opposite moments, and for opposite reasons.

**Session affinity pays when the replica comes back.** Its curve is flat through the whole outage —
four replicas at 6 req/s is inside what the fleet can serve — and its one dip is the bucket at
+110 s, where the replica is readmitted: 4 requests missed, every one of them on replica-2, with
0.3% of their prompts cached. That is the ring moving those sessions back onto an empty cache, the
second dip the ticket predicted. It costs 7 requests in all.

**Prefix affinity with the rule off pays during the outage.** Its dip is at +55 to +80 s, nowhere
near either the kill or the return. Through it, one replica carried 47% of the fleet's requests
against a 25% fair share among the four survivors, every missed request was on that replica, and
their prompts were 70–84% cached — a queue on one replica, not recomputation. Load share by window:

| | before the kill | outage, to +55 s | the dip, +55–80 s | outage, +80 s on | replica back |
|---|---|---|---|---|---|
| prefix affinity, spill off | busiest 30% | busiest 34% | **busiest 47%**, 39 missed | busiest 34% | busiest 33% |
| session affinity | busiest 25% | busiest 29% | busiest 33%, none missed | busiest 36% | busiest 24% |

The conversations orphaned by the kill landed together: replica-0 went from 19% of the load before
the fault to 34% after it, and to 47% while the orphans' later turns arrived in a wave. With the
spill rule off nothing moves a turn away from the replica holding its conversation, so the queue
stands until those conversations end — which is why it clears at +80 s, before the replica returns,
and why prefix affinity has no dip at all when it does return: it feeds the empty replica only as
new conversations arrive (11% of the load), where session affinity's hash hands it a full share at
once (16%).

Neither policy is "better at recovering" on one run each. What the pair shows is where each one's
cost falls: session affinity's on re-admission, prefix affinity's during the outage.

## Why prefix affinity ran twice

The first prefix affinity arm ran at `bench.Chosen` (KV high-water 0, load imbalance factor 2), the
spill point every later measurement of policy 4 is supposed to use. At this load that rule fired on
**19% of later turns** — against 0.674% at the closed-loop 32-user rung it was settled at — because
it compares inflight counts that are small integers when the fleet holds ~12 requests. It cost:

| decision | requests | missed the SLO | prompt tokens cached | TTFT p50 | TTFT p90 |
|---|---:|---:|---:|---:|---:|
| `PREFIX_AFFINITY` | 1134 | 7.6% | 73.9% | 372 ms | 845 ms |
| `SPILL_LOAD` | 226 | **42.0%** | 11.9% | 970 ms | 1688 ms |
| `COLD` | 140 | 2.1% | 2.1% | 297 ms | 418 ms |

Its baseline was 5.33/s against session affinity's 5.98/s on the same fleet in the same hour, and it
missed the SLO in bursts every 20 s as the workload's conversations took their later turns together
— before the kill as much as after. A curve like that measures the rule, not the fault, so the arm
was re-run with the rule off, which is how `runs/definitive` measured prefix affinity at this same
load point (5.98/s, 5.95–5.99, n=3). The spill-on run is kept here as the evidence for issue #31,
and its comparison is [`recovery-kill.md`](recovery-kill.md).

## The drain

Draining replica-2 through the router and then stopping it **dropped nothing**: 0 of 1501 measured
requests, against 1 for each kill. The router took it out of rotation 0.10 s after the request, its
last in-flight request finished 0.51 s in, and it was stopped a second later. That is the difference
between an operator taking a replica away and a replica dying, in one number each.

The drain ran at the settled spill point, so its goodput figures carry the same confound as the
spill-on kill run and are not comparable with the others. Its drop count is not affected by it.

## Two things to know about these numbers

**The first reports were wrong, and every record here is re-derived from the rows.** The load driver
keeps its own clock, started a fraction of a millisecond after the run's, and its truncated arrival
interval fits an 1,801st request into a 300 s run at 6 req/s. That request landed just past the
run's recorded end and was given a curve bucket of its own — one request across five seconds, which
read as goodput falling to 0.2/s as the run finished. It became both policies' reported trough, left
both reading "not recovered", and was 29 of session affinity's 36-request deficit. Fixed in
`89442ef`; the rows are unchanged, and the records and reports here are re-derived from them.

**One run per arm.** Every other table in this project is a median of three; these are single runs,
because each costs a fleet cycle. Treat a difference smaller than the bucket-to-bucket spread within
one run as noise. The 7-against-43-request deficit is larger than that spread; the 0.4 s against
0.5 s ejection times are not.

## Evidence

Each run directory holds `rows.jsonl` (one row per request: the routing decision, the engine's own
account of the prompt tokens it had cached, the outcome), `run.json` (the plan, the events, the
summary, the curve) and `report.md`. Under [`evidence/`](evidence/) are the router's own per-request
records and logs for each run, the run logs, and the two box scripts the runs were driven by —
`make chaos` cannot run on the fleet host, which has no Go toolchain.
