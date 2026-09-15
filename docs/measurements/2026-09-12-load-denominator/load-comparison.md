# What the load condition compared against

2703 rows, 2361 decisions the condition was evaluated on.

| what the comparison was made against | decisions | share |
|---|---:|---:|
| a single request (the factor is an absolute count) | 2361 | 100.0% |
| an idle replica, floored to one | 2276 | 96.4% |

The busiest replica any of these decisions weighed held 8 requests.

**Most of this run's comparisons were not ratios.** Past half the decisions, a
cell labelled with a factor is measuring "decline any replica holding more than
that many requests", which is a different rule at every rung — and the factor was
settled at one rung (#18, c32, where the minimum was genuinely 0 in 16% of
decisions). See #31.

## What each point would have declined

| factor | against | would decline | share | reaches |
|---|---|---:|---:|---|
| 2.0x | min | 385 | 16.3% | yes |
| 1.5x | mean | 589 | 24.9% | yes |
| 2.0x | mean | 300 | 12.7% | yes |
| 4.0x | mean | 9 | 0.4% | yes |
| 8.0x | mean | 0 | 0.0% | **no — cannot fire at this rung** |

Projected over the fleet this run produced, not predicted: a decline that had
happened would have moved the request and changed what every later decision saw.
It answers which points can fire at this rung, which is what the grid is cut
from. What they are worth is a goodput question and needs the run.
