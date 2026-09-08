#!/usr/bin/env python3
"""Recompute every figure in this directory's README from the rows beside it.

    python3 analyse.py

The characterization reports are rebuilt by `characterize -render`, but the
thermal finding is not something the tool computes: it comes from joining the
per-request rows to nvidia-smi telemetry sampled alongside them. This script is
that join, kept here so the README's tables are reproducible rather than
asserted.
"""

import collections
import csv
import datetime as dt
import json
import os
import statistics as st

HERE = os.path.dirname(os.path.abspath(__file__))

# Bit meanings for nvidia-smi's clocks_throttle_reasons.active mask.
THROTTLE_BITS = [
    (0x001, "GpuIdle"),
    (0x002, "AppClocks"),
    (0x004, "SwPowerCap"),
    (0x008, "HwSlowdown"),
    (0x010, "SyncBoost"),
    (0x020, "SwThermal"),
    (0x040, "HwThermal"),
    (0x080, "HwPowerBrake"),
    (0x100, "DisplayClk"),
]


def probes(arm):
    """Measured, successful rows for one arm, excluding warm-up."""
    path = os.path.join(HERE, arm, "probes.jsonl")
    with open(path) as f:
        rows = [json.loads(line) for line in f]
    return [r for r in rows if not r["warmup"] and r["outcome"] == "success"]


def per_replica_ttft(rows):
    by = collections.defaultdict(list)
    for r in rows:
        by[r["replica"]].append(r["ttft_ns"] / 1e6)
    return {k: st.median(v) for k, v in sorted(by.items())}


def spread(medians):
    lo, hi = min(medians.values()), max(medians.values())
    return 100 * (hi - lo) / lo


def telemetry():
    """(timestamp, gpu, temp C, sm clock MHz, power W, util %, throttle mask)."""
    out = []
    with open(os.path.join(HERE, "gpu-telemetry.csv")) as f:
        for row in csv.reader(f):
            if len(row) < 8:
                continue
            try:
                out.append((
                    dt.datetime.strptime(row[1].strip(), "%Y/%m/%d %H:%M:%S.%f"),
                    int(row[0]),
                    int(row[2]),
                    int(row[3].split()[0]),
                    float(row[4].split()[0]),
                    int(row[6].split()[0]),
                    int(row[7].strip(), 16),
                ))
            except (ValueError, IndexError):
                continue
    return out


def main():
    print("== Per-replica TTFT p50, by arm\n")
    for arm in ("solo", "together"):
        rows = probes(arm)
        meds = per_replica_ttft(rows)
        print(f"  {arm}: spread {spread(meds):.1f}%")
        for k, v in meds.items():
            print(f"    {k}  {v:7.1f} ms")
        print()

    # The together arm ran three repetitions; the deficit grows across them,
    # which is the thermal signature and the reason a single number understates
    # it.
    print("== Together arm, per repetition (the degradation)\n")
    rows = probes("together")
    by_rep = collections.defaultdict(list)
    for r in rows:
        by_rep[r["repetition"]].append(r)
    for rep in sorted(by_rep):
        meds = per_replica_ttft(by_rep[rep])
        r3 = meds["replica-3"]
        others = {k: v for k, v in meds.items() if k != "replica-3"}
        lo = min(others.values())
        print(f"  r{rep}: replica-3 {r3:6.1f} ms, fleet min {lo:6.1f} ms, "
              f"gap {100 * (r3 - lo) / lo:5.1f}%")
    print()

    # Telemetry is split into the first and last third of the busy window: the
    # first third is largely the solo arm, the last third the together arm.
    tel = telemetry()
    busy = [t for t in tel if t[5] > 0]
    t0, t1 = busy[0][0], busy[-1][0]
    span = (t1 - t0).total_seconds()
    print(f"== GPU telemetry ({len(busy)} busy samples over {t1 - t0})\n")

    for label, lo, hi in (("first third (mostly the solo arm)", 0.0, 0.34),
                          ("last third (the together arm)", 0.66, 1.01)):
        agg = collections.defaultdict(lambda: collections.defaultdict(list))
        for ts, gpu, temp, clk, pw, _util, mask in busy:
            frac = (ts - t0).total_seconds() / span
            if lo <= frac < hi:
                agg[gpu]["temp"].append(temp)
                agg[gpu]["clk"].append(clk)
                agg[gpu]["pw"].append(pw)
                agg[gpu]["mask"].append(mask)
        print(f"  {label}")
        for gpu in sorted(agg):
            v = agg[gpu]
            n = len(v["mask"])
            reasons = []
            for bit, name in THROTTLE_BITS:
                if name == "GpuIdle":
                    continue
                hits = sum(1 for m in v["mask"] if m & bit)
                if hits:
                    reasons.append(f"{name} {100 * hits / n:.0f}%")
            print(f"    GPU{gpu}  temp max {max(v['temp'])}C  "
                  f"clk mean {st.mean(v['clk']):6.0f} min {min(v['clk'])}  "
                  f"power mean {st.mean(v['pw']):5.1f}W  "
                  f"{', '.join(reasons) if reasons else '(no throttling)'}")
        print()


if __name__ == "__main__":
    main()
