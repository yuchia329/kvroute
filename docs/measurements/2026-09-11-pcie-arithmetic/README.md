# PCIe bandwidth arithmetic — 2026-09-11

`idea.md` §8 asks whether disaggregating prefill and decode is worth pursuing on this box, and says
to settle it with measurement and arithmetic **before building any of it**. This is that answer, and
it is issue #22. No KV transfer path was built: the copies here move plain bytes of the same size as
a request's KV, and touch no engine.

**Bandwidth is not what rules disaggregation out on this host — and the margin is six times thinner
than §8 guessed.** Moving a 2,048-token request's KV costs **36.7 ms** against the **490.7 ms** that
prefilling it costs: **7.5%**. §8 predicted ~20 ms against a ~1.5 s prefill, or 1.3%.

## The arithmetic

| | measured |
|---|---|
| A token's KV | 2 × 32 layers × 8 KV heads × 128 dims × 2 bytes (fp16) = **131,072 B**, 128 KiB |
| One 2,048-token request | **256 MiB** of KV |
| Best card-to-card path | **host bounce, 7.32–7.54 GB/s** → **35.6–36.7 ms** |
| What CUDA's own copy gets | **peer copy, 3.68–3.75 GB/s** → **71.5–73.0 ms** |
| Prefill of the same request | **490.7 ms** (the engine's own timer, one request at a time) |
| Transfer as a share of prefill | **7.3–7.5% bounced, 14.6–14.9% peer-copied, 13.2% under contention** |

Those are each class's typical pair; the slowest pair of a class runs 1–4% behind it, and every
figure both ways is in [`report.md`](report.md), which `cmd/disagg` rebuilds from the rows here.

The share barely moves with length: bounced, **8.4% at 256 tokens and 6.5% at 7,936**. Both halves
grow close to linearly, so there is no request length at which this argument changes.

## Why the margin is thinner than predicted

Two measured facts, each worth about half of it:

- **No pair of cards can reach another directly.** The driver reports peer access unsupported on all
  30 ordered pairs ("CNS, chipset not supported" — [`p2p-read.txt`](p2p-read.txt)), so every
  card-to-card copy goes out to host memory and back. §8's 12–13 GB/s was a direct-link figure; what
  this host gives is one card's own link, **7.87 GB/s in and 8.50 GB/s out** — and a bounce is two
  such legs pipelined, which lands at 7.32–7.54 at request size, rising to 7.48–7.69 for the largest
  copies measured.
- **Prefill is three times faster than §8 assumed.** 490.7 ms for 2,048 tokens, not ~1.5 s.

A third fact costs nothing here but would cost a naive implementation half its bandwidth: **CUDA's
own card-to-card copy runs at 3.7 GB/s where a hand-pipelined bounce through pinned host memory runs
at 7.4**. The driver's staging is not overlapped; chunking it is.

## What "under load" means here, and what it does not

The links were never read idle: NVML sampled **gen 3 x16** throughout every timed group, while the
same cards sit at gen 1 idle ([`pcie-tree.txt`](pcie-tree.txt)). Three conditions, and they load
different things:

| condition | what is loaded | what it showed |
|---|---|---|
| `alone` | nothing else | the figures above |
| `busy` | both cards' own memory, by a device-local copy loop at 406 GB/s or more | costs ~1%: the link does not care that the card is busy |
| `together` | the links, by every disjoint pair of one class copying at once | **NODE and SYS halve, to 4.16 GB/s a pair** |

**Only `together` puts traffic on PCIe.** The `busy` loop stays inside a card's own memory, so it is
evidence that a busy card still moves bytes at full rate, not evidence about a busy link. Nothing
here ran against a fleet actually serving inference.

The halving under `together` is structural, and [`pcie-tree.txt`](pcie-tree.txt) shows why: cards
(0,1), (2,3) and (4,5) each sit behind one PCIe switch with a **single gen 3 x16 uplink** to its CPU
root port. Two cards of one pair sending at once split that uplink. PIX pairs do not halve, because
each such pair's two cards use their own switch's uplink in opposite directions.

`busy` and `together` were measured at one size only — 256 MiB, one 2,048-token request. Every other
length was measured `alone`.

## The decision

**Not worth building.** Not because of bandwidth, but because bandwidth was the only objection this
measurement could remove, and the ones it cannot touch are the expensive ones:

- **Halved decode capacity.** Splitting five cards into prefill and decode groups leaves fewer cards
  decoding, which is where this fleet's goodput is won.
- **An extra hop and the scheduling that comes with it**, for a 36.7 ms saving on a 490.7 ms prefill
  that the receiving card must then be free to take.
- **The build itself.** §8 puts a real KV transfer path on 3090s at multiple weeks, and this host has
  no peer access to build it on, so it would be host-staged — the 7.4 GB/s path measured here.

One more measured reason, and it is the sharpest: **prefix caching shrinks the prefill but not the
KV.** At turn 4 of the frozen workload the engine prefills only that turn's new tokens, while a
disaggregated design must ship the whole conversation's KV unless the decode card already holds it.
On the figures here that is 36.7 ms of transfer against the 124.1 ms it costs to prefill 512 new
tokens — **30%**, against the headline's 7.5%. Disaggregation and prefix-cache-aware routing pull in
opposite directions, and this project is about the second.

What would change the verdict: a host with real peer access (or NVLink), a model whose prefill is
long relative to its KV, or an implementation that overlaps the transfer with the prefill layer by
layer, which would hide most of the 36.7 ms rather than paying it afterwards.

## Files

| file | what it is |
|---|---|
| [`report.md`](report.md) | the arithmetic and every measured figure, rebuilt by `make disagg` |
| `bandwidth-node0/`, `bandwidth-node1/` | every timed copy, one row each, and each pass's own conditions — **the system of record** |
| [`prefill.jsonl`](prefill.jsonl) | 60 prefill requests: 6 lengths × 10, with the engine's own timings |
| `prefill-meta.json` | the prefill run's conditions; written only at its end, so a partial run cannot read as whole |
| [`topology.txt`](topology.txt) | `nvidia-smi topo -m` verbatim: which class each pair is |
| [`pcie-tree.txt`](pcie-tree.txt) | each card's bridges up to its root port, from sysfs, with the idle link speeds |
| [`p2p-read.txt`](p2p-read.txt), [`p2p-write.txt`](p2p-write.txt) | the driver's peer-access matrices |
| [`model-config.json`](model-config.json) | the model's own dimensions, which a token's KV is derived from |
| `engine.log`, `measure.log` | the replica's startup log, and the run's own log |

Two passes of the bandwidth measurement, one per NUMA node the host buffer was bound to, because a
SYS pair's bounce crosses the socket link on one leg whichever node holds the buffer. Both passes
were clean: no foreign process held any card, and every page of the host buffer landed on the node
the pass names.

## Reproducing

On the box, with all six cards free (it takes ~17 minutes and leaves them free):

```sh
./ops/pcie/measure.sh runs/pcie-<date>
```

Then, from a checkout with the rows in place — no fleet and no GPU needed:

```sh
make disagg DISAGG_DIR=docs/measurements/2026-09-11-pcie-arithmetic
```

## Conditions

vLLM 0.28.0, `Meta-Llama-3.1-8B-Instruct-AWQ-INT4`, all engine settings from `ops/versions.env`;
driver 595.84, torch 2.13.0+cu130, CUDA 13.0; 6× RTX 3090. The prefill ran on replica-1 alone, at
concurrency 1, every prompt sent as fresh token ids so the prefix cache held none of it (ADR-0004) —
the engine reported 0 cached tokens on all 60 requests, and none was excluded. Bandwidth was
measured on all six cards, GPU 3 included: it is out of the fleet for thermal throttling under
simultaneous load (#25), which is about sustained compute, not about what its link carries.
