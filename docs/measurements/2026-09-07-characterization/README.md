# Characterization, 2026-09-07

The measured facts every later number in this project scales off, established in one pass on
`nlp-gpu-01.be.ucsc.edu` against six replicas of the pinned engine. This is issue #10.

Four things are settled here: how much KV cache the fleet actually has, what latency the hardware
delivers with nothing in the way, what SLO follows from that floor, and whether the six replicas are
interchangeable enough for a difference between policies to mean anything. They are one pass rather
than four because they have to describe one fleet in one state.

## Files

| File | What it is |
|---|---|
| `report.md` | the reading of the run — capacity, topology, floor, SLO, symmetry |
| `characterization.json` | the record: every figure above, plus every probe with its own contamination and prefix-cache evidence |
| `probes.jsonl` | per-request rows — **the system of record**; every figure is recomputable from these |
| `contract-fleet.txt` | the metric contract asserted against all six live replicas |
| `topology.txt` | `nvidia-smi topo -m` verbatim |
| `engine-startup.txt` | each replica's own log lines for backend, model load and KV capacity |
| `characterize.log` | the run's own log, probe by probe |

## Conditions

vLLM 0.28.0, `Meta-Llama-3.1-8B-Instruct-AWQ-INT4`, 6× RTX 3090, one replica per GPU, ports
8000–8005, all engine settings from `ops/versions.env`. Each replica driven **directly**, one at a
time, with no router in the path: every question here is about a replica, and the router's policy
would otherwise be in the answer.

The fleet was **restarted immediately before this run**, so no replica had seen any of these
prompts. That is not a detail — see below.

Six replicas, two load levels, three repetitions, 90-second probes with a 25-second warm-up: 36
probes, 6,701 requests, every one of them on a clean fleet (`clean: true`, zero foreign processes).

## Headline figures

| Quantity | Measured | Note |
|---|---|---|
| Fleet KV capacity | **755,712 tokens** | 7,872 `num_gpu_blocks` × 16, identical on all six replicas; 15.38 GiB of KV each |
| Against the hand estimate | **+9.8%** | 114,688 estimated per replica; the whole gap is the KV-budget assumption, not the per-token arithmetic |
| Hardware latency floor | **TTFT p50 329 ms, inter-token p50 7.8 ms** | 1,487 requests pooled across six replicas at concurrency 1, at a 1.3% prefix-cache hit rate |
| **SLO** | **TTFT < 990 ms, inter-token p50 < 24 ms** | 3× the floor, rounded up — [ADR-0003](../../adr/0003-slo-is-three-times-the-measured-floor.md) |
| Working set ratios | **92 / 369 / 1,107 / 2,952** sessions of 2k at WS 0.25 / 1 / 3 / 8 | rescaled off the measured total; `idea.md` had 86 / 344 / 1,030 / 2,750 off the estimate |
| Replica symmetry at concurrency 1 | **2.3% spread, 0.0% between NUMA nodes** | inside the 8% tolerance, and the measurement is precise to 2.8% — an 8% difference is ruled out |
| Replica symmetry at concurrency 32 | **unresolved** | 22.1% between replicas against 36.3% for one replica against itself |
| Topology | GPUs 0–3 on NUMA 0 at 12 threads each, GPUs 4–5 on NUMA 1 at 24 | pairs `PIX`, within-socket `NODE`, across-socket `SYS` |
| Metric contract | passes on **all six** replicas | `contract-fleet.txt` |

**No escalation is due.** `idea.md` §10's ladder — CPU pinning, then dropping to a single NUMA node
— is entered only if the replicas differ beyond tolerance. At concurrency 1 they do not, and the
difference at concurrency 32 is smaller than the measurement's own noise, so acting on it would mean
changing the pinned engine configuration every later cell depends on for a number this run cannot
tell from scatter. §10's own instruction is "if all six match within noise, no action".

## What is still open

**Symmetry at concurrency 32 is not settled.** The spread between replicas is 22.1% and one replica
varies 36.3% against *itself* between repetitions — the comparison is inside its own noise, so it is
neither a difference nor evidence of none. Three 90-second probes per replica is not enough at that
load; the mechanism §10 worries about lives there, so it wants a longer pass before the pressure grid
runs. The concurrency-1 result stands on its own and is what the SLO rests on.

Four of the 36 probes are flagged as still warming up, all at concurrency 32 and all in the 26–46%
band just over the 25% threshold. That is the same finding from the other end: at that load the
closed-loop driver's wave pattern and the queue's settling are not cleanly separated by a 25-second
warm-up.

## The capacity figure, reconciled

ADR-0001 recorded **119,408 tokens** from one replica at first contact; every bring-up since has
reported **125,952**. The difference is the `torch.compile` cache, and it reproduces to the token:

| | KV cache | engine init | compilation |
|---|---:|---:|---:|
| Warm compile cache | 125,952 tokens | 12.76 s | 0.26 s |
| Cold cache, `VLLM_CACHE_ROOT` moved aside | **119,408 tokens** | 38.55 s | **19.24 s** |
| ADR-0001, first contact | **119,408 tokens** | 37.5 s | **18.2 s** |

vLLM sizes the KV cache from the memory left free after its profiling pass, and on a cold cache
`torch.compile` is still holding about 0.8 GiB while that pass runs. Neither figure was wrong; they
were measured on differently-warmed hosts.

The consequence outlives the reconciliation: **KV capacity is not a property of the engine settings
alone**, so it is read off every replica at runtime on every run rather than taken from a constant
or from some earlier bring-up's startup log.

## The measurement that nearly went in wrong

The first pass of this reported a latency floor of **46 ms** and it was completely wrong. The real
figure is **~325 ms**, seven times higher.

Nothing about the fast rows looked wrong. They were clean, low-variance, contamination-free and
plausible. What they were measuring was the replicas' prefix cache: the workload generator is
deterministic in `(user, turn)` so that a re-run of a cell sends the same bytes, and every
measurement numbers its virtual users from zero — so the second repetition re-sent the first's
prompts verbatim, and the replicas, running with `--enable-prefix-caching`, did not prefill them at
all. An interrupted earlier run had left its prompts in the caches too, which is why the number of
"fast" requests on each replica matched exactly how many that run had sent it: 40, 38, 39, 17, 0, 0.

Two things came out of it, both in [ADR-0004](../../adr/0004-every-measurement-sends-unseen-bytes.md):

1. **Every measurement now sends from its own slice of the workload's user space**, keyed on its
   axes and not on its policy — so repetitions and concurrency levels differ, cells under two
   policies stay identical, and a resumed cell still re-sends its own bytes.
2. **Every probe records the engine's own prefix-cache hit rate over its window**, and the floor
   refuses to be used if that rate is above 5%. The evidence is the engine's counters, not the
   latency's plausibility.

The floor below was measured at a **1.2–1.3%** hit rate, which is the chat template's first block
and nothing else.

## What "not resolvable" means in the symmetry table

At concurrency 16 an earlier 30-second pass put the spread between replicas at 30%, which looks
like a fleet that needs CPU pinning. It was not: at that probe length each replica varied by up to
**44% against itself** between repetitions, more than the replicas differed from each other. A
difference smaller than the measurement's own noise is not a difference.

The symmetry verdict therefore carries a noise floor — the largest gap any single replica showed
against itself across its own repetitions — and reports a level as unresolved rather than
asymmetric when the spread does not clear it. Escalation follows only from a difference the
measurement could actually resolve, because pinning CPUs changes the pinned engine configuration
that every later cell depends on.
