# ADR-0014: Warm-up drift is two-sided, compared per turn index, and split on arrivals

**Status:** Accepted · **Date:** 2026-09-13 · **Amended:** 2026-09-14 (a fourth cause, past saturation, which keeps its goodput; and recorded cells are written back once re-scored) · **Amended:** 2026-09-14 (thin indices, partial visits, and the closed-loop split)

## Context

A warm-up length cannot be chosen in advance and then trusted, so a cell carries a check on its
own: TTFT p50 early in the measured window against TTFT p50 late, flagged past a threshold. The
first version of that check made three assumptions, and #33 found all three wrong against the 216
open-loop cells `docs/measurements/` already held.

**It tested one direction.** `s.WarmupDrift > threshold` fires only on a first half that was
*slower*. Three of #29's re-run cells recorded −0.571, −0.690 and −0.562 — a second half more than
twice as slow — and were published carrying no flag at all.

**It split the wrong window.** The midpoint was taken over first-request-start to
last-response-*end*, so it sat half the cell's drain past the arrival midpoint. The drain is not a
rounding error under the open-loop driver, which fires for its duration and then waits out whatever
is in flight: across those 216 cells it reaches **4 m 13 s** past the last arrival, and in **49 of
them the midpoint fell so far past the last arrival that the late half held fewer than five
successes**. Below that floor the check returns zero, so those cells recorded `+0.000` and no flag.
It was not lenient there; it was blind, at the load levels the headline goodput is taken at.

**It compared two populations the workload had made different.** The rotation maps the k-th arrival
to turn `k/pool`, so the turn index is the round: the whole conversation pool advances together and
rolls over every `TurnsPerSession` rounds. TTFT is therefore periodic with the **visit period**,
`TurnsPerSession × think time`, and a window holding a fractional number of those periods offers its
two halves different turn indices — for as long as the cell runs and however warm the fleet is.
That is what flagged all six of #17's recency cells, and the flag's own remedy, "lengthen the
warm-up and re-run", is the one fix that cannot work for it. #29 answered it by deriving a cell
geometry whose periods come out whole, which works and is per-configuration arithmetic every future
open-loop run has to repeat and can get silently wrong.

## Decision

**Drift is compared within each turn index, and combined request-weighted.** `Result.Turn` is the
workload's own turn position and every row in the project carries it, so `p50(early_t)` against
`p50(late_t)` per index cancels the composition effect for any cell length, any warm-up and any
rate, with no geometry to derive. Request-weighted rather than the largest of the per-index
figures: the maximum would hand a cell's verdict to whichever index carried the fewest requests,
and the thin strata are the noisy ones.

**The period is stated by the workload, not inferred from the rows.** The check reads turns per
session off the workload's name — the run's only record of what it sent, and therefore the only
thing a later re-score can read it from. Deriving it two ways would let a cell be judged one way
when it ran and another way when it is checked. It is not inferable in any case: a closed-loop
fixed-workload cell at concurrency 256 has its virtual users spread over half a dozen turn numbers
whose spans overlap exactly as a four-turn workload's indices do, and a rule that guessed from the
rows read fourteen such cells as four-turn workloads missing three of their visits. A workload that
states no turns per session — the fixed workload, whose index is a round counter that never comes
back — is pooled instead, which is the right comparison for a workload whose turns are all the same
size.

**The split is the midpoint of the arrival window**, the same window the cell's rates are divided
by, and a row's place in it is its due time rather than its start. One `cellWindow` type derives
both, so the denominator and the split cannot come to describe different spans. *(Amended
2026-09-14.)* A closed-loop cell has no schedule, and its split is the midpoint of first request
sent to last request sent, each row placed by when it was sent. That is not the span its rates are
divided by — a closed loop offers load while it waits for answers, so that span runs to the last
response — and the split cannot run there, for the reason the arrival window exists: at 256
virtual users one latency is tens of seconds, and the midpoint would sit half of it late.

**The threshold is tested against `|drift|`, and the direction is in the message.** A cell whose
TTFT p50 doubled across its window is as unpoolable as one that halved. They are different faults:
one is a warm-up that was too short, the other a fleet that degraded, and re-running the second with
a longer warm-up measures the same decline from a worse start.

**The flag names which of four causes it found rather than prescribing one remedy.** A still-cold
opening period (lengthen the warm-up); a fleet that slowed as the cell ran (find what degraded — a
throttled card, a replica lost, a cache growing); an open-loop cell past saturation (nothing to
fix — see below); or a measured window that is not a whole number of visit periods (re-run over a
whole, even number of them). The last is reported whenever one of the workload's turn indices is missing from a half of
the split, whatever the drift came to, because that makes *every* percentile in the summary a mix
no cell of another length shares — not only the drift. *(Amended 2026-09-14.)* Missing means
*offered* on only one side, counting every outcome. An index offered on both sides that came back
with too few successes on one to have a median is **thin**: it is left out of the figure and
counted, and flags nothing, because the fleet failing its requests says nothing about the window's
length. And a closed-loop cell has no rotation and so no visit period, so the same finding there
is named **partial visits** — its virtual users did not get far enough through their conversations
for every turn to land in both halves — and its fix is a longer cell, not a whole number of
periods nobody set.

**An open-loop cell that fell behind its offered load is past saturation, and keeps its goodput.**
*(Amended 2026-09-14.)* Two-sided, the check flags every cell offered a rate past its policy's knee,
because such a cell's queue grows for as long as arrivals keep coming and its TTFT moves whichever
way the split falls. Excluded as a broken measurement, those cells would blank the three-policy
table for both cache-blind policies from rate 8 up — the collapse the table exists to show, turned
into a gap that reads as "did not run". So each cell records its **backlog**, the share of its
offered requests still unanswered when its arrival window closed, and a cell whose TTFT moved past
the threshold with a backlog over **20%** is flagged *past saturation* instead of cold or degrading.
The comparison treats that flag as it treats a failure rate past the threshold — a measurement of a
fleet falling over, kept in the goodput, marked ⚠ and named — and leaves the cell out of the TTFT
percentiles, which never settled. The line sits in the gap the 216 recorded cells leave: every one
whose TTFT moved past the threshold had a backlog of 16% or less, or 28% or more, and the second
group is exactly the cells offered a rate past their policy's knee. A closed-loop cell has no
backlog, because its virtual users wait for their answers and offer only what the fleet serves.

**What the check computed is recorded beside the number it computed.** `warmup_drift_basis` says
whether each index was compared with itself or the successes were pooled, and
`warmup_drift_turns_compared` / `warmup_drift_turns_confined` say how the split divided the
workload. A drift figure means different things under the two bases, and a record that carried only
the figure would leave that to be inferred from a workload name.

**A recorded cell is re-scored first, and written back only after the re-score is published.**
`cmd/rescore` reads a recorded cell's rows back and puts the current check's verdict beside the
recorded one, writing nothing, because an edit in place before that report exists would erase the
thing it reports on. *(Amended 2026-09-14.)* Once it is published, `rescore -write` puts the
current verdict into the records — the drift fields, the backlog and the flags, and nothing else —
so every table and figure read from them judges the cells by the check the project now holds. It
refuses to write anything unless every cell reproduces its record's numbers and every flag beside
the drift check's, and it does not fill in columns added after a cell ran.

## Consequences

- **118 of the 216 recorded open-loop cells change verdict**, published in
  [`docs/measurements/2026-09-13-warmup-drift/`](../measurements/2026-09-13-warmup-drift/). 26 were
  flagged, 111 are now. Every one of the 216 reproduced its recorded requests, successes, window,
  goodput and TTFT percentiles exactly, so only the drift fields moved.
- **Cells past the knee flag by construction, and that is the intended reading.** 99 of the 216 go
  from clear to flagged on the two-sided test alone, and the verdict is monotone in offered rate:
  none below 6 req/s, all 36 at 20 req/s and up. 97 of them are past saturation, and keep their
  goodput in every comparison; the other two are #29's recency cells, which slowed while answering
  all but 2% of what they were offered.
- **The 216 open-loop records carry the current verdict, and the closed-loop records do not yet.**
  Both open-loop comparisons and the figures are regenerated from the rewritten records. The 300
  closed-loop records keep the verdicts they were recorded with, because three of their fifteen
  changed verdicts name a fractional number of visit periods on closed-loop cells, where the visit
  period has no meaning, and that label is corrected before they are written.
- **A window shorter than one visit period still pools.** The check cannot compare an index the
  window never offered twice. Such a cell reports every missing index as confined to one half, which
  is the right verdict, but its drift figure rests on whatever indices did appear.
- **The geometry derivation in `internal/bench/recencywindow_test.go` is reduced rather than
  retired.** It is no longer needed to keep the check from false-flagging, but a measured window of
  whole visit periods is still what makes two cells of different lengths comparable, and that
  property can only be checked against the schedule before a card is booked.
