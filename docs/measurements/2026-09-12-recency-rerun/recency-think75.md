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

Measured over 3 cells in runs/recency-rerun/think75: 14400 of 14400 measured requests carried an engine
account of their prompt.

Overall: 14400 requests, 97.7% of belief honoured; over-predicted on 1192 (732068 tokens), under-predicted on 13195 (1223383 tokens)

Computed prefill, two ways: 11873380 tokens summed off the per-request rows, against
12207748 off the fleet's own counters over the same cells' measured windows. They
cover slightly different windows and are printed rather than reconciled; a gross
disagreement means the per-request account is measuring something else.

## By policy

| policy | requests | predicted/req | actual/req | honoured | over-predicted | mean tokens over | under-predicted | mean tokens under | exact |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| prefix_affinity | 14400 | 2197 | 2231 | 97.7% | 1192 | 614 | 13195 | 93 | 13 |

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
| 3 (skew 1) | 14400 | 2197 | 2231 | 97.7% | 1192 | 614 | 13195 | 93 | 13 |

## By time since the session was last served

How stale the belief was when it was acted on. Derived from the rows rather than
recorded by the router — a request's age is its own start minus the end of that
session's previous turn in the same cell — so it costs the routing path nothing
and is recomputable from the record. `first turn` is a request whose session nothing
had served yet: there is no belief to have gone stale, so a match there came from
a shared system prompt or a branched ancestor instead.

| since last served | requests | predicted/req | actual/req | honoured | over-predicted | mean tokens over | under-predicted | mean tokens under | exact |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| first turn | 699 | 423 | 434 | 98.8% | 78 | 47 | 621 | 18 | 0 |
| <1s | 4173 | 3185 | 3158 | 98.5% | 222 | 894 | 3949 | 22 | 2 |
| 1s-2s | 869 | 2613 | 2639 | 99.7% | 12 | 599 | 854 | 35 | 3 |
| 2s-5s | 1342 | 2744 | 2773 | 99.3% | 42 | 644 | 1298 | 51 | 2 |
| 5s-10s | 1099 | 2719 | 2752 | 99.0% | 62 | 459 | 1037 | 63 | 0 |
| 10s-30s | 2035 | 2520 | 2544 | 97.8% | 143 | 792 | 1889 | 86 | 3 |
| 30s-1m0s | 1799 | 1583 | 1585 | 88.3% | 255 | 1304 | 1543 | 218 | 1 |
| 1m0s-2m0s | 2074 | 481 | 686 | 98.1% | 335 | 56 | 1737 | 256 | 2 |
| >=2m0s | 310 | 440 | 496 | 98.0% | 43 | 62 | 267 | 75 | 0 |

## By spill point

`off` is the reference that declines nothing, and it is the only population the
node cap is calibrated from. Spill diverts exactly the requests that would have
tested the index's best-match belief, and the row then records the *target's*
match rather than the declined replica's — so a threshold's row says what the
index was worth on the requests the rule left alone, not what it believed.

| spill | requests | predicted/req | actual/req | honoured | over-predicted | mean tokens over | under-predicted | mean tokens under | exact |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| off | 14400 | 2197 | 2231 | 97.7% | 1192 | 614 | 13195 | 93 | 13 |

## The index's own bounds

The index reached 16311 of its 16311 nodes. The cap bound, so it is a candidate for what
the over-prediction above is measuring, and `calibrate -divergence` will resize it.
