#!/usr/bin/env python3
"""Each card's chain of PCIe bridges up to its CPU root port, read from sysfs (#22).

nvidia-smi topo -m names how two cards reach each other but not what lies
between them, and that is what explains a contention figure: two cards behind
one switch share its single uplink to the CPU, however wide each card's own
link is. sysfs answers without root, where lspci -vv does not.

The links are printed as they stand when read. At idle, power management holds
the cards' own links at 2.5 GT/s (gen 1), which is why no bandwidth is taken
from this: the widths and the maxima are the fixed facts, and the current speeds
show only what an idle link looks like.
"""

import os
import re
import subprocess

BDF = re.compile(r"[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]")


def attr(device, name):
    try:
        with open(os.path.join("/sys/bus/pci/devices", device, name)) as f:
            return f.read().strip()
    except OSError:
        return "?"


def link(device):
    return (f"{device} [{attr(device, 'current_link_speed')} x{attr(device, 'current_link_width')}"
            f", max {attr(device, 'max_link_speed')} x{attr(device, 'max_link_width')}]")


def main():
    out = subprocess.run(["nvidia-smi", "--query-gpu=index,pci.bus_id", "--format=csv,noheader"],
                         capture_output=True, text=True, check=True).stdout
    for line in filter(None, out.splitlines()):
        index, bus = (x.strip() for x in line.split(","))
        domain, rest = bus.split(":", 1)
        device = f"{domain[-4:]}:{rest}".lower()
        chain = [c for c in os.path.realpath(f"/sys/bus/pci/devices/{device}").split("/") if BDF.fullmatch(c)]
        print(f"GPU{index}: " + "  ->  ".join(link(c) for c in chain))
    groups = os.listdir("/sys/kernel/iommu_groups") if os.path.isdir("/sys/kernel/iommu_groups") else []
    print(f"\nIOMMU groups: {len(groups)} ({'on' if groups else 'off'})")


if __name__ == "__main__":
    main()
