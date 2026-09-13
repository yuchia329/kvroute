#!/usr/bin/env python3
"""Read a recency cell's rows and say what the drift check saw, and why.

Run on the box, from ~/kvroute:

    python3 period-check.py runs/recency-rerun/think30 30
    python3 period-check.py runs/recency-rerun/think75 75

The re-run's bet is that a measured window of two whole visit periods leaves the
warm-up-drift check comparing the fleet against itself. This prints the two things
that would show it held: the TTFT p50 of each visit period (the sawtooth is
expected to still be there -- it is the workload, not a defect), and the medians of
the two halves the check actually divides, which should now be close.

Whether the cells flagged is on the records; this says which mechanism decided it.
"""
import glob
import json
import os
import statistics
import sys

TURNS_PER_SESSION = 4


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    base, think = sys.argv[1], float(sys.argv[2])
    period = TURNS_PER_SESSION * think

    cells = sorted(glob.glob(os.path.join(base, "cells", "*.json")))
    if not cells:
        print("no cells under %s" % base)
        return 1

    for record_path in cells:
        record = json.load(open(record_path))
        summary = record["summary"]
        rows_path = record_path[: -len(".json")] + ".jsonl"
        if not os.path.exists(rows_path):
            print("%s: no rows beside the record" % os.path.basename(record_path))
            continue
        rows = [json.loads(line) for line in open(rows_path)]

        cell_ns = record.get("cell_duration_ns") or 0
        warm_ns = record.get("warmup_ns") or 0
        print("\n%s  cell %.0fs, warm-up %.0fs, visit period %.0fs" % (
            os.path.basename(record_path), cell_ns / 1e9, warm_ns / 1e9, period))
        print("  recorded: drift %+.3f over %d requests -- %s" % (
            summary.get("warmup_drift", 0), summary.get("requests", 0),
            "CLEAN" if not summary.get("flagged") else "FLAGGED: "
            + "; ".join(summary.get("flag_reasons") or [])))

        # The cell's own clock, so periods are counted from where the cell began
        # rather than from the first row that happened to succeed.
        served = [r for r in rows if r.get("ttft_ns") and r.get("outcome") == "success"]
        if not served:
            print("  no successful rows carrying a TTFT")
            continue
        start = min(r["started_at_ns"] for r in served)

        # Every period, warm-up included, so the periods the warm-up was sized to
        # cover can be seen to have been the slow ones.
        periods: dict[int, list[float]] = {}
        for r in served:
            elapsed = (r["started_at_ns"] - start) / 1e9
            periods.setdefault(int(elapsed // period), []).append(r["ttft_ns"] / 1e6)
        shown = []
        for k in sorted(periods):
            v = periods[k]
            warm = "warm" if (k + 1) * period <= warm_ns / 1e9 else "measured"
            shown.append("p%d %.0fms (n=%d, %s)" % (k, statistics.median(v), len(v), warm))
        print("  per period: " + "  ".join(shown))

        # The split the check makes: the midpoint of the measured window.
        measured = [r for r in served if not r.get("warmup")]
        if len(measured) < 10:
            print("  too few measured rows to split")
            continue
        lo = min(r["started_at_ns"] for r in measured)
        hi = max(r["started_at_ns"] for r in measured)
        mid = lo + (hi - lo) / 2
        early = sorted(r["ttft_ns"] for r in measured if r["started_at_ns"] < mid)
        late = sorted(r["ttft_ns"] for r in measured if r["started_at_ns"] >= mid)
        if not early or not late:
            print("  the measured window did not split")
            continue
        ep, lp = statistics.median(early) / 1e6, statistics.median(late) / 1e6
        print("  halves: first %.0fms (n=%d) vs second %.0fms (n=%d) -> %+.3f" % (
            ep, len(early), lp, len(late), (ep - lp) / lp))
        print("  measured window spans %.2f visit periods" % ((hi - lo) / 1e9 / period))
    return 0


if __name__ == "__main__":
    sys.exit(main())
