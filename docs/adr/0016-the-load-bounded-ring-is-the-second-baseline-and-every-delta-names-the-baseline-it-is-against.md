# ADR-0016: The load-bounded ring is the second baseline, and every delta names the baseline it is against

**Status**: accepted, 2026-09-19
**Ticket**: [#46](https://github.com/yuchia329/kvroute/issues/46), for the measurement in
[#36](https://github.com/yuchia329/kvroute/issues/36); parent [#35](https://github.com/yuchia329/kvroute/issues/35)
**Amends**: `CONTEXT.md`'s *Session affinity* entry, which said its load-blindness was "not a
defect to be patched". Adds a seventh policy outside idea.md §5's five; ADR-0007's frozen
parameters are unchanged and this policy's cells are recorded under them.

## Context

Every margin this repo publishes for prefix affinity is a margin over **session affinity**, and
session affinity is blind to load on purpose: idea.md §0 predicted that a ring with no escape hatch
would pile hot sessions together, and the policy was kept blind so that the prediction could be
measured. It was. The pressure grid's headline is +66.2% at working set 1, skew 0.

The report's own second finding then says what that margin is made of: the two policies' prefix
cache hit rates differ by 0.0–2.2 points, so the win is **load**, not cache reuse. And load is the
one thing a plain load balancer can already fix without reading a prompt. Consistent hashing with
bounded loads is one setting in Envoy (`hash_balance_factor`) and in HAProxy
(`hash-balance-factor`), and CacheRoute runs exactly that as a baseline. The README says so in
print — *"the margins above are against load-blind stickiness, not against the best a plain load
balancer offers"* — and nothing has measured it.

So the published headline is stated against the weaker of two available baselines, and the
objection is one any reader who has configured a load balancer can raise in a sentence.

## Decision

**A seventh policy, `bounded_session_affinity`, joins the comparison as the second baseline.** It
is session affinity's ring walked clockwise past any replica over an inflight bound, taking the
first replica within it.

Five choices make it a baseline a reader can check against the prior art rather than a policy
invented here:

**The ring is session affinity's, called rather than copied.** The same `ringCache`, the same 128
virtual nodes, the same avalanche, the same supplied session identity — and the same rule for
requests no session could be identified for, which are rotated under `SESSION_UNIDENTIFIED`. On a fleet where nothing is over the bound the two
policies place every session identically, so a difference between their cells is what the bound
did and not a second hashing scheme. This is ADR-0012's argument, one policy over.

**The bound is the published rule.** A replica has room while its inflight is below
`⌈(1 + ε) × (total inflight + 1) / replicas⌉`: the capacity of Mirrokni, Thorup and Zadimoghaddam's
*Consistent Hashing with Bounded Loads*, with the request being placed counted into the mean, which
is also how HAProxy computes it. Counting the request is what makes the bound satisfiable — the
capacities sum to more than the fleet holds, so some replica always has room — and the ceiling is
what keeps a nearly idle fleet from a capacity of zero. The policy still falls back to the ring's
first choice if every replica were over the bound; no accepted bound can reach that branch, and it
is handled rather than asserted so that the alternative is never a dropped request.

**It remembers nothing.** A session the bound moved is walked from its own position again on its
next turn and returns to the replica the ring places it on as soon as that replica has room. That is the algorithm as Envoy and HAProxy
ship it. A variant that remembered deflections would be a session table, would very likely do
better on this workload, and would be a baseline nobody runs — which is the opposite of what this
policy is for.

**ε is 0.25 and nothing else.** It is CacheRoute's setting for the same policy, taken so that a
reader comparing the two tables compares one policy. It is a judgement and not a measurement, so
it is in the class of the spill thresholds and the hash window, not ADR-0006's: never defaulted,
refused when unstated, published on `/router/stats`, checked against the router before the first
cell, and recorded on the cell in its own `inflight_bound` column (#42) rather than in the
workload's name. No other ε is swept. The objection that 0.25 was a bad choice is left open
deliberately (#35, out of scope), and any write-up says so.

**Its two decisions are recorded apart.** `BOUNDED_SESSION_AFFINITY` for a turn the bound let
stand, `BOUND_DEFLECTED` for one it moved — the `PREFIX_HASH` / `HASH_DEFLECTED` split, for the
same reason. The deflected count is also the policy's cost, counted: each is a turn sent away from
the replica holding its history.

**Every published delta names the baseline it is against.** From this ADR on, no margin is
published as a bare figure. "+66.2%" is "+66.2% over session affinity"; the figure beside it, once
#36's grid has run, is the margin over bounded session affinity at the same point. Concretely:

- The README's headline, the pressure map's summary and the regime map state both margins side by
  side wherever they state one, and the headline sentence leads with the margin over the **harder**
  baseline — bounded session affinity — because that is the claim a reader is entitled to.
- Session affinity keeps its rows and its name. Its cells are not re-run, re-judged or withdrawn:
  they measure what they always measured, and the first baseline stays in every table as the
  mechanism §0 predicted. It is never called "the baseline" unqualified again.
- The pressure map is drawn once per baseline (`-baseline session_affinity`,
  `-baseline bounded_session_affinity`), never as one table with an unnamed reference.
- Measurement directories already committed are not rewritten — they are records (ADR-0002, and
  the practice every later measurement has followed). Their READMEs gain a dated note pointing at
  this ADR and at #36's directory, saying their margins are over the load-blind baseline.

**The relabelling lands with #36's cells, not before them.** #46 adds the policy and this ADR;
the README, the pressure map and the regime map are relabelled in the change that commits the
bounded grid's measurement, because the second margin does not exist until then. Until it does,
the README's "not yet measured" paragraph is the label.

**The correction has a stated trigger, set before the cells run.** "Most of the +66%" is #36's
wording; the threshold below is this ADR's, fixed here so that it cannot be chosen after the
number is known. If, at working set 1 / skew 0,
bounded session affinity's median goodput closes **more than half** of the gap between session
affinity and prefix affinity, beyond the repetition ranges of both, then the bound "captures most
of the +66%". In that case a new entry goes under the README's *Results that overturned
themselves* — the headline was a margin over a baseline with no load bound, and most of it belongs
to the bound rather than to the index — and it is published **before** any external write-up
refers to the result, and before any downstream arm of #35 is reported. The two de-risk points of
#36 (WS 1 / skew 0 and WS 1 / skew 1.4) run first so that this is known on day one.

If the bound closes less than half, the headline stands with both margins stated, and the README's
"not yet measured" paragraph is replaced by the measurement.

## Consequences

**The comparison gets harder to win, on purpose.** Prefix affinity now has to beat a policy that
balances load and keeps most session locality, with no index, no calibration and no TTL. If it
still wins, the claim becomes the one worth making: a KV index beats a load-bounded hash. If it
does not, the finding is that at this scale the index's cost buys what one load-balancer setting
buys, and that is published with the same prominence.

**`policy.Order` grows to seven, and the bounded policy sits directly after session affinity.**
The two baselines read as the pair they are, and the comparison table, the regime map, the
recovery comparison and the overhead figure pick the policy up from `Order` without being told.
Reports built from directories that hold no bounded cells are unchanged: a policy with no cells
has no row.

**The decision mix gains two columns.** Cells recorded before them read zero in both, which is
correct: no earlier cell ran this policy.

**A deflected turn pays a prefill, and the grid will show it.** At high skew the hot sessions'
home replicas sit over the bound persistently, so their turns are deflected — sometimes to the
same neighbour, which then holds the history too, sometimes not. How much cache the bound costs
is a result of #36, read off the hit-rate column and the `BOUND_DEFLECTED` share, not a property
asserted here.

**It costs fleet time and a cold fleet.** Thirty-six cells at the frozen geometry, run like every
other policy's: fleet down and up before it, points in `bench.PressureGrid()`'s order, five cards
(ADR-0013), contamination and warm-up drift checked as ADR-0014 rebuilt them. Its cells are
compared with the session-affinity and prefix-affinity cells of the same engine configuration
only (ADR-0010).

**It must never be reported as Envoy's, HAProxy's or CacheRoute's implementation.** The rule is
the published one and the bound is CacheRoute's, but the ring, the virtual node count, the hash
and what counts as inflight are this repo's. It is reported as consistent hashing with bounded
loads at ε = 0.25 over this repo's ring, under this repo's name for it.
