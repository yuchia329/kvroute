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
The prefix index chunks by bytes, not tokens, so its blocks are self-consistent within the router
but do not align with vLLM's internal blocks. Exact residency matches in vLLM's own blocks of
tokens instead, because that is how the engine's events name them.
_Avoid_: chunk, segment, page

**Prefix index**:
The router's trie mapping prefix block hash to the set of replicas believed to hold that block.
It is a belief about state the router does not own, and it decays as replicas evict.
_Avoid_: prefix cache, radix tree, cache map

**Prefix match**:
The length of the longest chain of leading prefix blocks a candidate replica is believed to
hold. Measured in bytes under the prefix index and in the engine's tokens under exact residency,
and each reported in its own unit rather than converted into the other's.
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
router's prediction of it. Unqualified, it is the figure for a whole run or cell: the two
cumulative counters read once at each end and differenced.
_Avoid_: hit rate (unqualified)

**Windowed hit rate**:
The prefix cache hit rate taken per replica over a moving window of the router's own scrapes —
the oldest reading in the window differenced against the newest — rather than over a run. It is
the spill rule's residency signal since #28. The windowing is the whole of it: the counters are
cumulative, so a replica's lifetime ratio barely moves once a run is under way and says nothing
about what that replica is doing now. A window holding too few block queries is unread rather than
low, because a rate over three blocks is arithmetic and thresholding it would spill on noise.
Measured over 4,022 decisions it spans 0.014 to 0.908 and moves 0.6 points across the whole load
range, against the batch gauge's 47.3 (ADR-0011).
_Avoid_: prefix cache hit rate (unqualified — that is the per-run figure above), cache health

**Belief divergence**:
The gap between the router's prefix match prediction and the replica's actual prefix cache hit
for the same request. Measured per request, in the engine's tokens, against
`usage.prompt_tokens_details.cached_tokens` — the only per-request account there is, the engine's
histogram of the same quantity carrying no request id to join on.
_Avoid_: cache miss, staleness, drift

**Over-prediction**:
Belief divergence where the router believed in more than the replica held. The direction that
misroutes: the request pays the full prefill anyway and spent its routing decision on a reason
that had stopped being true, which is worse than having routed on load.
_Avoid_: false positive, stale hit

**Under-prediction**:
Belief divergence where the replica held more than the router claimed. It forfeits a match that
was really there, costing an avoidable prefill, but never actively misroutes. Never summed with
over-prediction: they are different failures with different costs, and a net figure reports a
router that does both equally as one that does neither.
_Avoid_: false negative, miss

**Honoured belief**:
The share of the prompt tokens the index claimed that the engine turned out to be holding. It is
what the index's node cap is scaled by, and it is bounded above by one: an index that was right
about everything it claimed is fully honoured however much it missed.
_Avoid_: accuracy, precision, hit rate

**Honoured rate**:
Honoured belief taken per replica, live, over a bounded window of that replica's most recent
requests, fed from the usage block of every response the router already proxies. Recorded on every
decision and routed on by nothing. It was built as the spill rule's residency signal and measured
saturated: because the index is calibrated not to over-predict, it drops beliefs before the engines
evict the blocks, so 70% of readings are exactly 1.0 and no window recovers a range (ADR-0011). Kept
as the live counterpart of belief divergence, and as the column that shows the saturation rather
than letting it be rediscovered.
_Avoid_: cache health, residency score, honoured belief (unqualified — that is the per-run figure
above)

**Residency signal**:
Whatever the spill rule's first branch reads to decide that a replica is evicting the match it is
being asked to serve. It is a role rather than a metric, and three have held it: the KV gauge, which
measured the running batch; the honoured rate, which measured the index's own conservatism; and now
the per-replica prefix cache hit rate over a moving window, which measures what the engine's cache
actually answered. Each rename was forced by measuring the signal rather than trusting its name
(ADR-0011).
_Avoid_: memory pressure signal, cache pressure (those name the pressure, not the measurement)

**Scoring request**:
A request that said something about whether a replica is honouring beliefs: one the router claimed
a prefix match for, sent to that replica, and got a usage block back from. A request the index
claimed nothing for is not one, and is not counted: it would be a claim perfectly honoured by
arithmetic, dragging every rate towards one and silently disabling the condition that reads it.
_Avoid_: sample, observation, probe (that is a characterization measurement)

**KV cache event**:
A notification published by a replica when it stores or removes a block. Consuming the stream
gives exact residency rather than a belief, at the cost of depending on engine cooperation.
_Avoid_: cache notification, invalidation

**Exact residency**:
What a replica's prefix cache holds, as its own engine reported it through KV cache events: a
block is held from the event that stored it to the event that removed it, with no TTL or cap,
because nothing is being guessed. Kept in the engine's tokens and block size, so a prefix match
against it is a count of tokens the engine's usage block can be held to directly. Exact about
what the engine has computed and reported, and blind to a prefill still in flight. It forgets
rather than guesses: a replica whose stream lost history is reset, never kept. Policy 5 routes on
it (ADR-0010).
_Avoid_: ground truth (that is the engine's own per-request account), precise index, KV index

**Lost history**:
KV cache event batches a router's stream never received and could not recover from the engine's
replay buffer: everything older than the buffer when a router subscribes late, and any later gap
the buffer no longer covers. It is counted per replica rather than hidden, and never guessed
across: what the router held for that replica is forgotten and rebuilt from the events that
follow, because a missing batch may have removed any of it.
_Avoid_: dropped events (a dropped request is something else), missed events, desync

### Load

**Inflight**:
The count of requests the router has dispatched to a replica and not yet seen complete. Counted
locally and exactly, never scraped. Includes both requests vLLM is running and requests vLLM has
queued. Recorded on every row as the chosen replica's inflight at the moment it was chosen — but
it is the router's own bookkeeping, so how balanced a policy left the fleet is read from placement
below and not from this column. Every session-affinity row written before 9311e68 carries zero
here, meaning "not recorded" rather than "idle" (#27).
_Avoid_: queue depth, outstanding, load, concurrency

**Load denominator**:
What the spill rule's load condition holds a replica's inflight against: the fleet's minimum
inflight, or its mean, floored at one request either way. It is part of the grid point a cell
records rather than a detail of the comparison, because the two are one rule only while the fleet
is busy — the minimum is a single replica's small integer, and under the open-loop driver at a low
arrival rate it is 0 or 1 on most decisions, which makes a multiple of it an absolute count of
requests rather than the ratio the rule is written as (#31). Flooring the minimum at the mean is
the mean: a minimum is never above its own mean, so there is no regime where the first comes back.
_Avoid_: floor (that is the one request below which neither is used), threshold (that is the factor
times this), fleet load

**Batch KV occupancy**:
The fraction of a replica's KV cache blocks held by the requests it is currently running, scraped
from that replica as `vllm:kv_cache_usage_perc`. A load signal, and named for what it counts
rather than for what it was taken to mean: through #16 it was read as memory pressure, and it is
not that. Blocks holding the cached prefixes of finished requests are free to the allocator and do
not count, so the gauge is blind to exactly the residency a prefix match depends on. Measured, it
is 0.021 + 0.0214 × inflight at r = 0.973 — the router's own inflight in other units. Nothing
routes on it; it is recorded beside the inflight of the same decision so that claim stays checkable
(ADR-0011).
_Avoid_: KV utilization, memory pressure, cache usage, cache residency

### Routing

**Policy**:
A pluggable rule mapping a request plus fleet state to a chosen replica. Six exist — idea.md §5's
five, and the stateless prefix hash it does not number; only the policy varies between benchmark
runs.
_Avoid_: strategy, algorithm, scheduler

**Candidate**:
One replica as a policy sees it: its identity together with the load the router knows it is under.
Policies are handed candidates rather than bare replicas, so a rule that ignores load has to ignore
it deliberately.
_Avoid_: option, target, choice (that is the decision, not what it was made from)

**Spill**:
The router's decision to decline the best prefix match and route elsewhere because that replica is
evicting the match or is buried under load. A router-layer decision only. Its two branches read two
signals — the honoured rate and inflight — and that they are two signals rather than one in two
units is a measurement each run makes, not a property of the code (ADR-0011).
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

**Untokenized**:
A request exact residency could not look up, because the engine did not tokenize its prompt in
time, and which was routed on load instead. Its own decision rather than cold: cold says no
engine held the prompt, and this says nobody could look. A cell full of them had an index nobody
could ask, which no goodput figure beside it would reveal.
_Avoid_: cold, failed (nothing failed that the client saw)

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

**Stateless prefix hash**:
Routing on a hash of the prompt's leading blocks, placed on the same ring session affinity uses,
weighed against replica inflight, and remembering nothing. It is the third kind of cache-aware
routing here and the bottom rung of the residency ladder: it holds no index and no belief, where
prefix affinity holds a believed one and exact residency the engines' own. Named for what it
hashes rather than for the lab that documented one — the mechanism is inferred from OpenAI's
public documentation of a hash of "the initial tokens" plus machine load, and is never reported as
a reproduction of their router.
_Avoid_: OpenAI routing, prompt cache key routing, content hash (unqualified), prefix hashing
(unqualified — the prefix index hashes blocks too)

**Hash window**:
How many of a prompt's leading blocks the stateless prefix hash covers, and therefore how much of
a prompt decides where it goes. A window and not a prefix length, because both ends of it bind: a
short one hashes every conversation carrying the shared system prompt to one key, and a long one
reaches into the part of the prompt that grows, so a conversation's second turn lands somewhere
other than its first. Stated for every run rather than defaulted, because no number for it has
ever been published.
_Avoid_: initial tokens, prefix length, hash depth

**Deflection**:
The stateless prefix hash's decision to pass over the replica its hash ranked first because a
sibling was enough less loaded to outweigh it. Distinct from a spill, which declines a prefix
match the router believes in and pays a prefill to escape pressure; a deflection gives up no
belief, because that policy holds none. How often it fires is how the weighting between the two
terms is read.
_Avoid_: spill, overflow, rebalance

**Unhashed**:
A request whose prompt was shorter than the hash window, routed on load because it had no leading
blocks to hash. Its own decision rather than cold, for the reason untokenized is: cold says no
replica held the prompt, and this says the prompt was too short to ask about. A cell full of them
offered prompts the window never fitted, which no goodput figure beside it would reveal.
_Avoid_: cold, short prompt, unhashable

### Failure and recovery

**In rotation**:
A replica the router is offering to the policy. A replica leaves rotation by ejection or by drain,
and while it is out it is simply absent from the snapshot a policy decides from, so no policy has to
know that replicas can leave. It is still counted: the requests it was already serving finish, and
are counted down, whichever way it left.
_Avoid_: healthy (that is one of the two reasons, not the state), available, up

**Health check**:
The router asking a running replica whether it is alive — a GET of its `/health`, once a second —
and the active half of ejection: two in a row failed eject it, and two in a row passed readmit it.
Distinct from preflight, which asks whether the GPUs are free before any replica exists.
_Avoid_: probe (that is a characterization measurement), heartbeat, ping

**Ejection**:
The router taking a replica out of rotation because it stopped answering: found actively, when two
health checks in a row fail, or passively, when a real request cannot reach it or loses it
mid-stream. Undone by the same tracking — two health checks in a row passed readmit it, with nothing
restarted. Only a failure to answer counts. A replica that answers with an error status is
overloaded rather than gone, and ejecting it would change the fleet under the load being measured.
_Avoid_: removal, blacklisting, circuit breaking, outlier detection

**Drain**:
An operator taking a replica out of rotation gracefully: from that moment it is given no new
request, and every request it is already serving finishes. Undone only by the operator, by restoring
it. A drained replica that comes back healthy stays out, because the drain was a decision and not a
symptom.
_Avoid_: cordon, decommission, graceful shutdown (that is the replica process's own, which a drain
comes before)

**Reroute**:
Sending a request to another replica because the one it was placed on failed before emitting any of
its answer. The client cannot tell: nothing reached it before the failure, and the second replica's
answer is the whole of what it receives. Only a request with nothing emitted can be rerouted; one
already streaming when its replica is lost is dropped, and never rerouted.
_Avoid_: retry (that is the client's), failover, resend

**Recovery curve**:
Goodput sampled through a replica failure and its recovery, in buckets of the time requests were
offered, read relative to the moment of the fault. A curve rather than a before-and-after pair,
because what the two affinity policies are predicted to differ in is its shape, and they are
predicted to differ twice: when the replica leaves, and again when it returns — session affinity's
ring moves that replica's sessions back onto an empty cache, and a prefix index does not.
_Avoid_: failure graph, chaos graph, recovery time (that is one number read off it)

**Chaos run**:
One policy's run of a chaos scenario: a replica taken away from the fleet under steady open-loop
load and brought back, with every request, what the router did with the replica, and the recovery
curve recorded. The scenario is what two policies' runs share — which replica, which fault, the
load, the timing, the SLO and the workload — and two runs are compared only if they share all of it.
_Avoid_: chaos cell (a cell is a point of the policy comparison, on a whole fleet), chaos test (that
is the whole experiment)

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
fact about the siblings: it is measured as the recomputed prefill **per request** one policy
carries over the policy that recomputed least per request on identical bytes.

Per request, not in total. Refusing cells whose workloads differ guarantees both policies the same
*generator*; it does not guarantee them the same number of *prompts*. Under the closed-loop driver
a virtual user sends its next turn when its last one returns, so a policy that answers faster gets
further through the same sequence and offers more prompts in the same window. Absolute recomputed
tokens then rise with throughput, and a column built on them credits the slower policy with having
wasted less — which is false, and which #18's grid showed at six of its twelve points. Where a
token total is wanted it is a policy's per-request excess times **its own** requests, never a
difference of two policies' raw totals.
_Avoid_: wasted prefill, recompute, duplicate work

**Placement**:
Which replica served a request, counted per replica over a cell's measured rows. It is the
fleet-side view of a cell, where the decision mix is the router-side one: the mix says what
reasons the policy gave, and this says what fleet it left behind. Counted whatever each request's
outcome, because a request a replica accepted and then failed still occupied it.
_Avoid_: distribution, assignment, routing (that is the decision)

**Fleet imbalance**:
How unevenly a policy spread a cell's requests across its replicas, reported as the busiest
replica's share of the placed requests against the fair share one replica would carry if the load
were even, and as the spread between the busiest and the quietest. It is what idea.md §5 predicts
consistent hashing pays for its locality, and it is counted from the rows rather than read off the
per-decision inflight column — which recorded zero on every session-affinity row written before
9311e68 (#27). The replica count in the figure is the replicas that served at least one request,
not the fleet: one that served nothing is in no row, so the figures understate an imbalance that
bad rather than overstate it.
_Avoid_: skew (that is the workload's Zipf exponent), load spread, hot spotting

**Router overhead**:
Time from the router accepting a request to dispatching it upstream. Distinct from any latency
the fleet contributes, and reported separately so the router's own cost is never hidden inside
TTFT.
_Avoid_: routing latency, proxy overhead

**Engine step**:
One iteration of a replica's scheduler: every token the engine computes in one forward pass, across
every request it scheduled — a whole prompt or a chunk of one for a request still prefilling, one
token for each request decoding. It is the unit the GPU executes, so it is the unit the roofline
places: a request is many steps, and a step is many requests. vLLM names each one's composition when
its profiler runs, which is where the roofline reads it from.
_Avoid_: iteration, forward pass, batch (a batch is the requests a step serves, not the work it does)

**Roofline**:
Engine steps placed by arithmetic intensity — FLOPs per byte of GPU memory traffic — against the rate
they achieved, under the card's two ceilings: memory bandwidth times intensity, and peak compute.
Left of the ridge where the two meet a step waits on memory, and right of it on arithmetic. The
intensity is counted from the model's shapes rather than read off hardware counters, which this
box reserves for root, so it is exact about what a step had to do and blind to what its kernels
wasted doing it; only the time is measured.
_Avoid_: speed of light (that is Nsight Compute's per-kernel section), performance model

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
on load. A throttled cell is as invalid as an unclean one and is not the same thing. Every cell
records what each of its cards clocked at and why, and is flagged when one of them spent more than
half its busy samples limited by heat (ADR-0013).
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

**Visit**:
One session's run from its first turn to its last, before the workload draws a fresh session for
that slot. Its length is the workload's turns per session, and every conversation in an
open-loop cell's pool walks its visit in step, because the rotation maps the k-th arrival to turn
k/pool and so makes the turn index the round.
_Avoid_: session (that is the conversation, not the pass through it), episode, epoch

**Visit period**:
How long a cell takes to walk one visit: turns per session times think time. It is the period of
everything that follows from prompt length — TTFT, prefill tokens, KV held per session — because
the whole pool rolls over to freshly drawn sessions at once. A measured window holding a
fractional number of visit periods offers some turn indices more often than others, so its
percentiles are over a mix no cell of another length shares.
_Avoid_: cycle, round (that is one turn for the whole pool, not a whole visit), session length

**Warm-up drift**:
How much a cell's TTFT p50 moved across the window it was measured over, compared within each
turn index and split on the arrival window. Positive is slower early. It is what lets the warm-up
length be checked rather than trusted, and it is judged two-sided: a cell that got twice as slow
is as unpoolable as one that got twice as fast. A cell over the threshold is flagged with the
cause the check found — a still-cold opening period, a fleet that degraded, or a fractional
number of visit periods — because the three have different fixes and only the first is a longer
warm-up.
_Avoid_: warm-up error, ramp, drift (unqualified — that is belief divergence or schedule lag)

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
and fire different branches of the spill rule, so neither stands in for the other — measured in
#18, where the residency branch's firing rises three- to tenfold up the working set axis while the
load branch's halves over the same axis.
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
A request that received no complete response because the router could not place it, or because the
replica it was placed on was lost after its stream had begun — the failure the chaos test counts. A
request whose replica is lost before any of its answer has reached the client is not dropped but
rerouted, so a drop count is only a claim when the reroute count stands beside it.
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

**Topology class**:
The host's own name for how two cards reach each other, as `nvidia-smi topo -m` gives it: `PIX`
through one PCIe switch, `NODE` across the host bridges of one socket, `SYS` across the link between
the sockets. It groups card pairs whose copies cross the same kind of path, and it is the axis KV
transfer bandwidth is measured along, because pairs of one class should agree and pairs of two
should not.
_Avoid_: link type, distance, hop count

**Peer copy**:
A copy from one card's memory to another's as CUDA performs it when asked — what a framework gets
by calling copy. On this host no pair has direct peer access (the driver reports the chipset
unsupported), so the driver stages every peer copy through host memory itself.
_Avoid_: P2P (that is direct access between cards, which this host does not have), DMA transfer

**Host bounce**:
A card-to-card copy staged through pinned host memory by hand: chunked, with the sender's copy out
of one chunk overlapping the receiver's copy into the one before. It is the fastest a KV transfer
path written for this host could move bytes without peer access, and where its host buffer sits
decides whether either leg crosses between the sockets.
_Avoid_: staging copy, relay, host copy (that is one leg of it: a card and host memory)

**Preflight**:
The check that refuses to bring the fleet up while any GPU already holds memory. It is the same
probe that samples for foreign processes during a cell, differing only in that nothing is exempt:
before the fleet exists, every process on a card is contamination whoever started it.
_Avoid_: health check (that is the router asking a running replica whether it is alive), precheck
