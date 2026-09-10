
# Belief divergence

The gap between what the router believed the chosen replica held of a prompt
and what the engine says it actually served out of cache, per request.

- Prediction: the router's prefix match, in bytes, converted at each request's own
  prompt bytes per token — both sides of that ratio are on the row.
- Truth: `usage.prompt_tokens_details.cached_tokens`, the engine's own per-request
  account. `vllm:request_prefill_kv_computed_tokens` is the same quantity as a
  histogram and carries no request id, so it cannot be joined to the prediction
  it would check.
- Over-prediction and under-prediction are never netted. Believing in blocks a
  replica evicted misroutes the request; forgetting blocks it still holds only
  forfeits a match. They are different failures and share no column.

Measured over 9 cells in working-set/ws0.25, working-set/ws1, working-set/ws8: 16752 of 16752 measured requests carried an engine
account of their prompt.

Overall: 16752 requests, 99.7% of belief honoured; over-predicted on 1362 (126227 tokens), under-predicted on 15375 (536066 tokens)

Computed prefill, two ways: 5158497 tokens summed off the per-request rows, against
5183567 off the fleet's own counters over the same cells' measured windows. They
cover slightly different windows and are printed rather than reconciled; a gross
disagreement means the per-request account is measuring something else.

## By policy

| policy | requests | predicted/req | actual/req | honoured | over-predicted | mean tokens over | under-predicted | mean tokens under | exact |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| prefix_affinity | 16752 | 2731 | 2755 | 99.7% | 1362 | 93 | 15375 | 35 | 15 |

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
| first turn | 874 | 401 | 411 | 98.2% | 65 | 98 | 809 | 18 | 0 |
| <1s | 13718 | 3141 | 3167 | 99.8% | 1132 | 94 | 12571 | 38 | 15 |
| 1s-2s | 187 | 1163 | 1179 | 100.0% | 19 | 4 | 168 | 17 | 0 |
| 2s-5s | 443 | 1139 | 1156 | 100.0% | 28 | 4 | 415 | 18 | 0 |
| 5s-10s | 506 | 1150 | 1165 | 100.0% | 44 | 4 | 462 | 18 | 0 |
| 10s-30s | 646 | 1156 | 1168 | 99.7% | 50 | 47 | 596 | 17 | 0 |
| 30s-1m0s | 228 | 902 | 908 | 95.5% | 17 | 542 | 211 | 50 | 0 |
| 1m0s-2m0s | 144 | 364 | 403 | 96.1% | 7 | 295 | 137 | 57 | 0 |
| >=2m0s | 6 | 607 | 627 | 100.0% | 0 | — | 6 | 20 | 0 |

## By spill point

`off` is the reference that declines nothing, and it is the only population the
node cap is calibrated from. Spill diverts exactly the requests that would have
tested the index's best-match belief, and the row then records the *target's*
match rather than the declined replica's — so a threshold's row says what the
index was worth on the requests the rule left alone, not what it believed.

| spill | requests | predicted/req | actual/req | honoured | over-predicted | mean tokens over | under-predicted | mean tokens under | exact |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| off | 16752 | 2731 | 2755 | 99.7% | 1362 | 93 | 15375 | 35 | 15 |

## The index's own bounds

The index reached 16311 of its 16311 nodes. The cap bound, so it is a candidate for what
the over-prediction above is measuring, and `calibrate -divergence` will resize it.
