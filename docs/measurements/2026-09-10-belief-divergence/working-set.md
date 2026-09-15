# Belief divergence

The gap between what the router believed the chosen replica held of a prompt
and what the engine says it actually served out of cache, per request.

- Prediction: the router's prefix match, in bytes, converted at each request's own
  prompt bytes per token — both sides of that ratio are on the row. Exact residency
  predicts in the engine's own tokens and is read as it stands.
- Truth: `usage.prompt_tokens_details.cached_tokens`, the engine's own per-request
  account. `vllm:request_prefill_kv_computed_tokens` is the same quantity as a
  histogram and carries no request id, so it cannot be joined to the prediction
  it would check.
- Over-prediction and under-prediction are never netted. Believing in blocks a
  replica evicted misroutes the request; forgetting blocks it still holds only
  forfeits a match. They are different failures and share no column.

Measured over 12 cells in runs/divergence/ws0.25, runs/divergence/ws1, runs/divergence/ws3, runs/divergence/ws8: 19391 of 19391 measured requests carried an engine
account of their prompt.

Overall: 19391 requests, 99.7% of belief honoured; over-predicted on 1531 (167810 tokens), under-predicted on 17845 (702696 tokens)

Computed prefill, two ways: 7730857 tokens summed off the per-request rows, against
7770437 off the fleet's own counters over the same cells' measured windows. They
cover slightly different windows and are printed rather than reconciled; a gross
disagreement means the per-request account is measuring something else.

## By policy

| policy | requests | predicted/req | actual/req | honoured | over-predicted | mean tokens over | under-predicted | mean tokens under | exact |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| prefix_affinity | 19391 | 2640 | 2668 | 99.7% | 1531 | 110 | 17845 | 39 | 15 |

## By working set ratio

The pressure axis that drives eviction: a fleet that cannot hold every session
at once is one whose replicas are dropping the blocks the index still believes in.

**These are nominal labels and they overstate nothing but themselves.** The
generator sizes prompts at a declared bytes-per-token that the engines do not
agree with, so the ratio a cell is labelled with is not the pressure it applied —
ADR-0007 measures the gap and is explicit that runs under it must not be
published as WS 1.0. The error is identical across cells, so the *shape* of the
curve below is sound and only its x-axis is mislabelled. The measured ratio each
cell actually ran at is on the cell record, and skew is printed beside the label
because it decides how much of the pool a cell of finite length ever touches.

`unstated` is a cell whose workload stated no WS point, which is an absence rather
than WS 0 — pass `-kv-capacity` to the sweep and the ratio is derived without
changing a byte of what it sends.

| WS | requests | predicted/req | actual/req | honoured | over-predicted | mean tokens over | under-predicted | mean tokens under | exact |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 0.25 (skew 0) | 10626 | 3021 | 3037 | 100.0% | 951 | 4 | 9662 | 18 | 13 |
| 1 (skew 0) | 3689 | 2385 | 2419 | 98.9% | 317 | 314 | 3370 | 67 | 2 |
| 3 (skew 0) | 2639 | 2067 | 2114 | 99.2% | 169 | 246 | 2470 | 67 | 0 |
| 8 (skew 0) | 2437 | 1990 | 2036 | 99.5% | 94 | 241 | 2343 | 57 | 0 |

## By time since the session was last served

How stale the belief was when it was acted on. Derived from the rows rather than
recorded by the router — a request's age is its own start minus the end of that
session's previous turn in the same cell — so it costs the routing path nothing
and is recomputable from the record. `first turn` is a request whose session nothing
had served yet: there is no belief to have gone stale, so a match there came from
a shared system prompt or a branched ancestor instead.

| since last served | requests | predicted/req | actual/req | honoured | over-predicted | mean tokens over | under-predicted | mean tokens under | exact |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| first turn | 1365 | 392 | 398 | 97.7% | 155 | 79 | 1210 | 17 | 0 |
| <1s | 15718 | 3069 | 3100 | 99.7% | 1192 | 110 | 14511 | 43 | 15 |
| 1s-2s | 190 | 1164 | 1179 | 100.0% | 20 | 4 | 170 | 17 | 0 |
| 2s-5s | 448 | 1139 | 1155 | 100.0% | 28 | 4 | 420 | 18 | 0 |
| 5s-10s | 519 | 1150 | 1166 | 100.0% | 46 | 4 | 473 | 18 | 0 |
| 10s-30s | 676 | 1156 | 1165 | 99.4% | 55 | 81 | 621 | 17 | 0 |
| 30s-1m0s | 267 | 903 | 889 | 92.8% | 26 | 670 | 241 | 56 | 0 |
| 1m0s-2m0s | 195 | 384 | 417 | 97.2% | 8 | 259 | 187 | 46 | 0 |
| >=2m0s | 13 | 561 | 577 | 99.8% | 1 | 15 | 12 | 19 | 0 |

## By spill point

`off` is the reference that declines nothing, and it is the only population the
node cap is calibrated from. Spill diverts exactly the requests that would have
tested the index's best-match belief, and the row then records the *target's*
match rather than the declined replica's — so a threshold's row says what the
index was worth on the requests the rule left alone, not what it believed.

| spill | requests | predicted/req | actual/req | honoured | over-predicted | mean tokens over | under-predicted | mean tokens under | exact |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| off | 19391 | 2640 | 2668 | 99.7% | 1531 | 110 | 17845 | 39 | 15 |

## The index's own bounds

The index reached 16311 of its 16311 nodes. The cap bound, so it is a candidate for what
the over-prediction above is measuring, and `calibrate -divergence` will resize it.
