# ADR-0010: Exact residency asks the engine for its tokens and forgets what it cannot vouch for

**Status:** Accepted · **Date:** 2026-09-10 · **Amends:** ADR-0007

## Context

Policy 5 (#24, idea.md §5) replaces the prefix index's belief with the engines' own account of
their caches. vLLM 0.28.0 publishes a KV cache event for every block it stores in and removes
from its prefix cache, and a router that follows those events knows what each replica holds
rather than guessing it from where it sent things. Running that head to head against policy 4 is
a replication of llm-d's precise-versus-approximate comparison, at a scale nobody has covered.

Four things stand between the event stream and a routing decision, and each had to be decided:

1. **The events name blocks by tokens, and the router has no tokenizer.** idea.md §4.3 rejected
   one for the prefix index, which only has to agree with itself. Exact residency has to agree
   with the engine.
2. **The events name blocks by the engine's own hash**, which is seeded per engine process. The
   router cannot compute it for a prompt.
3. **The events are not a ledger.** Two physical blocks can carry one hash, each stored with an
   event of its own and each removed with one. And a request that asks for full reporting
   (`kv_cache_report_mode` of `"full"`, which this harness never sets) has every block it reused
   out of the cache reported stored again (`emit_cached_block_events`), with no removal to pair
   with the second report.
4. **The stream carries no initial state.** A subscriber sees what is published after it
   connects. Dynamo's own comparison says so of vLLM: *"No built-in initial state sync"*, and a
   gap older than the publisher's replay buffer *"must rebuild state through other means"*.

And one thing stands between the policy and a fair comparison: publishing events is work the
engine does.

## Decision

**1. The engine tokenizes the prompt.** Before routing, the policy sends the chat body verbatim
to a replica's `/tokenize`, which renders the chat template and tokenizes the result with the
same code a chat completion of that body runs. The tokens therefore cannot drift from the ones
the engine caches. Replicas are asked in turn.

The alternative, a tokenizer and a byte-exact chat template inside the router, needs cgo or an
unofficial port, and it fails silently: one template byte out gives different tokens, every
match drops to zero, and the result reads as "exact residency found nothing". Asking costs one
request per prompt before routing, paid in router overhead and in API-server CPU on the fleet.
Every row records it (`tokenize_ns`), so the cost of knowing exactly can be told apart from the
cost of deciding. A tokenization that fails, or takes longer than 500 ms, is routed on load under
its own reason, `PROMPT_UNTOKENIZED`. The 500 ms is chosen, not measured, and the decision mix
shows whether it ever bound.

**2. Blocks are keyed by a chain over their tokens, not by the engine's hash.** The index hashes
each block's tokens onto its parent's chain (FNV-1a, continued from the parent's value) and
remembers, per replica, which chain each engine hash names. A stored run names its parent by
engine hash, and the parent's chain is looked up. A prompt is hashed by the same function, so
both sides of a match come from one place.

A run whose parent the index never saw cannot be placed, and is left out and counted as
orphaned. A block whose engine hash covers more than its tokens (a LoRA adapter, a cache salt,
multimodal inputs) is refused together with everything after it in its run, since a prompt
identified by tokens alone could never be served from it. So are blocks in a tier other than the
GPU, and blocks at a size other than the engine's; the router is told that size
(`-kv-block-size`, from ops/versions.env) and has no default for it.

**3. Holding is a fact, not a count.** A stored event sets holding, and one removal ends it,
however many times the block was reported stored. Counting reports would be exact for
duplicates, and would keep a re-reported block believed after the engine let it go: the
over-prediction ADR-0006 calls strictly worse than routing on load. Whether that can happen would
then depend on how each request asked to be reported, which is not something a routing index
should rest on.

The price is the opposite error on a duplicate. When one of two copies carrying one hash is
evicted, the index forgets a block the engine still holds, and in the default reporting mode
nothing reports it again while that copy lives. The engine's scheduler looks for cached blocks
before allocating each request, which keeps duplicates to requests that compute the same new
block before either is cached. How much they cost is not argued here but measured: it is
under-prediction, and belief divergence reports it in its own column.

**4. What cannot be vouched for is forgotten.** A new connection starts the replica from nothing,
because the engine may have restarted with its sequence back at zero and nothing on the wire says
so. It then replays whatever the publisher buffers, subscribing first and replaying second so
that no batch falls between the two, and fills any later gap from the buffer the same way.

History the buffer no longer holds is lost, and so is a batch that arrives and cannot be read.
Either way the replica's residency is reset and rebuilt from what follows. That can make the
index forget blocks the engine holds, but never keep one it dropped. The stream counts every
batch lost and every reset, and `/router/stats` reports them per replica, beside the blocks held
and the runs orphaned.

**5. The cold-start limitation is handled by counting it, not by hiding it.** A router that
subscribes after its engines have been publishing recovers the last 10,000 batches (the engine's
default `buffer_steps`, pinned as `KV_EVENTS_BUFFER_STEPS`) and nothing older. Blocks stored
before that are held by the engines and unknown to the index, and they show up as lost batches
and orphaned runs.

In the grid this should not arise: the fleet is restarted before every policy, the engines
publish nothing until the first request, and the router subscribes before that request is sent.
After a router restart mid-run it does arise, and the stats say by how much. The router also
refuses to start at all until every replica's stream has connected (`-kv-events-wait`), because
a router following nothing would route every request cold and report that as the exact policy's
result.

Each cell carries the same evidence for its own window. The harness reads the router's residency
figures on either side of every cell, as it reads the ejection count, and records the batches
lost, the resets, the reconnects and the orphaned runs in between. A cell during which history was
lost, a stream reconnected, or a stream ended disconnected is flagged and kept out of every
figure, like a contaminated one: its index knew less than it claimed for part of the window, and
nothing in its goodput would say so. Orphaned runs are recorded but do not flag. A duplicate block
evicted while its twin lives on orphans the next run stored on top of it, and that is the index
forgetting what it cannot vouch for rather than history lost.

**6. KV events are an engine setting, so both sides of the comparison run with them on.**
`KV_EVENTS=1` adds `--kv-events-config` to every replica: a publisher thread in the engine's
process encoding one event per block stored or removed. That makes it a different engine
configuration (ADR-0001, ADR-0007). Policy 5's cells are therefore compared only with cells
recorded under the same setting. Policy 4 is measured again with events on, rather than read
from a grid recorded without them.

That is enforced rather than remembered. `KV_EVENTS` is set in `ops/versions.env` and nowhere
else, and the harness labels every cell with it (`kv_events`). A sweep refuses to resume cells
recorded under the other value, compare refuses to put the two in one table, and a router that is
visibly following events refuses a sweep that would label its cells as run without them.
`make pressure-grid` sweeps an events-on fleet into `runs/pressure-kv-events`, so #18's
events-off grid in `runs/pressure` is never resumed into or merged with.

**This amends ADR-0007**, whose consequence was that every ticket after the definitive run adds
only its own cells. Policy 5 cannot: the setting it needs changes the engine every cell shares,
so its comparison re-measures policy 4 under that setting. The frozen grid's policy 4 cells stay
what they were, the events-off reference, and are not merged with the re-measurement.

**7. The router speaks ZeroMQ itself.** The router is cross-compiled without cgo, so it cannot
bind libzmq. It speaks the two socket roles it needs, SUB to follow and DEALER to ask for a
replay, in ZMTP 3.0 with the NULL mechanism, and it decodes the three event types from msgpack.
There is no new dependency. Both halves are tested against the versions the engine itself runs:
the decoder against payloads msgspec 0.21.1 encoded (`internal/kvevents/testdata/gen.py`), and
the wire against the libzmq 4.3.5 bundled with pyzmq 27.2.0 (`KVROUTE_LIBZMQ=1`).

## Consequences

- **Exact residency cannot see a prefill still in flight.** The engine reports a block once it
  has computed it. Policy 4 records its belief at the decision, so a session's overlapping
  turns find each other there; under policy 5 they can look cold until the first turn's blocks
  are reported. That is part of what the comparison measures, not a defect to be tuned away.
- **The two policies differ in more than their knowledge.** Policy 5 pays a tokenization per
  request, matches in 16-token blocks aligned to the engine's rather than 64-byte ones, and has
  no TTL or node cap to calibrate. The write-up has to say which of these a difference can be put
  down to.
- **Its prediction is in the engine's tokens** (`prefix_match_tokens`), and needs no
  bytes-per-token conversion to be held against `usage.prompt_tokens_details.cached_tokens` for
  the same request. The byte columns are zero under it.
- **Replays can arrive with holes.** The ROUTER socket that serves replays keeps libzmq's
  default high-water mark, and libzmq drops what a ROUTER cannot queue for a peer unless told
  otherwise, which the engine does not do. A large replay can therefore arrive with holes. The
  stream treats a hole as lost history, so the cost is a reset, never a wrong belief.
- **The comparison costs three policies' grids, not one.** The share of the achievable gain the
  approximation keeps is (policy 4 − policy 3) / (policy 5 − policy 3) in goodput at each grid
  point, against session affinity as the baseline that matters. All three have to be measured
  under the same engine setting, so #24's grid re-measures session affinity and prefix affinity
  with events on beside exact residency: three policies over twelve points, roughly nine to ten
  hours at the frozen cell.
- **The tokenization rides on the replicas' API servers.** Every prompt is tokenized twice on the
  fleet. Asking in turn spreads it across all five alike, but it is load the other policies do not
  add, and it counts as this policy's cost.
- **Not yet checked on the box:** that `/tokenize` returns exactly as many tokens as a chat
  completion's `usage.prompt_tokens` for the workload's own bodies. The exactness claim rests on
  it, so it is checked against the live fleet before the comparison runs.
