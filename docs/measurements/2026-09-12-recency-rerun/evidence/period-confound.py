#!/usr/bin/env python3
"""Is the recency decay a within-period fact, or an artefact of pressure drifting across the cell?

    python3 period-confound.py runs/recency-rerun/think75 75

think75's two measured visit periods differ in TTFT p50 by a factor of two to
three: the pool is 1.95x the fleet's KV, so eviction pressure accumulates as more
distinct sessions pass through. The warm-up-drift check does not fire on it -- the
cell gets slower, not faster -- but it raises a different question than the flag
does. If the later period is under more pressure AND contributes unevenly to the
recency buckets, then the curve would be mixing recency with pressure, which is
exactly the confound the single-cell design exists to exclude.

So the curve is recomputed inside each measured visit period separately. If the
decay is there in both, it is a within-period fact and the trend across periods
does not reach it.

honoured = (predicted - over) / predicted, as internal/prefix.Divergence computes it.

NOTE ON THE COUNTS. Rows on which the index claimed nothing are skipped here, where
cmd/divergence bins them. A zero claim adds nothing to either side of the honoured
ratio, so the SHARES below are comparable with the report's; the n columns are not,
and are smaller -- most of the difference falls in the buckets past the TTL, which is
exactly where the index stops claiming.
"""
import glob
import json
import os
import sys

TURNS_PER_SESSION = 4
BOUNDS = [
    ("<1s", 1), ("1s-2s", 2), ("2s-5s", 5), ("5s-10s", 10),
    ("10s-30s", 30), ("30s-1m", 60), ("1m-2m", 120),
]


def bucket(age_s: float) -> str:
    for label, upper in BOUNDS:
        if age_s < upper:
            return label
    return ">=2m"


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    base, think = sys.argv[1], float(sys.argv[2])
    period = TURNS_PER_SESSION * think

    # label -> period index -> [predicted, over]
    totals: dict[str, dict[int, list[float]]] = {}
    counts: dict[str, dict[int, int]] = {}

    for record_path in sorted(glob.glob(os.path.join(base, "cells", "*.json"))):
        rows_path = record_path[: -len(".json")] + ".jsonl"
        if not os.path.exists(rows_path):
            continue
        rows = [json.loads(line) for line in open(rows_path)]
        rows.sort(key=lambda r: r["started_at_ns"])
        start = min(r["started_at_ns"] for r in rows)

        # A request's age is its own start minus the end of that session's
        # previous turn in the same cell -- the definition the report uses. Warm-up
        # rows are walked so they can supply a predecessor, but never scored.
        # Kept exactly as internal/bench/divergence.go keeps it: advanced only on a
        # SUCCESSFUL turn, and with max() rather than assignment. At skew 1.0 a hot
        # conversation is held by several pool slots at once and its turns overlap,
        # so a plain assignment lets the predecessor move BACKWARDS and ages the
        # next turn by tens of seconds -- enough to carry a row across the 57 s TTL
        # in the very table this script exists to check.
        last_end: dict[str, int] = {}
        for r in rows:
            session = r.get("session") or ""
            prior = last_end.get(session)
            success = r.get("outcome") == "success"
            if session and success:
                end = r["started_at_ns"] + (r.get("total_ns") or 0)
                last_end[session] = max(last_end.get(session, 0), end)
            if r.get("warmup") or not success:
                continue
            if prior is None:
                continue
            label = bucket((r["started_at_ns"] - prior) / 1e9)
            p = int((r["started_at_ns"] - start) / 1e9 // period)
            # The prefix index predicts in BYTES -- prefix_match_tokens is only
            # populated by exact residency, which predicts in the engine's own
            # tokens. So the prediction is converted at this request's own prompt
            # bytes per token, both sides of which are on the row, which is what
            # the report does.
            # divergence.go books a row with no engine cached-token account as
            # Unaccounted rather than scoring it; without this a replica that
            # stopped reporting prompt_tokens_details reads here as 0% honoured
            # and manufactures the decay the table is testing for.
            if not r.get("engine_cache_read"):
                continue
            match_bytes = r.get("prefix_match_bytes") or 0
            prompt_bytes = r.get("prompt_bytes") or 0
            prompt_tokens = r.get("engine_prompt_tokens") or 0
            if match_bytes <= 0 or prompt_bytes <= 0 or prompt_tokens <= 0:
                continue
            predicted = match_bytes * prompt_tokens / prompt_bytes
            actual = r.get("engine_cached_tokens") or 0
            over = max(0, predicted - actual)
            slot = totals.setdefault(label, {}).setdefault(p, [0.0, 0.0])
            slot[0] += predicted
            slot[1] += over
            counts.setdefault(label, {})[p] = counts.setdefault(label, {}).get(p, 0) + 1

    periods = sorted({p for d in totals.values() for p in d})
    print("honoured share of belief, per recency bucket, per visit period (%.0fs)" % period)
    print("%-10s %s" % ("bucket", "".join("   p%-16d" % p for p in periods)))
    for label, _ in BOUNDS + [(">=2m", 0)]:
        if label not in totals:
            continue
        cells = []
        for p in periods:
            if p not in totals[label]:
                cells.append("%-19s" % "-")
                continue
            predicted, over = totals[label][p]
            honoured = (predicted - over) / predicted if predicted else 0
            cells.append("%6.1f%% (n=%-6d)  " % (honoured * 100, counts[label][p]))
        print("%-10s %s" % (label, "".join(cells)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
