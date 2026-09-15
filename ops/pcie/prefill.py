#!/usr/bin/env python3
"""Measure how long one replica takes to prefill a prompt of N tokens (#22).

The other half of idea.md §8's arithmetic. A disaggregated request is
prefilled on one card and then ships the KV it built to another, so whether the
shipping matters depends on how it compares with the prefill before it. This
measures that prefill on the pinned engine, one request at a time, across a
range of prompt lengths.

Every prompt is token ids drawn fresh from a seeded generator and sent to
/v1/completions as ids, so the engine prefills exactly N tokens with no
tokenizer in between. No prompt is sent twice, so the prefix cache holds none
of it (ADR-0004), and each row carries the engine's own cached-token count to
show it.

The engine's own account is read around each request: the change in
vllm:request_prefill_time_seconds, which runs from the scheduler first taking
the request to its first token and so is prefill and nothing else, and in
vllm:time_to_first_token_seconds, beside the client's own clock. Sending one
request at a time is what makes a histogram sum's change one request's figure.

Lengths are sent in reverse order on alternate repetitions, so a host that
drifts during the run cannot look like a length that is slow.
"""

import argparse
import json
import random
import sys
import time
import urllib.request
from datetime import datetime, timezone

SERIES = {
    "prefill_sum": "vllm:request_prefill_time_seconds_sum",
    "prefill_count": "vllm:request_prefill_time_seconds_count",
    "ttft_sum": "vllm:time_to_first_token_seconds_sum",
    "ttft_count": "vllm:time_to_first_token_seconds_count",
}

# Llama 3's special tokens start at 128000. Ids are drawn below them, and above
# the first thousand, so a prompt is ordinary text to the engine.
TOKEN_RANGE = (1000, 128000)


def utcnow():
    return datetime.now(timezone.utc).isoformat()


def parse_args():
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--replica", required=True, help="the replica's base URL, e.g. http://127.0.0.1:8001")
    p.add_argument("--model", required=True)
    p.add_argument("--lengths", default="256,512,1024,2048,4096,7936",
                   help="prompt lengths in tokens; the last fits under MAX_MODEL_LEN with room for one output token")
    p.add_argument("--repetitions", type=int, default=10)
    p.add_argument("--warmup", type=int, default=3, help="untimed requests first, so no row pays a first forward pass")
    p.add_argument("--seed", type=int, default=22)
    p.add_argument("--out", required=True, help="rows, one per request, as JSONL")
    return p.parse_args()


def scrape(base):
    body = urllib.request.urlopen(base + "/metrics", timeout=10).read().decode()
    found = {}
    for line in body.splitlines():
        if not line or line.startswith("#"):
            continue
        name = line.split("{", 1)[0].split(" ", 1)[0]
        for key, series in SERIES.items():
            if name == series:
                found[key] = found.get(key, 0.0) + float(line.rsplit(" ", 1)[1])
    missing = sorted(SERIES[k] for k in set(SERIES) - set(found))
    if missing:
        sys.exit(f"prefill: the replica publishes no {missing}; the engine's own prefill time cannot be read")
    return found


def settled(base, before, timeout=5.0):
    """The metrics once the engine has counted the request just answered.

    The engine records a request's timings as it finishes, which can be after
    the response has reached the client, so reading at once can miss it.
    """
    deadline = time.monotonic() + timeout
    while True:
        after = scrape(base)
        if after["prefill_count"] > before["prefill_count"] or time.monotonic() > deadline:
            return after
        time.sleep(0.02)


def complete(base, model, ids):
    body = json.dumps({"model": model, "prompt": ids, "max_tokens": 1, "temperature": 0}).encode()
    req = urllib.request.Request(base + "/v1/completions", data=body, headers={"Content-Type": "application/json"})
    t0 = time.perf_counter()
    with urllib.request.urlopen(req, timeout=300) as r:
        resp = json.load(r)
    return time.perf_counter() - t0, resp


def measure(base, model, ids):
    before = scrape(base)
    seconds, resp = complete(base, model, ids)
    after = settled(base, before)
    usage = resp.get("usage") or {}
    details = usage.get("prompt_tokens_details") or {}
    return {
        "client_seconds": seconds,
        "engine_prefill_seconds": after["prefill_sum"] - before["prefill_sum"],
        "engine_ttft_seconds": after["ttft_sum"] - before["ttft_sum"],
        "requests_counted": round(after["prefill_count"] - before["prefill_count"]),
        "prompt_tokens": usage.get("prompt_tokens", -1),
        # -1 when the engine does not report it, which is not the same as none
        # cached: ENABLE_PROMPT_TOKENS_DETAILS is what turns it on.
        "cached_tokens": details.get("cached_tokens", -1) if details else -1,
    }


def main():
    args = parse_args()
    lengths = [int(n) for n in args.lengths.split(",")]
    rng = random.Random(args.seed)

    def prompt(n):
        return [rng.randrange(*TOKEN_RANGE) for _ in range(n)]

    started = utcnow()
    for _ in range(args.warmup):
        measure(args.replica, args.model, prompt(1024))

    with open(args.out, "w") as out:
        for rep in range(args.repetitions):
            order = lengths if rep % 2 == 0 else list(reversed(lengths))
            for n in order:
                row = {"tokens": n, "rep": rep, **measure(args.replica, args.model, prompt(n))}
                out.write(json.dumps(row) + "\n")
                out.flush()
                print(f"{n:5d} tokens  prefill {row['engine_prefill_seconds'] * 1e3:7.1f} ms  "
                      f"client {row['client_seconds'] * 1e3:7.1f} ms  cached {row['cached_tokens']}", flush=True)

    meta = {"started": started, "finished": utcnow(), "replica": args.replica, "model": args.model,
            "lengths": lengths, "repetitions": args.repetitions, "warmup": args.warmup, "seed": args.seed}
    with open(args.out.rsplit(".", 1)[0] + "-meta.json", "w") as f:
        json.dump(meta, f, indent=2)


if __name__ == "__main__":
    main()
