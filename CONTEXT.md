# kvroute

A KV-cache-aware inference router: it fronts a fleet of single-GPU vLLM replicas and decides
which replica should serve each request, trading prefix-cache locality against load balance. The
deliverable is a measured comparison of routing policies, not a service.

## Language

### Fleet

**Replica**:
One vLLM process serving one model on one GPU. Replicas are interchangeable in capability and
differ only in what they currently hold in cache and how loaded they are.
_Avoid_: worker, backend, node, instance

**Router**:
The Go process that is the sole ingress to the fleet. Because it is the only ingress, it knows
exactly what it has dispatched.
_Avoid_: proxy, gateway, load balancer

**Replica symmetry**:
The property that all replicas deliver equal latency under equal load. It is a claim about the
host, not the GPUs: identical cards can still differ through NUMA placement and cores available
per GPU. Verified before the policy comparison is trusted, because asymmetry would leak topology
into the result.
_Avoid_: fairness, balance, uniformity

### Conversation state

**Session**:
A multi-turn conversation, identified by the `X-Session-Id` header the client supplies. Turns of
one session share a growing prefix; the session is the unit that cache locality is preserved
for.
_Avoid_: conversation, chat, thread, dialogue

**Turn**:
One request/response exchange within a session. Turn N resends the full history of turns 1..N-1,
which is why prefixes are shared.
_Avoid_: message, exchange, round

**Prefix block**:
A fixed-size chunk of the rendered prompt, hashed to form the unit of cache-locality tracking.
The router chunks by bytes, not tokens, so blocks are self-consistent within the router but do
not align with vLLM's internal blocks.
_Avoid_: chunk, segment, page

**Prefix index**:
The router's trie mapping prefix block hash to the set of replicas believed to hold that block.
It is a belief about state the router does not own, and it decays as replicas evict.
_Avoid_: prefix cache, radix tree, cache map

**Prefix match**:
The length of the longest chain of leading prefix blocks a candidate replica is believed to
hold. Measured in bytes, and reported as such.
_Avoid_: prefix hit, cache hit (those mean the vLLM-side quantity, below)

**Prefix cache hit rate**:
vLLM's own reported figure, scraped from a replica. This is ground truth; prefix match is the
router's prediction of it.
_Avoid_: hit rate (unqualified)

**Belief divergence**:
The gap between the router's prefix match prediction and the replica's actual prefix cache hit
for the same request.
_Avoid_: cache miss, staleness, drift

**KV cache event**:
A notification published by a replica when it stores or removes a block. Consuming the stream
gives exact residency rather than a belief, at the cost of depending on engine cooperation.
_Avoid_: cache notification, invalidation

### Load

**Inflight**:
The count of requests the router has dispatched to a replica and not yet seen complete. Counted
locally and exactly, never scraped. Includes both requests vLLM is running and requests vLLM has
queued.
_Avoid_: queue depth, outstanding, load, concurrency

**KV utilization**:
The fraction of a replica's KV cache blocks currently allocated, scraped from that replica. The
one load signal the router cannot derive locally.
_Avoid_: memory pressure, cache usage

### Routing

**Policy**:
A pluggable rule mapping a request plus fleet state to a chosen replica. Four exist; only the
policy varies between benchmark runs.
_Avoid_: strategy, algorithm, scheduler

**Spill**:
The router's decision to decline the best prefix match and route elsewhere because that replica
is under KV or load pressure. A router-layer decision only.
_Avoid_: preemption, eviction, overflow — vLLM independently preempts and swaps sequences under
its own KV pressure, which is a different thing at a different layer. Never call that spilling.

**Affinity**:
The router's decision to route to the replica with the best prefix match.
_Avoid_: stickiness, pinning

### Measurement

**Goodput**:
Requests per second that met the SLO. The primary metric. Distinct from throughput, which counts
requests that completed regardless of how badly.
_Avoid_: throughput, RPS, QPS

**Redundant prefill**:
Prompt tokens a replica had to compute because it did not hold them, which some other replica did
hold. The physical work prefix affinity exists to eliminate, and therefore the measurement of the
mechanism rather than of the outcome.
_Avoid_: wasted prefill, recompute, duplicate work

**Router overhead**:
Time from the router accepting a request to dispatching it upstream. Distinct from any latency
the fleet contributes, and reported separately so the router's own cost is never hidden inside
TTFT.
_Avoid_: routing latency, proxy overhead

**SLO**:
The per-request pass/fail threshold on TTFT and inter-token latency, derived from the measured
concurrency-1 floor rather than chosen a priori.
_Avoid_: target, budget, threshold (unqualified)

**Cell**:
One benchmark data point: a fixed (policy, concurrency or arrival rate, working set ratio,
repetition). Cells are cached so a sweep can resume, and each carries its own contamination
evidence.
_Avoid_: run, trial, sample

**Clean cell**:
A cell during which no process outside the experiment held memory on any of the six GPUs. The
box is shared, so cleanliness is recorded per cell and unclean cells are discarded and re-run,
never averaged in.
_Avoid_: valid run, good sample

**Closed-loop driver**:
The load generator that holds a fixed number of virtual users, each sending its next request only
after the previous response completes. Offered load is an outcome. Used for the concurrency
sweep.
_Avoid_: concurrency driver, worker pool

**Open-loop driver**:
The load generator that fires requests on a fixed arrival schedule regardless of whether earlier
requests have finished. Offered load is an input. Used for the headline goodput number, because
a closed-loop driver throttles itself when the fleet slows and so understates the tail.
_Avoid_: rate driver, Poisson driver

**Sweep**:
A set of cells varying one axis. Three exist: concurrency, skew, and policy tunables.
_Avoid_: benchmark, experiment (those mean the whole comparison)

**Working set ratio**:
Total session tokens offered divided by aggregate fleet KV capacity. The primary workload axis,
because it determines whether the fleet can hold every session at once and therefore whether
spill ever fires. Written WS.
_Avoid_: load, pressure, session count

**Skew**:
The Zipf parameter governing how unevenly traffic concentrates on a subset of sessions. Held
fixed during the main sweep so that working set ratio is the only workload variable, and varied
only in a small side experiment.
_Avoid_: distribution, hotness, locality

**Dropped request**:
A request that received no complete response because the router could not place it — the failure
the chaos test counts.
_Avoid_: failed, errored

**Failed request**:
A request a replica accepted and then errored on. Counted separately from dropped, and never
folded into latency percentiles.
_Avoid_: error, drop

**SLO violation**:
A request that completed successfully but missed the SLO. Neither dropped nor failed; it is the
quantity goodput excludes.
_Avoid_: miss, slow request

**Cancelled request**:
A request whose client went away before the response finished. No replica errored and the router
placed it fine, so it is neither dropped nor failed: counting it as either would let a client's
behaviour land in the fleet's failure column. A cell containing one is flagged, because a
cancellation means the cell was interrupted rather than run to its end.
_Avoid_: aborted, disconnected, timed out

**Foreign process**:
A process holding memory on one of the fleet's GPUs that the fleet did not start. Ownership is by
ancestry, not by process id: the id recorded for a replica is its API server, and the process
actually holding the card is that server's engine child. A replica left over from an earlier run
is foreign — it is ours, but the fleet the cell is measuring did not start it. Seeing one is what
makes a cell not clean.
_Avoid_: other process, stray process, someone else's job

**Preflight**:
The check that refuses to bring the fleet up while any GPU already holds memory. It is the same
probe that samples for foreign processes during a cell, differing only in that nothing is exempt:
before the fleet exists, every process on a card is contamination whoever started it.
_Avoid_: health check, precheck
