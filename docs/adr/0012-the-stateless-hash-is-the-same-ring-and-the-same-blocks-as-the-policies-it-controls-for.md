# ADR-0012: The stateless hash is the same ring and the same blocks as the policies it controls for

**Status**: accepted, 2026-09-11
**Ticket**: [#26](https://github.com/yuchia329/kvroute/issues/26)
**Amends**: nothing. Adds a sixth policy outside idea.md §5's five; ADR-0007's frozen parameters
are unchanged and this policy's cells are recorded under them.

## Context

idea.md §5 numbers five policies, and its §1 records a verified fact about frontier practice:
OpenAI routes by *"a hash of the initial tokens"* plus machine load, with `prompt_cache_key` folded
into that hash as a disambiguator rather than a pin — their docs say such keys *"influence routing;
they do not pin requests to a machine or guarantee a cache read hit."* None of the five is that.
Session affinity hashes a conversation's identity and is blind to load; prefix affinity tracks a
believed residency in a trie under a TTL and a node cap; exact residency tracks the engines' own
account of their caches.

So the project measures what an index is worth against a policy that knows nothing about
prompts — and never against one that reads the prompt and still holds no index. Two questions go
unasked without it:

1. **What does tracking belief buy over a content hash?** Prefix affinity's index is the expensive
   part of this project: a trie, a calibration measured off the fleet, a TTL, a node cap, an
   eviction model, and a divergence measurement to check it. A stateless hash of the same prompt's
   leading blocks costs a ring walk and nothing else. If the two are close, the index's cost buys
   little at this scale, and that is the finding.
2. **Is the comparison's ladder complete?** With #24 the project has *believed* and *exact*
   residency knowledge. The rung below them — **none** — was missing, and a ladder missing its
   bottom rung reads as two points rather than as a trend.

## Decision

A sixth policy, `prefix_hash`, deliberately outside §5's five: it hashes the prompt's leading
blocks, places that hash on a ring of the replicas, weighs the ranking against inflight, and
remembers nothing at all.

Four choices make it a control rather than a fourth cache-aware policy:

**The chunking and hashing are #15's, called rather than copied.** `prefix.Blocks` gives the same
64-byte blocks and the same chained FNV-1a hash the prefix index is built on, so `prefix_hash` and
`prefix_affinity` read a prompt identically and differ only in what they do with what they read. A
second chunker would put a second difference into the one figure this policy exists to produce.

**The placement is session affinity's ring, called rather than copied.** The hash reaches the
replicas through the same consistent-hash ring, with the same 128 virtual nodes per replica and the
same avalanche. A replica leaving therefore moves the same share of the key space under both
policies, which is what lets §7's recovery curves be read as a difference between *what* is hashed
rather than *how* it is placed. The ring is now a shared `ringCache`, and the tie rotation both
policies break ties with is one function, for the same reason.

**The two terms are added in one unit.** A replica's cost is `inflight + weight × rank`, where rank
is how far down the ring's clockwise order it sits from the prompt's key. The weight is therefore
in inflight requests per rank step, and it says exactly what it means: the hash's first choice is
kept while it carries fewer than `weight` more requests than the next replica along. Its two ends
are controls rather than degenerate settings — 0 is least-outstanding with a hash that decides
nothing, and a weight above any imbalance the fleet can show is the hash with no load term — and
both are run, because they are what says whether the balance in between is doing anything.

**Neither knob is defaulted.** The window is the number nobody has published for "the initial
tokens"; the weight is the axis the policy is swept along. Both are stated per run, published on
`/router/stats`, recorded on every cell, and checked against the router before the first cell — the
same treatment the spill thresholds get, for the same reason: a grid of cells labelled with a point
that never ran is one point measured five times, in numbers that are all real.

## Consequences

**A prompt shorter than the window is not hashed on what it has.** Hashing the blocks a short
prompt happens to fill would give a conversation a different key on every turn until it grew past
the window, which is the opposite of what the window is for. Those requests are routed on load
under `PROMPT_UNHASHED`, so a run whose prompts never filled the window reads as that rather than
as a hash that preferred nothing. At this workload's geometry the window is under a third of a
first turn, so this should never fire; the smoke run fails if it does.

**The window has to be stated wherever the comparison is reported.** It is the one number that
decides how much of a prompt routes it, and 16 blocks — 1,024 bytes, roughly 644 engine tokens at
the measured 1.59 prompt bytes per token — is a choice bracketed by the workload's own geometry
rather than a measurement: past the 512-byte shared system prompt that a third of sessions send
identically, and well inside the first turn that every later turn resends unchanged.

**Nothing downstream depends on it.** It is optional by construction: no other policy, figure or
ticket reads its cells, so a night that does not get spent on it costs the project nothing.

**It must never be reported as OpenAI's router.** The mechanism is inferred from public
documentation, not from an implementation nobody outside OpenAI has seen. The window, the
weighting, the ring and the rank ordering are all decisions made here. Every write-up of this
comparison says so, in the same sentence that names the inference — which is also why the policy is
named for what it hashes rather than for the lab that documented one.

**It costs a fleet night it can be told apart from.** Its cells land in `runs/pressure-kv-events`
beside #24's three, so the ladder is read across one engine configuration (ADR-0010), which means
this policy runs on a fleet publishing KV cache events it does not itself read. That is a cost —
publishing is work the engine does on every step — paid so that the four policies in the ladder
differ in their routing and in nothing else.
