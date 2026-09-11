#!/usr/bin/env python3
"""Measure how fast KV-sized buffers move between this host's cards (#22).

idea.md §8 asks whether disaggregating prefill and decode could pay on this box,
and says to settle it with arithmetic before building anything. The arithmetic
needs one measured quantity, and this is it: how many bytes per second actually
cross from one card to another, per topology class, with the links trained up by
real traffic. Read off an idle card, power management reports every link at
gen 1, which is not the link a transfer would use.

Paths, each timed on the host clock around a copy and a wait for its streams:

  h2d, d2h  one card and pinned host memory: the card's own link, the ceiling
            every card-to-card path below is built out of.
  peer      a card-to-card copy as CUDA performs it when asked — torch issues
            cudaMemcpyPeerAsync when peer access is off. The driver reports
            peer access unsupported on every pair of this host ("CNS", chipset
            not supported), so this is the driver's own staging through host
            memory, not a direct transfer. It is what a framework gets by
            calling copy.
  bounce    a card-to-card copy staged through pinned host memory by hand,
            chunked and pipelined over one stream per card, so the sender's
            read of one chunk overlaps the receiver's write of the one before.
            The best a transfer path written for this host could do without
            peer access.

Conditions:

  alone     one transfer at a time, every other card idle.
  busy      the same, while both cards run a device-local copy loop, which
            keeps their memory bus loaded the way decode does. The loop runs on
            its own stream, which the clock never waits for.
  together  every disjoint pair of one topology class transferring at once,
            each repetition started on a common barrier: where transfers
            contend for a shared switch, root port, host memory or the socket
            link.

The host buffer is placed on the NUMA node this process is bound to, so run it
under `numactl --cpunodebind=N --membind=N`, once per node. Its pages are
touched before they are pinned, so first touch places them, and where they
landed is read back from /proc/self/numa_maps and recorded rather than assumed.

It moves bytes and nothing else: no KV cache is built, read or shipped. Rows go
to <dir>/rows.jsonl, one per timed transfer, and the run's conditions to
<dir>/meta.json.
"""

import argparse
import bisect
import contextlib
import json
import mmap
import os
import socket
import sys
import threading
import time
from datetime import datetime, timezone

# Before torch touches CUDA: number the cards by PCI bus, as nvidia-smi and NVML
# do, rather than CUDA's default fastest-first. Otherwise a row could name a card
# by an index the topology matrix gives to another.
os.environ["CUDA_DEVICE_ORDER"] = "PCI_BUS_ID"

import pynvml  # noqa: E402
import torch  # noqa: E402

MIB = 1 << 20

# NVML's levels for how two cards reach each other, spelled as nvidia-smi topo -m
# spells them, so every row can be checked against the recorded matrix.
LINKS = {0: "X", 10: "PIX", 20: "PXB", 30: "PHB", 40: "NODE", 50: "SYS"}


def utcnow():
    return datetime.now(timezone.utc).isoformat()


def text(value):
    return value.decode() if isinstance(value, bytes) else value


def parse_args():
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--dir", required=True, help="where rows.jsonl and meta.json are written")
    p.add_argument("--host-node", type=int, required=True,
                   help="the NUMA node numactl binds this run to; checked against where the pages actually landed")
    p.add_argument("--sizes-mib", default="32,64,128,256,512,992",
                   help="transfer sizes measured alone, on every path and pair")
    p.add_argument("--load-mib", type=int, default=256, help="the one size measured busy and together")
    p.add_argument("--repetitions", type=int, default=10)
    p.add_argument("--warmup", type=int, default=3,
                   help="untimed transfers before each group, so the link has trained up before the clock starts")
    p.add_argument("--chunk-mib", type=int, default=8, help="bounce chunk size")
    p.add_argument("--slots", type=int, default=4, help="bounce chunks in flight per pair")
    p.add_argument("--churn-mib", type=int, default=512, help="size of the busy condition's device-local copy")
    p.add_argument("--dirty-mib", type=int, default=256,
                   help="refuse to start if any card already holds this much; GPU_DIRTY_THRESHOLD_MIB")
    return p.parse_args()


def memory(h):
    """A card's used and driver-reserved memory, in MiB.

    The v2 call, as nvidia-smi reads it. The v1 call counts the driver's own
    reservation as used — about 450 MiB on every idle 3090 of this box, against
    the 1 MiB nvidia-smi shows — and would refuse an idle box as a busy one.
    """
    try:
        m = pynvml.nvmlDeviceGetMemoryInfo(h, version=pynvml.nvmlMemory_v2)
        return m.used // MIB, m.reserved // MIB
    except (AttributeError, TypeError, pynvml.NVMLError):
        return pynvml.nvmlDeviceGetMemoryInfo(h).used // MIB, None


def card_states(handles):
    """What every card holds, and who holds it."""
    states = []
    for i, h in enumerate(handles):
        used, reserved = memory(h)
        states.append({
            "index": i,
            "used_mib": used,
            "reserved_mib": reserved,
            "pids": [p.pid for p in pynvml.nvmlDeviceGetComputeRunningProcesses(h)],
        })
    return states


def check_numbering(handles):
    """Refuse to run if torch and NVML number the cards differently."""
    for i, h in enumerate(handles):
        nvml_bus = int(text(pynvml.nvmlDeviceGetPciInfo(h).busId).split(":")[1], 16)
        torch_bus = getattr(torch.cuda.get_device_properties(i), "pci_bus_id", None)
        if torch_bus is not None and torch_bus != nvml_bus:
            sys.exit(f"bandwidth: torch's card {i} is on bus {torch_bus:#x} and NVML's on {nvml_bus:#x}; "
                     "the rows would name the wrong cards")


def link_between(handles, a, b):
    return LINKS.get(pynvml.nvmlDeviceGetTopologyCommonAncestor(handles[a], handles[b]), "?")


class Sampler(threading.Thread):
    """Samples every card's PCIe link while the run goes, and watches for anyone else on the cards.

    The link state is the evidence that a figure was measured on a trained link
    and not an idle one. The processes are the evidence that it was measured on
    a box nobody else was using: the box is shared, and a copy timed beside
    someone else's job measures their job too.
    """

    def __init__(self, handles, interval=0.005, procs_every=1.0):
        super().__init__(daemon=True)
        self.handles = handles
        self.interval = interval
        self.procs_every = procs_every
        self.times = []
        self.links = []
        self.foreign = {}
        self.errors = 0
        self.halt = threading.Event()

    def run(self):
        own = os.getpid()
        next_procs = 0.0
        while not self.halt.is_set():
            now = time.perf_counter()
            for i, h in enumerate(self.handles):
                try:
                    link = (i, pynvml.nvmlDeviceGetCurrPcieLinkGeneration(h), pynvml.nvmlDeviceGetCurrPcieLinkWidth(h))
                except pynvml.NVMLError:
                    self.errors += 1
                    continue
                self.times.append(now)
                self.links.append(link)
            if now >= next_procs:
                next_procs = now + self.procs_every
                for i, h in enumerate(self.handles):
                    try:
                        procs = pynvml.nvmlDeviceGetComputeRunningProcesses(h)
                    except pynvml.NVMLError:
                        self.errors += 1
                        continue
                    for p in procs:
                        if p.pid != own and p.pid not in self.foreign:
                            self.foreign[p.pid] = {"card": i, "used_mib": (p.usedGpuMemory or 0) // MIB,
                                                   "seen_at": utcnow()}
            self.halt.wait(self.interval)

    def window(self, t0, t1, cards):
        """The lowest link generation and width the given cards showed between t0 and t1."""
        lo, hi = bisect.bisect_left(self.times, t0), bisect.bisect_right(self.times, t1)
        seen = [(gen, width) for (card, gen, width) in self.links[lo:hi] if card in cards]
        if not seen:
            return {"link_samples": 0}
        return {"link_samples": len(seen),
                "link_gen_min": min(g for g, _ in seen),
                "link_width_min": min(w for _, w in seen)}


class HostBuffer:
    """Pinned host memory whose NUMA placement is read back rather than assumed."""

    def __init__(self, nbytes):
        # mmap for a page-aligned region of its own, so numa_maps names it by
        # its start address and nothing else shares its pages.
        self.map = mmap.mmap(-1, nbytes, flags=mmap.MAP_PRIVATE | mmap.MAP_ANONYMOUS)
        self.tensor = torch.frombuffer(self.map, dtype=torch.uint8)
        self.tensor.fill_(1)  # first touch, under this process's binding
        torch.cuda.check_error(torch.cuda.cudart().cudaHostRegister(self.tensor.data_ptr(), nbytes, 0))
        if not self.tensor.is_pinned():
            sys.exit("bandwidth: the host buffer did not register as pinned; every host copy would be synchronous")

    def pages_by_node(self):
        start = f"{self.tensor.data_ptr():x}"
        with open("/proc/self/numa_maps") as f:
            for line in f:
                fields = line.split()
                if fields and fields[0].lstrip("0") == start.lstrip("0"):
                    return {k[1:]: int(v) for k, v in (fl.split("=", 1) for fl in fields if fl[:1] == "N" and "=" in fl)}
        return {}

    def slice(self, offset, n):
        return self.tensor[offset:offset + n]


def on(*streams):
    """Make these the current streams on their cards, so a copy torch issues runs on them."""
    stack = contextlib.ExitStack()
    for s in streams:
        stack.enter_context(torch.cuda.stream(s))
    return stack


def timed(run_once, streams):
    """Seconds one transfer took: from its streams being idle to their being idle again.

    Streams, not devices: a device-wide wait would also wait for the busy
    condition's copy loop, and time that instead.
    """
    for s in streams:
        s.synchronize()
    t0 = time.perf_counter()
    run_once()
    for s in streams:
        s.synchronize()
    return time.perf_counter() - t0


class Churn(threading.Thread):
    """Keeps one card's memory bus busy, on a stream the measurement never waits for."""

    def __init__(self, a, b):
        super().__init__(daemon=True)
        self.a, self.b = a, b
        self.stream = torch.cuda.Stream(device=a.device)
        self.halt = threading.Event()
        self.copied = 0
        self.seconds = 0.0

    def run(self):
        t0 = time.perf_counter()
        with torch.cuda.stream(self.stream):
            while not self.halt.is_set():
                for _ in range(4):
                    self.b.copy_(self.a, non_blocking=True)
                self.stream.synchronize()
                self.copied += 4 * self.a.numel()
        self.seconds = time.perf_counter() - t0

    def gbps(self):
        return self.copied / self.seconds / 1e9 if self.seconds else 0.0


class Bench:
    def __init__(self, args, handles, sampler, rows, host):
        self.args = args
        self.handles = handles
        self.sampler = sampler
        self.rows = rows
        self.host = host
        self.cards = list(range(len(handles)))
        self.chunk = args.chunk_mib * MIB
        self.ring_bytes = args.slots * self.chunk
        biggest = max(max(int(s) for s in args.sizes_mib.split(",")), args.load_mib) * MIB
        self.dev = {c: torch.empty(biggest, dtype=torch.uint8, device=c) for c in self.cards}
        churn = args.churn_mib * MIB
        self.churn_bufs = {c: (torch.empty(churn, dtype=torch.uint8, device=c),
                               torch.empty(churn, dtype=torch.uint8, device=c)) for c in self.cards}
        self.lock = threading.Lock()

    def write(self, row):
        with self.lock:
            self.rows.write(json.dumps(row) + "\n")
            self.rows.flush()

    def group(self, fields, cards, streams, run_once, before_each=None, extra=None):
        """Times one kind of transfer: a warm-up first, then each repetition on its own clock."""
        for _ in range(self.args.warmup):
            if before_each:
                before_each()
            timed(run_once, streams)
        seconds = []
        t0 = time.perf_counter()
        for _ in range(self.args.repetitions):
            if before_each:
                before_each()
            seconds.append(timed(run_once, streams))
        link = self.sampler.window(t0, time.perf_counter(), cards)
        for rep, s in enumerate(seconds):
            self.write({**fields, "rep": rep, "seconds": s, **link, **(extra() if extra else {})})

    def host_copy(self, path, card, n):
        s = torch.cuda.Stream(device=card)
        dev, host = self.dev[card][:n], self.host.slice(0, n)

        def run_once():
            with on(s):
                if path == "h2d":
                    dev.copy_(host, non_blocking=True)
                else:
                    host.copy_(dev, non_blocking=True)

        src, dst = (-1, card) if path == "h2d" else (card, -1)
        self.group({"path": path, "condition": "alone", "src": src, "dst": dst, "link": "",
                    "host_node": self.args.host_node, "bytes": n}, [card], [s], run_once)

    def pair_copy(self, path, src, dst, n, ring_offset=0):
        """A card-to-card copy of n bytes as a closure, with the streams it runs on."""
        s_src, s_dst = torch.cuda.Stream(device=src), torch.cuda.Stream(device=dst)
        a, b = self.dev[src][:n], self.dev[dst][:n]
        if path == "peer":
            def run_once():
                with on(s_dst, s_src):
                    b.copy_(a, non_blocking=True)
            return run_once, [s_src, s_dst]

        ring = [self.host.slice(ring_offset + k * self.chunk, self.chunk) for k in range(self.args.slots)]

        def run_once():
            freed = [None] * len(ring)
            offset, i = 0, 0
            while offset < n:
                c = min(self.chunk, n - offset)
                slot = i % len(ring)
                if freed[slot] is not None:
                    s_src.wait_event(freed[slot])
                with on(s_src):
                    ring[slot][:c].copy_(a[offset:offset + c], non_blocking=True)
                    filled = torch.cuda.Event()
                    filled.record(s_src)
                s_dst.wait_event(filled)
                with on(s_dst):
                    b[offset:offset + c].copy_(ring[slot][:c], non_blocking=True)
                    done = torch.cuda.Event()
                    done.record(s_dst)
                freed[slot] = done
                offset += c
                i += 1
        return run_once, [s_src, s_dst]

    def fields(self, path, condition, src, dst, n):
        return {"path": path, "condition": condition, "src": src, "dst": dst,
                "link": link_between(self.handles, src, dst), "host_node": self.args.host_node, "bytes": n}

    def alone(self, path, src, dst, n):
        run_once, streams = self.pair_copy(path, src, dst, n)
        self.group(self.fields(path, "alone", src, dst, n), [src, dst], streams, run_once)

    def busy(self, path, src, dst, n):
        run_once, streams = self.pair_copy(path, src, dst, n)
        churns = {c: Churn(*self.churn_bufs[c]) for c in (src, dst)}
        for ch in churns.values():
            ch.start()
        time.sleep(0.5)  # let the loops reach their steady rate before the clock starts
        try:
            self.group(self.fields(path, "busy", src, dst, n), [src, dst], streams, run_once)
        finally:
            for ch in churns.values():
                ch.halt.set()
            for ch in churns.values():
                ch.join()
        # Recorded after the fact, on a row of its own kind: the loops' achieved
        # rate is the evidence the cards really were busy.
        self.write({"path": "churn", "condition": "busy", "src": src, "dst": dst, "bytes": n,
                    "host_node": self.args.host_node,
                    "churn_gbps": {str(c): ch.gbps() for c, ch in churns.items()}})

    def together(self, path, pairs, n):
        barrier = threading.Barrier(len(pairs))
        errors = []

        def one(k, src, dst):
            try:
                run_once, streams = self.pair_copy(path, src, dst, n, ring_offset=k * self.ring_bytes)
                others = [[s, d] for (s, d) in pairs if (s, d) != (src, dst)]
                self.group({**self.fields(path, "together", src, dst, n), "together_with": others},
                           [src, dst], streams, run_once, before_each=barrier.wait)
            except Exception as e:  # surfaced below; a silent thread would drop a pair's rows
                errors.append(e)
                barrier.abort()

        threads = [threading.Thread(target=one, args=(k, s, d)) for k, (s, d) in enumerate(pairs)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        if errors:
            raise errors[0]


def disjoint_pairs(handles, link):
    """As many pairs joined by this link as can run at once with no card in two of them."""
    used, pairs = set(), []
    n = len(handles)
    for a in range(n):
        for b in range(a + 1, n):
            if a in used or b in used or link_between(handles, a, b) != link:
                continue
            pairs.append((a, b))
            used.update((a, b))
    return pairs


def main():
    args = parse_args()
    pynvml.nvmlInit()
    handles = [pynvml.nvmlDeviceGetHandleByIndex(i) for i in range(pynvml.nvmlDeviceGetCount())]

    before = card_states(handles)
    dirty = [c for c in before if c["used_mib"] >= args.dirty_mib or c["pids"]]
    if dirty:
        sys.exit(f"bandwidth: refusing to measure while cards are in use: {dirty}. The box is shared, and a copy "
                 "timed beside someone else's job measures their job too.")
    if torch.cuda.device_count() != len(handles):
        sys.exit(f"bandwidth: torch sees {torch.cuda.device_count()} cards and NVML {len(handles)}")
    check_numbering(handles)

    os.makedirs(args.dir, exist_ok=True)
    sizes = [int(s) * MIB for s in args.sizes_mib.split(",")]
    cards = list(range(len(handles)))
    peer_access = {f"{a}-{b}": torch.cuda.can_device_access_peer(a, b) for a in cards for b in cards if a != b}

    sampler = Sampler(handles)
    sampler.start()
    started = utcnow()
    host_bytes = max(max(sizes), args.load_mib * MIB, 3 * args.slots * args.chunk_mib * MIB)
    host = HostBuffer(host_bytes)
    pages = host.pages_by_node()

    with open(os.path.join(args.dir, "rows.jsonl"), "w") as rows:
        bench = Bench(args, handles, sampler, rows, host)
        for n in sizes:
            for c in cards:
                bench.host_copy("h2d", c, n)
                bench.host_copy("d2h", c, n)
        pairs = [(a, b) for a in cards for b in cards if a != b]
        for n in sizes:
            for src, dst in pairs:
                bench.alone("peer", src, dst, n)
                bench.alone("bounce", src, dst, n)
        load = args.load_mib * MIB
        for src, dst in pairs:
            bench.busy("peer", src, dst, load)
            bench.busy("bounce", src, dst, load)
        for link in ("PIX", "PXB", "PHB", "NODE", "SYS"):
            forward = disjoint_pairs(handles, link)
            if len(forward) < 2:
                continue  # one pair alone is the alone condition
            for direction in (forward, [(b, a) for (a, b) in forward]):
                bench.together("peer", direction, load)
                bench.together("bounce", direction, load)

    sampler.halt.set()
    sampler.join()
    meta = {
        "started": started,
        "finished": utcnow(),
        "host": socket.gethostname(),
        "torch": torch.__version__,
        "cuda": torch.version.cuda,
        "driver": text(pynvml.nvmlSystemGetDriverVersion()),
        "host_node": args.host_node,
        # Where the cores were bound. The memory binding has no such field —
        # numactl --membind sets a policy and leaves Mems_allowed_list alone —
        # so its evidence is where the host buffer's pages actually landed.
        "cpus_allowed": status_field("Cpus_allowed_list"),
        "host_pages_by_node": pages,
        "peer_access": peer_access,
        "cards_before": before,
        "foreign": sampler.foreign,
        "sampler_errors": sampler.errors,
        "sizes_mib": args.sizes_mib,
        "load_mib": args.load_mib,
        "repetitions": args.repetitions,
        "warmup": args.warmup,
        "chunk_mib": args.chunk_mib,
        "slots": args.slots,
        "churn_mib": args.churn_mib,
    }
    with open(os.path.join(args.dir, "meta.json"), "w") as f:
        json.dump(meta, f, indent=2)
    if str(args.host_node) not in pages or len(pages) != 1:
        sys.exit(f"bandwidth: the host buffer's pages landed on {pages}, not only on node {args.host_node}; "
                 "every row in this run claims the wrong placement. Was it run under numactl --membind?")
    if sampler.foreign:
        sys.exit(f"bandwidth: someone else used the cards during the run: {sampler.foreign}. The rows are kept "
                 "but are not clean.")


def status_field(name):
    with open("/proc/self/status") as f:
        for line in f:
            if line.startswith(name + ":"):
                return line.split(":", 1)[1].strip()
    return ""


if __name__ == "__main__":
    main()
