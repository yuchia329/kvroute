# ADR-0013: A thermally throttled cell is recorded, and flagged above half its busy samples

**Status:** Accepted · **Date:** 2026-09-11

## Context

GPU 3 of this host thermally throttles whenever all six cards draw power at once, measured in
[`docs/measurements/2026-09-07-gpu3-thermal/`](../measurements/2026-09-07-gpu3-thermal/) and
written up in #25. It clocked down to 960 MHz while its siblings held 1305, at a *lower* core
temperature than a card that was not throttling at all, and its deficit grew with time on load:
1.1%, then 12.7%, then 17.2% across three repetitions of about a minute each.

The card has since been dropped — the fleet is `0,1,2,4,5` — and that closes the immediate
problem. It does not close the general one. Nothing in a cell's record said the cards had been
throttled, so the defect was invisible for a week and was found only by driving all six at once
on purpose and reading separate telemetry afterwards. A cell already carries evidence about
*who else was on the box*; it carried none about *whether its own cards could keep up with each
other*. Either defect makes a cell's figures describe something other than the fleet the results
claim to be about, and the second one is silent: a throttled replica produces no error, drops no
request, and simply answers more slowly than its siblings.

The two are also independent. CONTEXT.md's *clean cell* is about foreign processes only, and a
cell can be perfectly clean and thermally throttled at the same time.

## Decision

**The probe that samples cleanliness also samples clocks.** `nvidia-smi` is asked for
`clocks.sm` and `clocks_throttle_reasons.active` in the same query that already reads memory and
utilization, so there is one sampler and one answer to "what were the cards doing during this
cell". A second sampler would be a second answer, and the one that drifted would be the one
nobody was reading.

**The evidence is per card, and it is carried in the cell's record** (`throttle` in the cell
JSON, a repeated group in the Parquet): each card's busy-sample count, the share of those samples
it spent on a thermal reason, the share it spent power-capped, the range its SM clock moved in,
and the driver's own names for every reason seen. Per card because this fleet is named card by
card, and because the finding was a *comparison between* cards, not a fleet-wide number.

**Shares are over busy samples only** — the card had non-zero utilization — matching the original
analysis. An idle card reports `GpuIdle` and no thermal reason at all, so counting the fleet's
idle moments would divide a throttled card's evidence by however long the warm-up took.

**A cell is flagged when any of its cards spent more than half of its busy samples thermally
throttled**, alongside the unclean, under-warmed and mid-cell-ejection flags, and is therefore
excluded from pooled figures by the existing rule. Half, because in the measurement the five
healthy cards spent 0–34% of their busy samples on a thermal reason while sitting almost entirely
on the normal, equal power cap, and the defective one spent 67% — more than it spent power-capped.
A card past half its samples is one whose *dominant* limit is heat, which is the signature that
separated GPU 3 from its siblings with margin on both sides. A threshold under 34% would flag
healthy cells under ordinary load, and a flag that fires on every cell stops being read. The
threshold is recorded on the cell beside the verdict, and `-thermal-share-threshold` moves it.

**`SwPowerCap` is recorded and never flagged.** Every card in a loaded fleet is power-capped; that
is the fleet working as intended. The power-cap share sits beside the thermal share precisely so
the comparison between them can be read.

**`HwSlowdown` does not count as thermal.** The driver raises it for heat and for a power brake
alike, so counting it would put power-capped samples into the count that decides whether a card is
the odd one out on temperature. `SwThermal` and `HwThermal` are the two reasons that count.

**Unread clocks are recorded, not flagged.** A driver that answers `[N/A]` leaves `clock_samples`
at zero and the cell unflagged: there is nothing to discard it for, because re-running it would
produce the same silence. This is the distinction `gpu_samples` already draws for cleanliness —
"nothing was found" and "nothing was looked for" must not read the same.

## Consequences

- **Cells recorded before this carry no throttle evidence**, and read as clocks nobody looked at
  rather than as cards that were fine. Everything measured before 2026-09-08 also ran with GPU 3
  in the fleet. The six-card policy comparison in the report is the one published result that
  stands on a fleet that no longer exists, and its caveat stays until it is re-run on the five.
- **The flag can fire on a healthy host.** A hot room or a blocked intake throttles cards that are
  not defective, and the cell is still excluded — correctly, because its figures still describe a
  fleet whose replicas were not interchangeable. The record names the card, the share and the
  clock range, so the cause can be found rather than guessed.
- **It is evidence, not a fix.** The real fix is a uniform `nvidia-smi -pl` power cap at a wattage
  every card sustains, or physical attention to the card; both need box administration this
  project does not have. Until then the fleet's answer is to name the cards it runs on and flag the
  cells where one of them could not keep up.
- **The chaos runs and the characterization probes carry it too.** The together arm of a
  characterization is the only condition in which every card draws power at once, which is the only
  condition this defect appears in — the run that found it had to be re-read off separate telemetry
  because the probes themselves recorded nothing about clocks.
