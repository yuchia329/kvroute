# ADR-0008: The node cap is calibrated against measured belief divergence

**Status:** Accepted · **Date:** 2026-09-08 · **Amends:** ADR-0006

## Context

ADR-0006 sized the prefix index's node cap to the fleet: `fleet_tokens ×
prompt_bytes_per_token ÷ 64`, the number of 64-byte blocks of prompt the fleet's KV capacity can
hold. It also said plainly what that argument could not reach:

> The node cap is a *model* of the fleet, not a measurement of index accuracy. #17 calibrates it
> against observed belief divergence, which is the check this ADR cannot make: nothing here proves
> a fleet-sized index is the right size, only that it is a size derived from the fleet.

This is that check. It needs a per-request measurement, and the obvious source turns out not to be
one.

**`vllm:request_prefill_kv_computed_tokens` cannot do it.** idea.md §4.3 names it as what prefix
match should be logged against, and it is the right *quantity* — prefill tokens actually computed.
It is a histogram. It carries no request id, so there is no way to put a bucket next to the
prediction that was made for the request that fell into it, and a divergence assembled from a
histogram and a row nobody joined is not a measurement of anything. Over a window it agrees with
the counters the cells already record, and that is all it can be used for.

## Decision

**1. The per-request truth is the response's own usage block.** Every request the harness sends
carries `stream_options: {"include_usage": true}`, and the engine's reply ends with
`usage.prompt_tokens` and `usage.prompt_tokens_details.cached_tokens` for that request alone. The
harness row records both beside the prefix match it already carried, so the prediction and the
truth are two columns of one row.

Three consequences, stated because they are the cost:

- It adds a trailing chunk with no content. `readStream` does not count it as a token, so
  inter-token latency is untouched; only the response byte count moves.
- It adds about forty bytes to every request body. `stream_options` sorts last in the marshalled
  JSON, so every prompt's *leading* bytes — the prefix the index chunks and the engine caches —
  are exactly where they were.
- It changes the bytes nonetheless, so both workload names carry `usage=on`. A cell recorded
  before this and one recorded after did not send the same request, and ADR-0004's whole argument
  is that a comparison whose two sides sent different bytes is not a comparison. The marker is
  what stops their names matching.

**2. Divergence is measured in the engine's units, converted per request.** The index counts
bytes because it has no tokenizer. Both sides of the conversion are already on the row — the bytes
this request sent, and the tokens the engine says they came to — so the ratio is measured on the
very request it is applied to rather than taken off a run-wide average that fits no individual
prompt. CONTEXT.md's prompt bytes per token is the same quantity over a whole run, and the report
prints the two accounts of computed prefill side by side rather than assuming they agree.

**3. Over-prediction and under-prediction never share a column.** ADR-0006 rests on the asymmetry:
believing in blocks a replica evicted pays the full prefill *and* spends the routing decision on a
reason that stopped being true, which is strictly worse than routing on load; forgetting blocks a
replica still holds only forfeits a match. A signed total would report a run that did both equally
as one that did neither.

**4. The cap is the fleet model scaled by the share of belief the engines honoured.** Honoured is
`Σ min(predicted, actual) / Σ predicted` — of the tokens the index claimed, how many were really
there. An index whose claims are honoured half the time is holding twice as much belief as the
fleet is backing, so it models half the fleet.

Three guards, each of them a way the scaling would otherwise be superstition:

- **The fleet model is a ceiling.** Under-prediction is a reason to forget less, not a licence to
  believe in more blocks than the fleet can hold. Honoured is bounded above by one.
- **A divergence that claimed nothing does not resize anything.** Every request under a policy
  that consults no index predicts zero, and scaling by that would shrink the index to nothing on
  evidence from a run that never exercised it. `cmd/divergence` writes only the policies that
  predicted something into the calibration file, and `Calibration` refuses such a reading too.
- **A spilling cell measures the index on a population the spill rule chose.** Declining a match
  sends the request to a replica the index claimed little or nothing about, and the row then
  records that target's match rather than the declined replica's. So the calibration takes the
  spill-off cells alone — #16's grid leads each of its axes with one for exactly this reason. The
  thresholds are still reported beside it, because whether declining matches moves measured
  divergence is a result in its own right.
- **A cap that never bound is not what over-predicted.** The router publishes its index occupancy
  through `/router/stats`, the sweep records it on every cell, and a reading whose index never
  filled its cap leaves the cap alone: what failed there was the TTL's reckoning or the engines'
  own eviction, and shrinking the cap would be treating their failure as its.

`NodeCapSource` names which of these applied, for the same reason `TTLSource` exists: a bound
reported without its provenance is one whose derivation nobody can check.

**5. The reading is carried in a file, like the calibration itself.** `divergence
-calibration-out` writes it and `calibrate -divergence` folds it in. The order is: sweep, measure,
recalibrate, sweep again. The first sweep of prefix affinity necessarily runs against a cap
nothing has measured yet, which is not a defect — it is where the measurement comes from.

## Consequences

- The node cap now moves between calibrations, so a run's own output has to carry it. The router
  already logs its bounds at startup and now logs where the cap came from as well.
- **A cell run before this and one run after are not comparable**, and their workload names now
  differ so the comparison refuses to put them in one table. The 2026-09-08 policy comparison in
  `docs/measurements/` predates it.
- The report's working-set axis is only drawn for cells whose workload stated a WS point. The
  frozen headline workload names a session count on purpose — capacity moves between bring-ups —
  so passing `-kv-capacity` to the sweep is what puts it on the axis, and doing so changes neither
  the workload name nor a byte of what it sends.
- A fleet whose engines do not report `prompt_tokens_details` leaves the rows honestly empty and
  the report says over how many requests it could measure anything. That is a reading of a
  fraction of the run rather than a reading of a run that diverged by nothing.
- Working-set bins carry skew beside the ratio, because skew decides how much of the session pool
  a cell of finite length actually touches — at WS 1 a cell realises 0.97 of its label at alpha 0
  and 0.40 at alpha 1.4. A bin pooling the skew axis would average a several-fold range of realised
  pressure and flatten the curve this measurement exists to draw.
- The working-set labels are nominal, and ADR-0007 records that they are wrong by a constant factor
  for every cell. The report says so rather than printing them bare: the error is identical across
  cells, so the shape of the curve is sound and only the axis is mislabelled.
- Time since the session was last served is derived from the rows rather than recorded by the
  router. The routing path is unchanged, the router holds no per-session state it did not already
  have, and the axis is recomputable from the record — which matters more here than usual, because
  the axis is derived and a derivation nobody can re-run is a claim.
