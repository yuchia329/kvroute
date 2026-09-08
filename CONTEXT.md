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

**Fleet KV capacity**:
The total number of tokens the fleet can hold in KV cache at once, read as `num_gpu_blocks ×
block_size` from every replica's own reported cache configuration and summed. It is a measurement,
never an extrapolation from one replica, because a figure taken from one card and multiplied has
already moved between two bring-ups of identical configuration. Working set ratio is defined
against it, so every point of the pressure grid moves when it does.
_Avoid_: KV size, cache size, memory (those are bytes; this is tokens)

**Replica symmetry**:
The property that all replicas deliver equal latency under equal load. It is a claim about the
host, not the GPUs: identical cards can still differ through NUMA placement and cores available
per GPU. Verified before the policy comparison is trusted, because asymmetry would leak topology
into the result.
_Avoid_: fairness, balance, uniformity

### Conversation state

**Session**:
A multi-turn conversation, identified by the `X-Session-Id` header the client supplies, or by its
own opening where the client supplies none — see session identity below. Turns of one session
share a growing prefix; the session is the unit that cache locality is preserved for.
_Avoid_: conversation, chat, thread, dialogue

**Session identity**:
The answer to which session a request belongs to, and where that answer came from. **Supplied**
identity is the `X-Session-Id` header a client sent; **derived** identity is a hash of the
conversation's opening — its first system and first user message — which every turn resends
unchanged and which therefore needs no client cooperation. The two are recorded separately
because they are different experiments: routing on a supplied key gives a policy a perfect
oracle, and routing on a derived one is what the same policy is worth without one.
_Avoid_: session key, conversation id, session hash

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

**Prompt bytes per token**:
How many bytes of prompt the model's tokenizer turns into one token, measured over a run as the
prompt bytes the harness offered divided by the prompt tokens the engines reported processing.
It is the conversion every byte-denominated figure here has to be read through, because the
router has no tokenizer and reports prefix match in its own bytes. Measured rather than assumed,
and published with every comparison: a byte figure nobody can convert is a byte figure nobody can
check against the engine's own counts. Not to be confused with the KV footprint of one token in
GPU memory, which is a different quantity that shares the name in English.
_Avoid_: bytes/token (unqualified — that is the KV arithmetic), token size, compression ratio

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

**Candidate**:
One replica as a policy sees it: its identity together with the load the router knows it is under.
Policies are handed candidates rather than bare replicas, so a rule that ignores load has to ignore
it deliberately.
_Avoid_: option, target, choice (that is the decision, not what it was made from)

**Spill**:
The router's decision to decline the best prefix match and route elsewhere because that replica
is under KV or load pressure. A router-layer decision only.
_Avoid_: preemption, eviction, overflow — vLLM independently preempts and swaps sequences under
its own KV pressure, which is a different thing at a different layer. Never call that spilling.

**Affinity**:
The router's decision to route to the replica with the best prefix match.
_Avoid_: stickiness, pinning

**Cold**:
A request no replica was believed to hold any of, routed on load because there was no cache
locality to preserve. It is its own decision rather than a least-outstanding decision or a
declined affinity: a policy producing nothing but cold decisions has an index that is not
working, and no goodput figure beside it would reveal that.
_Avoid_: miss, no match, fallback

**Session affinity**:
Routing every turn of a session to one replica by hashing its session identity onto a ring of the
replicas. Named for the session rather than for the prefix, because it is the *other* kind of
affinity and the two are the comparison: this one knows nothing about what a replica holds and
only that a conversation went there before. Consistent hashing rather than hash-modulo-count, so
that a replica leaving moves only the sessions that lived on it instead of nearly all of them.
It is deliberately blind to load — that blindness is the mechanism prefix affinity has to beat,
not a defect to be patched.
_Avoid_: sticky sessions, session pinning, affinity (unqualified — that is the prefix-match
decision above)

### Measurement

**Goodput**:
Requests per second that met the SLO. The primary metric. Distinct from throughput, which counts
requests that completed regardless of how badly.
_Avoid_: throughput, RPS, QPS

**Recomputed prefill**:
Prompt tokens a replica had to compute because it did not hold them, read off one fleet as
`vllm:prompt_tokens_total` minus `vllm:prompt_tokens_cached_total`. It says what the GPUs spent
and nothing about whether spending it was avoidable.
_Avoid_: prefill (unqualified), redundant prefill (that is the comparison below)

**Redundant prefill**:
Prompt tokens a replica had to compute because it did not hold them, which some other replica did
hold. The physical work prefix affinity exists to eliminate, and therefore the measurement of the
mechanism rather than of the outcome. No single fleet's counters can see it, because holding is a
fact about the siblings: it is measured as the recomputed prefill one policy carries over the
policy that recomputed least on identical bytes, which the comparison guarantees by refusing
cells whose workloads differ.
_Avoid_: wasted prefill, recompute, duplicate work

**Router overhead**:
Time from the router accepting a request to dispatching it upstream. Distinct from any latency
the fleet contributes, and reported separately so the router's own cost is never hidden inside
TTFT.
_Avoid_: routing latency, proxy overhead

**Latency floor**:
What a request costs with nothing in the way: one at a time, straight at a replica, no router and
no competing load. Measured across every replica and pooled, because an SLO derived from the
fastest card would be unmeetable on the slowest. It is the one latency figure in the project that
is not about the fleet under load, and it exists so the SLO is derived rather than chosen.
_Avoid_: baseline, best case, idle latency

**SLO**:
The per-request pass/fail threshold on TTFT and inter-token latency, derived as a stated multiple
of the measured latency floor rather than chosen a priori. The multiple is a judgement and the
floor is not, so the two are always published together.
_Avoid_: target, budget, threshold (unqualified)

**Characterization**:
The pass that establishes the measured facts every later number depends on — fleet KV capacity, the
latency floor and the SLO derived from it, and whether the replicas are interchangeable — before
any policy is compared. One pass rather than four, because an SLO derived from a fleet in one state
and a symmetry verdict about a fleet in another do not describe the same fleet.
_Avoid_: baselining, calibration, warm-up

**Probe**:
One replica driven at one load level, with no router in front of it. Deliberately not a cell: a
cell is a point of the policy comparison and carries a policy, and a probe has none, which is
exactly what lets it say something about a replica rather than about a routing decision. Its rows
share the harness row schema, so a probe's identity lands in the row's `cell_id` column — the
column is named for the commoner case and a probe row is told apart by carrying no policy.
_Avoid_: cell, baseline run, trial

**Schedule**:
Whether a probe had the host to itself. **Solo** drives one replica while every other is idle, which
isolates the card and its slot. **Together** drives all six at once, which is the only condition in
which host-side contention exists at all — four cards sharing a NUMA node only compete for its cores
while all four are busy. The two answer different questions and a latency from one is never compared
against a latency from the other.
_Avoid_: mode, parallel, isolated

**Cell**:
One benchmark data point: a fixed (policy, concurrency or arrival rate, working set ratio, skew,
repetition). Cells are cached so a sweep can resume, and each carries its own contamination
evidence.
_Avoid_: run, trial, sample

**Clean cell**:
A cell during which no process outside the experiment held memory on any of the six GPUs. The
box is shared, so cleanliness is recorded per cell and unclean cells are discarded and re-run,
never averaged in. Cleanliness is about *foreign processes only*; a cell can be clean and still be
thermally throttled, which is a separate defect with its own evidence.
_Avoid_: valid run, good sample

**Thermal throttle**:
A GPU clocking itself down because it is too hot, rather than because it has hit its power limit.
The driver reports the two separately, and the distinction is the whole point: every card in a
loaded fleet is power-capped, which is normal and equal, while a card that is *thermally* limited
is slower than its siblings for a reason that has nothing to do with the workload. Measured on GPU
3 of this host, where it appears only when all six cards draw power at once and worsens with time
on load. A throttled cell is as invalid as an unclean one and is not the same thing.
_Avoid_: overheating, thermal issue, slow GPU

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

**Arrival rate**:
The requests per second an open-loop cell offers the fleet. It is that driver's input the way
concurrency is the closed-loop driver's, and it is a property of the schedule rather than of the
fleet: a cell offered 32 requests per second offered exactly that whether the fleet served 32 or
four. Written into the cell id as `a32`, against `c32` for a concurrency.
_Avoid_: offered load (that is the quantity, not the axis), throughput, RPS

**Think time**:
How long a session waits between its turns under the open-loop driver. With the arrival rate it
sets how many conversations a cell holds open at once — rate times think time — so it is what
decides whether sessions reach their later turns at all, and therefore whether there is a growing
prefix for a policy to be aware of. Stated as a duration rather than a conversation count because
a count means something different at every rate and a gap between turns does not. The closed-loop
driver has no such knob: there the next turn goes out when the last response arrives.
_Avoid_: delay, pacing, user delay, session pool size

**Schedule lag**:
How long after its due time a request was actually sent. It is the open-loop driver auditing
itself: a driver that fell behind its own schedule offered less than the cell claims, which is
the closed-loop failure mode reappearing inside the driver that exists to avoid it. Recorded per
request and summarised per cell, and a cell whose lag ran past the threshold is flagged rather
than quietly reporting a rate it never offered.
_Avoid_: jitter, delay, drift

**Load level**:
One point of a sweep's load axis: a concurrency under the closed-loop driver, or an arrival rate
under the open-loop one. It is one type in the code (`bench.Load`) because the two drivers differ
in exactly this — which side of the loop is held fixed — and a cell carries the level it was run
at rather than the number of requests that happened to result.
_Avoid_: load (unqualified — that is offered load or inflight), level, step

**Sweep**:
A set of cells varying one axis while everything else is held fixed. Three exist: concurrency,
arrival rate and policy tunables. The first two are the same measurement under the two drivers and
are never merged into one column — a concurrency level and an arrival rate are different inputs to
different loops. The pressure grid runs alongside them all and is not a sweep, because it crosses
two axes at once.
_Avoid_: benchmark, experiment (those mean the whole comparison), pressure grid

**Pressure grid**:
The two-dimensional set of cells crossing working set ratio with skew at a single fixed
concurrency. Two-dimensional because memory pressure and load imbalance are physically different
and fire different branches of the spill rule, so neither stands in for the other.
_Avoid_: pressure sweep, the grid, pressure map (that is the figure drawn from it)

**Pressure map**:
The figure drawn from the pressure grid: the goodput delta between two policies at each of its
points, showing where cache-aware routing pays and where it does not.
_Avoid_: heatmap, pressure grid (that is the set of cells it is drawn from)

**Working set ratio**:
Total session tokens offered divided by aggregate fleet KV capacity. The axis of the pressure grid
that creates memory pressure, because it determines whether the fleet can hold every session at
once and therefore whether spill ever fires. Written WS. It names the session pool a cell offers,
which is the pressure a cell asks for rather than the pressure it applies: skew decides how much of
that pool a cell of finite length actually draws from, and at the top of the skew axis that is a
large discount. A cell records both.
_Avoid_: load, pressure, session count

**Skew**:
The Zipf parameter governing how unevenly traffic concentrates on a subset of sessions. The axis of
the pressure grid that creates load imbalance rather than memory pressure, and the one the
mechanism most likely lives on: session-sticky hashing is blind to load, so concentrated traffic is
the clearest place prefix affinity with spill can beat it.
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
