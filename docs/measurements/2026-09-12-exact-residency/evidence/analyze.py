"""Why exact residency lost: cost of knowing vs quality of knowledge.

Reads the bench cell rows (prediction + decision + TTFT, client side) and the
router's own records (tokenize_ns + router_overhead_ns, router side), for the
three policies #24's grid ran. Warm-up rows are dropped, as the harness drops
them, so this measures the same window the goodput figures came from.
"""
import glob
import json
import statistics as st
from collections import Counter

RUN = "runs/pressure-kv-events"
POLICIES = ["session_affinity", "prefix_affinity", "exact_residency"]


def q(xs, p):
    if not xs:
        return float("nan")
    xs = sorted(xs)
    return xs[min(int(len(xs) * p), len(xs) - 1)]


print("=== client side: bench cell rows (warm-up dropped) ===")
for pol in POLICIES:
    dec = Counter()
    ttft, pred, cached, over_pred, n, ok = [], [], [], 0, 0, 0
    for f in glob.glob(f"{RUN}/*/cells/{pol}-*.jsonl"):
        for line in open(f):
            try:
                r = json.loads(line)
            except Exception:
                continue
            if r.get("warmup"):
                continue
            n += 1
            dec[r.get("decision", "?")] += 1
            if r.get("outcome") == "success":
                ok += 1
            if r.get("ttft_ns"):
                ttft.append(r["ttft_ns"])
            # Prediction vs what the engine actually reused. Exact residency
            # predicts in tokens; prefix affinity predicts in bytes, so only the
            # token-predicting policy is compared directly here.
            if r.get("engine_usage_read") and r.get("engine_cache_read"):
                c = r.get("engine_cached_tokens", 0)
                p = r.get("prefix_match_tokens", 0)
                if p:
                    pred.append(p)
                    cached.append(c)
                    if p > c:
                        over_pred += 1
    print(f"\n-- {pol}: {n} measured rows, {ok} success")
    tot = sum(dec.values()) or 1
    for k, v in dec.most_common():
        print(f"   {k:24s} {v:8d}  {100*v/tot:5.1f}%")
    if ttft:
        print(f"   ttft   p50 {st.median(ttft)/1e6:7.1f}ms  p90 {q(ttft,.9)/1e6:7.1f}ms")
    if pred:
        print(f"   token prediction: n={len(pred)}  predicted p50 {st.median(pred):.0f}  "
              f"engine reused p50 {st.median(cached):.0f}  over-predicted {100*over_pred/len(pred):.2f}%")

print("\n=== router side: tokenize and overhead (sampled 1 in 20) ===")
for pol in POLICIES:
    f = f"{RUN}/router-{pol}.jsonl"
    tok, over = [], []
    try:
        for i, line in enumerate(open(f)):
            if i % 20:
                continue
            try:
                r = json.loads(line)
            except Exception:
                continue
            if r.get("tokenize_ns"):
                tok.append(r["tokenize_ns"])
            if r.get("router_overhead_ns"):
                over.append(r["router_overhead_ns"])
    except FileNotFoundError:
        print(f"-- {pol}: no router records")
        continue
    print(f"-- {pol}: n={len(over)}")
    if tok:
        print(f"   tokenize_ns       p50 {st.median(tok)/1e6:7.2f}ms  p90 {q(tok,.9)/1e6:7.2f}ms  p99 {q(tok,.99)/1e6:7.2f}ms")
    else:
        print("   tokenize_ns       none (policy does not tokenize)")
    if over:
        print(f"   router_overhead   p50 {st.median(over)/1e6:7.2f}ms  p90 {q(over,.9)/1e6:7.2f}ms  p99 {q(over,.99)/1e6:7.2f}ms")
