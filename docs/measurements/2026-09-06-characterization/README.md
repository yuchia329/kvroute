# Characterization, 2026-09-06

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
