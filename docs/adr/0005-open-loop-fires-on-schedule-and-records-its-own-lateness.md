# ADR-0005: The open-loop driver fires on schedule and records its own lateness

**Status:** Accepted · **Date:** 2026-09-07

## Context

Goodput under an SLO is a question about behaviour *at and past saturation*. The closed-loop driver
cannot answer it. It holds N virtual users, each sending its next request only when the previous
response has finished, so the moment the fleet slows the offered load slows with it: the system is
never pushed past its knee and the measured tail is systematically optimistic. That is coordinated
omission, and it is why `idea.md` §"Closed-loop and open-loop" asks for both drivers rather than
one.

An open-loop driver fires on a fixed arrival schedule instead. Offered load becomes an input. Five
things about that are decisions rather than details, because each has a way of being implemented
that looks right and quietly reintroduces the omission the driver exists to remove.

## Decision

**1. Requests in flight are unbounded, and the schedule is computed from the cell's origin.**

Each arrival is dispatched on its own goroutine, so a request the fleet is slow to answer cannot
delay the next dispatch. There is no cap on how many are outstanding: a fleet that cannot keep up
accumulates them, and that accumulation *is* the fact being measured. Capping it — a worker pool, a
semaphore, a "max in flight" safety valve — would be a closed-loop driver wearing an arrival rate.

The k-th request is due at `start + k/rate`, computed from the cell's own start rather than by
accumulating sleeps. A late dispatch therefore cannot push the ones after it later still.

**2. Lateness is recorded per request, and a cell that lost its schedule is flagged.**

Every row carries `scheduled_at_ns` beside `started_at_ns`. The summary reports the p50, p99 and
maximum gap between them, and flags any cell whose p99 lag exceeds 100 ms — far above the host
scheduling wobble a goroutine handoff costs, far below the fleet's own hundreds of milliseconds.

This exists because "the driver held its schedule" is otherwise an assumption. A driver that fell
behind offered less load than the cell says it did, and the goodput computed from it would be a
figure for a rate the fleet was never given — the closed-loop failure mode, reappearing inside the
driver written to avoid it. Recording only the send time would make that invisible; recording only
the due time would hide it in the latencies. Both, and the difference is evidence.

Measured against a fake fleet on the development machine, a 40 req/s cell holds its schedule to a
375 µs median and a 1.6 ms worst case.

**3. A scheduled cell's rates are computed over the window it offered load in.**

A cell fires for its duration and then waits out whatever is still in flight. Past saturation that
drain tail runs well beyond the last arrival, so dividing the requests by the span of the rows would
divide a minute's offered load by a minute and a half and call the result a rate — understating the
headline number by the same mechanism the driver was written to remove. A scheduled cell's window is
therefore its schedule: first arrival due to last arrival due, plus the one inter-arrival gap the
last arrival owns, so *n* arrivals at rate *r* occupy exactly *n/r* seconds. A closed-loop cell keeps
the old window, first start to last finish, because it offers load for as long as it is receiving
responses.

Requests that finished after the window still count in the numerator. They were offered inside it,
and dropping them would let a fleet improve its goodput by being too slow to answer before the cell
ended.

**4. The schedule is even, not Poisson.**

`CONTEXT.md` defines the open-loop driver as firing on a *fixed* arrival schedule, and an even one
is what the project needs from it: it is reproducible between repetitions of the same cell, and its
lateness can be read off a row without reconstructing a generator's state. An exponential
inter-arrival distribution is more realistic about how requests arrive in the world, and it is the
thing to reach for if a result turns out to depend on the smoothness of the offering. It is not
needed to push a fleet past its knee, which is what this driver is for.

**5. Both drivers write one row schema, and every table names the driver.**

`bench.Result` gained `driver`, `arrival_rate` and `scheduled_at_ns` columns; the closed-loop driver
leaves the last two zero, because under it offered load is an outcome and nothing scheduled
anything. One schema means one Parquet file, one summary function and one results table across both
axes — and that is only safe if the driver is on the row and in the table, because a closed-loop
tail and an open-loop tail are not comparable numbers. `bench.Table` gives the driver its own
column, and a cell recorded before drivers were named renders as **unstated** rather than being
guessed at.

## Consequences

- The sweep's load axis is a `bench.Load`: a concurrency under one driver or an arrival rate under
  the other. Cell ids gain a letter — `round_robin-a16-r1` beside `round_robin-c16-r1` — so both
  axes can resume from one directory without colliding.
- ADR-0004's partition of the workload's user space now covers both axes: concurrency levels take
  the low half of a repetition's block and arrival rates the high half, so a cell at 8 req/s and one
  at concurrency 8 never send each other's prompts. The concurrency arithmetic is unchanged, so
  cells recorded before this ADR still resolve to the slice they were run on. Each axis is bounded
  at 512 levels and the sweep refuses anything above, because a run of `-concurrency 520` would
  otherwise land on 8 req/s's slice. Within one sweep the whole partition is checked before the
  first cell runs; the bound is what holds across two sweeps into one directory, which is a
  supported way to run both axes and which no single check can see.
- Arrivals rotate through a pool of conversations — rate times think time, by Little's law — and the
  k-th arrival is the next turn of the k-th of them. This is the same walk a closed-loop virtual
  user makes, deliberately: the two drivers then offer traffic of one shape and differ only in what
  paces it, so a goodput gap between their tables is the pacing, which is what the pair is read for.

  The first version of this gave every arrival its own slot, which was correct for the fixed
  workload and silently wrong for the multi-turn generator that landed after it: measured over 600
  ms, closed-loop produced turns 0–5 evenly and open-loop produced 60 rows all on turn 0. A load
  model that discards the workload's sessions leaves a cache-aware policy nothing to be aware of.
  The pool is bounded by the cell's slice of the workload's user space, and a pool larger than half
  the cell's arrivals is warned about rather than refused, since for the fixed workload — whose
  turns share no prefix — first turns only is honest.

  Think time is five seconds by default, between two bounds. Below a turn's own latency a session's
  next turn would be offered before the previous was answered, which is not a conversation, and the
  measured floor puts a 64-token reply at about 0.8 s with nothing in the way. Above roughly a
  quarter of a cell no session reaches its later turns. Five seconds sits between them at every rate
  on the ladder.
- `cmd/bench -driver open_loop` runs the rate ladder, `-driver both` runs both axes into one
  directory. The default ladder — 4, 8, 12, 16, 24, 32, 48 req/s — brackets the 11.6 req/s the
  bring-up sweep reached at concurrency 8. It is a starting ladder for a fleet measured once, not a
  constant: a run that turns before 12, or has not turned by 48, should move it rather than
  reporting the edge of the range as the answer.
- Goodput at saturation is now measurable, but nothing here says the fleet's SLO is met at any of
  those rates. That is the measurement, and it is what #18's pressure grid consumes.
