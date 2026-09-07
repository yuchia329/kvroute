# ADR-0003: The SLO is three times the measured latency floor

**Status:** Accepted · **Date:** 2026-09-07

## Context

Goodput — requests per second that met the SLO — is this project's primary metric, so the SLO
threshold decides the headline number. Pick it loosely and every policy passes; pick it tightly and
none does; pick it at all, in advance, and the comparison measures whoever picked it.

`idea.md` §6 settles the direction: derive the threshold from the measured concurrency-1 floor
rather than choosing it a priori. That leaves three questions it does not answer — what exactly the
floor is, what statistic of it to multiply, and by how much.

## Decision

**The floor is the pooled median at concurrency one, measured directly at each replica.** Each of
the six replicas is driven on its own, one request at a time, with no router in the path, and the
successful rows from all six are pooled. Measured on 2026-09-06: TTFT p50 320 ms, inter-token p50
7.7 ms.

Pooled rather than taken from the fastest replica, because an SLO derived from the best card would
be unmeetable on the worst and every replica serves the same traffic. Directly rather than through
the router, because the router's policy decides where a request goes and its overhead — 135 µs, and
reported separately — is not what the floor is about.

**The multiplied statistic is the median, not the tail.** The floor is what the hardware costs with
nothing in the way. At concurrency one the p99 already contains this fleet's own jitter, and
deriving from it would build that jitter into the threshold, hiding exactly the degradation the SLO
exists to catch.

**The multiple is three, and it is stated rather than tuned.** The SLO has to sit far enough above
the floor that ordinary queueing does not trip it and close enough that a request nobody would wait
for does. Three times an idle fleet's latency is that line: at the measured floor it puts TTFT just
under a second, which is roughly where an interactive client stops feeling answered.

**Both thresholds round up** — to 10 ms for TTFT and 1 ms for inter-token latency — so the derived
figure never undercuts the multiple it claims and is quotable without a decimal tail.

**The multiple is published beside the floor and beside the alternatives.** The floor is a
measurement and the multiple is a judgement, so the report carries the thresholds at 2×, 3×, 4× and
5× with the chosen one marked. A reader who disagrees with the judgement can see what they are
disagreeing with.

## Consequences

- The SLO is **960 ms TTFT and 24 ms inter-token p50** for this fleet, and it moves if the hardware
  does. It is not a constant anywhere in the code: `cmd/bench` defaults both flags to zero and a
  cell run without them records that no SLO was applied, rather than reporting a goodput that was
  never checked against anything.
- The concurrency-1 cell necessarily runs before the SLO exists. Cells are therefore resummarised
  from their own rows when the SLO arrives, rather than re-run — see [ADR-0002](0002-jsonl-during-parquet-after.md).
- A floor measured against a warm prefix cache would be seven times too low and would drag the SLO
  down with it, so the floor refuses to be used unless the engine's own counters say its prompts
  were actually prefilled. See [ADR-0004](0004-every-measurement-sends-unseen-bytes.md).

## Alternatives considered

**An absolute threshold** — "TTFT < 1 s" — is the industry-conventional choice and was rejected
because it measures the person who chose it. If the fleet's floor were 900 ms the threshold would
be unreachable, and if it were 50 ms it would be free; neither would be visible in the number.

**Deriving from the p99 of the floor** would fold the fleet's own concurrency-1 variance into the
threshold. The floor's job is to say what the hardware costs, not what it costs on a bad day.

**A per-replica SLO** — each replica judged against its own floor — was rejected because it would
let a slow replica grade itself on a curve, and because a client does not know which replica served
it.
