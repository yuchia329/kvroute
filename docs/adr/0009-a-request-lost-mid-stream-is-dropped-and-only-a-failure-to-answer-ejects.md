# ADR-0009: A request lost mid-stream is dropped, and only a failure to answer ejects

**Status:** Accepted · **Date:** 2026-09-10

## Context

#19 gives the router health tracking, ejection, drain and reroute. Most of that is mechanism, but
four of its decisions change what a recorded number means, and those are recorded here rather than
left to code comments.

Two things were wrong before it, in the direction that flatters the fleet:

1. **A request whose replica died mid-stream was booked as failed** — "a replica accepted and then
   errored" — by both the router and the harness. No replica answered it with an error; it lost its
   replica. idea.md §7 and #19's criterion 4 both call it dropped.
2. **A stream that stopped short was booked as a success.** When the router lost its upstream
   mid-response it *ended* the response to the client cleanly, and the harness, which never checked
   for the `[DONE]` every engine stream ends with, read a truncated answer as a complete one — and
   put its latency in the percentiles.

## Decision

**A request lost after its first byte is dropped.** The router aborts the client's connection
rather than ending the response (`panic(http.ErrAbortHandler)`), so the client cannot mistake a
cut-off stream for a finished one, and books the row dropped. The harness books a broken stream as
dropped, and an event stream that ends without `[DONE]` as dropped too, whatever produced it.
CONTEXT.md's *dropped request* is widened to say so. The failure rate is unchanged, because it
already sums dropped and failed.

**The reroute boundary is the first byte of the replica's body**, and on vLLM that byte is the first
token: the chunk announcing the assistant's role goes out in the same engine step as the first
generated token, so #19's "not yet emitted a first token" and the router's "before the first byte"
are one boundary. Until then the router writes
nothing to the client — it holds back even the status line, which vLLM sends long before its first
token — so a replica that dies before that byte has emitted nothing, and the request is sent to
another replica without the client being able to tell. After it the request belongs to its replica.
Mid-stream continuation, idea.md §7's flagged option, is not built; if it ever is, it needs the
splice-correctness test #19 names before it can ship.

**Only a failure to answer ejects a replica, and it is never an error status.** A refused
connection, a reset, or a stream that breaks is evidence the process is gone, and one such request
ejects the replica at once (the passive half); two failed health checks in a row do the same when no
traffic is flowing (the active half), and two passed checks readmit it. A replica that answers with
a 503 is overloaded, not gone: it is neither ejected nor rerouted from, and its error reaches the
client as it was sent. Ejecting on errors would take busy replicas out of rotation *because* a cell
was loading them — differently under each policy, since each loads the fleet differently — and
rerouting them would hide an overloaded fleet behind another replica's answer.

**Drain and ejection are separate, and neither undoes the other.** A drain is an operator's
decision and only the operator restores it; an ejection is the health tracking's and reverses itself.
A drained replica that is stopped and restarted passes its health checks and must still stay out.

**A cell during which the router ejected a replica is flagged.** Health checks run in every router,
including under ADR-0007's frozen comparison, and a false ejection mid-cell would change the fleet
the cell measured without anything in its figures saying so. The harness reads the router's ejection
count before and after every cell, and flags any cell where it moved.

## Consequences

- Cells recorded before this booked a mid-stream break as failed and a truncated stream as a
  success. The sweeps they came from ran with no replica deaths, so neither case is expected in them,
  but a re-summarised old cell keeps the outcome its rows were written with.
- Every router now sends each replica one `/health` a second. That is negligible beside the
  requests it serves, and a replica missing one check under load is not ejected.
- A drop count is only a claim beside the reroute count. The chaos report never prints one without
  the other, or without the reason the figure is what it is.
