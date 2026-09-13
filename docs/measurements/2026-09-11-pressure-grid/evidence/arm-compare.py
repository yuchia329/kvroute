#!/usr/bin/env python3
"""Compare the grid's arms point by point, off the cell records committed here.

    ./arm-compare.py session=../grid:session_affinity \
                     load=../grid:prefix_affinity \
                     off=../spilloff:prefix_affinity \
                     r0.62=../residency/m0.62:prefix_affinity

Each argument is NAME=DIR:POLICY. DIR holds one subdirectory per grid point
(ws<ws>-skew<skew>/cells/*.json), which is what every arm of #18 is: the grid
itself, the spill-off arm, and one directory per residency mark.

Why this exists. The tables in the README that cmd/pressuremap does not draw are
cross-arm — the same policy under different spill rules, in different directories
— and a pressure map is one directory's comparison between policies. Without this
script those tables are assertions. It reads only what is committed beside it, so
it reproduces them from the repository rather than from the box.

What it does NOT read. Per-replica placement — the busiest replica's share, and
how many replicas served each repetition's hottest conversations — is counted off
the router rows, which stay on the box (43 MB gzipped). Those columns in the
README name the run that produced them.

A flagged cell is skipped, never pooled: §6 discards rather than averages. n is
what survived, and a point where the arms have different n says so.
"""

import glob
import json
import os
import statistics
import sys


def cells(root, policy):
    """Yield (point, cell) for every unflagged cell of one policy under root."""
    for path in sorted(glob.glob(os.path.join(root, "ws*-skew*", "cells", "*.json"))):
        try:
            cell = json.load(open(path))
        except Exception:
            continue  # a truncated record is not a measurement
        if cell.get("policy") != policy:
            continue
        if cell.get("summary", {}).get("flagged"):
            continue
        point = os.path.basename(os.path.dirname(os.path.dirname(path)))
        yield point, cell


def point_key(point):
    """ws3-skew1.4 -> (3.0, 1.4), so the axes sort numerically."""
    ws, _, skew = point.removeprefix("ws").partition("-skew")
    return (float(ws), float(skew))


def hit_rate(cell):
    queries = cell.get("prefix_cache_queries") or 0
    if not cell.get("prefix_cache_read") or queries == 0:
        return None
    return cell["prefix_cache_hits"] / queries


def decisions(cell):
    return cell.get("summary", {}).get("decisions", {})


def spilled(cell):
    """(residency spills, load spills) for a cell, old and new field names.

    #28 renamed the residency branch's counter from spill_kv to spill_hit_rate
    when it replaced the signal. The grid's own cells carry the old name and the
    residency arm's carry the new one, and both are zero for a branch that was
    off, so reading only one name would silently drop an arm's whole column.
    """
    d = decisions(cell)
    return d.get("spill_hit_rate", d.get("spill_kv", 0)), d.get("spill_load", 0)


def routed(cell):
    """Decisions the challenger's own policy made, the denominator for a rate."""
    d = decisions(cell)
    return sum(v for k, v in d.items() if k != "undecided") or 0


def load(spec):
    name, _, rest = spec.partition("=")
    root, _, policy = rest.partition(":")
    if not name or not root or not policy:
        sys.exit(f"arm-compare: {spec!r} is not NAME=DIR:POLICY")
    arm = {}
    for point, cell in cells(root, policy):
        arm.setdefault(point, []).append(cell)
    if not arm:
        sys.exit(f"arm-compare: no unflagged {policy} cells under {root}")
    return name, arm


def main(argv):
    if len(argv) < 2:
        sys.exit(__doc__)
    arms = [load(spec) for spec in argv[1:]]
    points = sorted({p for _, arm in arms for p in arm}, key=point_key)

    width = max(14, max(len(n) for n, _ in arms) + 1)
    print("GOODPUT, requests/s inside the SLO: median [min-max] n, flagged cells skipped\n")
    print("point".ljust(16) + "".join(n.ljust(width + 12) for n, _ in arms))
    for point in points:
        row = point.ljust(16)
        for _, arm in arms:
            got = sorted(c["summary"]["goodput_rps"] for c in arm.get(point, []))
            if not got:
                row += "--".ljust(width + 12)
                continue
            cell = f"{statistics.median(got):6.2f} [{got[0]:5.2f}-{got[-1]:5.2f}] {len(got)}"
            row += cell.ljust(width + 12)
        print(row)

    print("\nMECHANISM, means over the cells behind each median")
    print("hit% is the engines' own prefix cache hit rate; the spill columns are")
    print("each branch's share of the decisions the policy made.\n")
    header = "point".ljust(16)
    for name, _ in arms:
        header += f"{name} hit%".ljust(14) + f"{name} resid%".ljust(15) + f"{name} load%".ljust(14)
    print(header)
    for point in points:
        row = point.ljust(16)
        for _, arm in arms:
            got = arm.get(point, [])
            rates = [r for r in (hit_rate(c) for c in got) if r is not None]
            hit = f"{100 * statistics.mean(rates):.1f}" if rates else "--"
            resid = load_ = "--"
            if got:
                totals = [routed(c) for c in got]
                if sum(totals):
                    resid = f"{100 * sum(spilled(c)[0] for c in got) / sum(totals):.2f}"
                    load_ = f"{100 * sum(spilled(c)[1] for c in got) / sum(totals):.2f}"
            row += hit.ljust(14) + resid.ljust(15) + load_.ljust(14)
        print(row)

    # The separability marginals: each axis pooled over the other, which is what
    # isolates it. One branch per column, counted rather than rated, because the
    # question is which axis a branch answers to and a rate divides by a
    # denominator that moves with the policy's own throughput.
    print("\nSEPARABILITY, spill decisions counted, one axis pooled over the other\n")
    for axis, index in (("working set", 0), ("skew", 1)):
        print(f"{axis}, other pooled".ljust(24) + "".join(
            f"{n} resid".ljust(15) + f"{n} load".ljust(15) for n, _ in arms))
        for value in sorted({point_key(p)[index] for p in points}):
            row = f"{value:g}".ljust(24)
            for _, arm in arms:
                resid = sum(spilled(c)[0] for p, got in arm.items()
                            if point_key(p)[index] == value for c in got)
                load_ = sum(spilled(c)[1] for p, got in arm.items()
                            if point_key(p)[index] == value for c in got)
                row += f"{resid}".ljust(15) + f"{load_}".ljust(15)
            print(row)
        print()


if __name__ == "__main__":
    main(sys.argv)
