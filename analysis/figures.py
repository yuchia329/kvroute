# /// script
# requires-python = ">=3.11"
# dependencies = ["matplotlib==3.11.2"]
# ///
"""Draw the project's figures from the figure data the Go commands wrote.

    uv run analysis/figures.py <data dir> <out dir>

`make figures` is the one command: it has compare, pressuremap, recovery and
overhead write their figure data as JSON, then runs this.

Every number is in the JSON. Percentiles, medians, deltas and what the
repetitions can support each have one definition, in internal/bench, and the
published tables are rendered from them; this script places marks and does no
arithmetic of its own beyond scaling for display, so a figure cannot disagree
with the table printed beside it.

A data file's kind is the start of its name — pressuremap, comparison, recovery,
overhead — and each is drawn to an SVG of the same stem. A comparison draws two:
goodput against load, and the cache mechanism.
"""

import json
import math
import sys
from pathlib import Path

import matplotlib

matplotlib.use("svg")
import matplotlib.pyplot as plt  # noqa: E402

# Reproducible output: the same data draws the same bytes, so regenerating the
# figures changes nothing in the repository unless a number changed.
plt.rcParams["svg.hashsalt"] = "kvroute"
plt.rcParams["font.size"] = 9
SAVE = {"format": "svg", "metadata": {"Date": None}, "bbox_inches": "tight"}

# One colour per policy, the same in every figure, in idea.md §5's order.
POLICY_COLOURS = {
    "round_robin": "#7f7f7f",
    "least_outstanding": "#2ca02c",
    "session_affinity": "#1f77b4",
    "prefix_affinity": "#d62728",
    "exact_residency": "#9467bd",
}
WITHIN_SPREAD = "#e6e6e6"
FAILING_NOTE = "⚠ rests on a repetition that dropped or failed more requests than the threshold allows"
SATURATED_NOTE = "⚠ rests on a repetition that fell behind the load it was offered; its TTFT is not drawn"


def colour(policy):
    return POLICY_COLOURS.get(policy, "#8c564b")


def axis_label(value):
    return f"{value:g}"


def pressure_map(data):
    """The headline figure: the delta at each point of the grid, coloured only
    where it separated beyond the run-to-run spread."""
    ws, skews = data["working_sets"], data["skews"]
    loads = list(dict.fromkeys(d["load_label"] for d in data["deltas"])) or [""]
    fig, axes = plt.subplots(1, len(loads), squeeze=False,
                             figsize=((1.8 + 1.6 * len(skews)) * len(loads), 1.2 + 0.9 * len(ws)))
    separated = [abs(d["percent_change"]) for d in data["deltas"] if d["separated"] and not d["baseline_zero"]]
    limit = max(separated, default=1.0)
    norm = matplotlib.colors.Normalize(vmin=-limit, vmax=limit)
    cmap = plt.get_cmap("RdBu")

    for ax, load in zip(axes[0], loads):
        for row, w in enumerate(ws):
            for col, s in enumerate(skews):
                delta = next((d for d in data["deltas"]
                              if d["working_set"] == w and d["skew"] == s and d["load_label"] == load), None)
                ink = "black"
                if delta is None:
                    face, text = "white", "not run"
                elif delta["separated"] and not delta["baseline_zero"]:
                    face, text = cmap(norm(delta["percent_change"])), delta["label"]
                    if abs(norm(delta["percent_change"]) - 0.5) > 0.3:
                        ink = "white"  # legible on the darkest cells
                else:
                    face, text = WITHIN_SPREAD, delta["label"]
                ax.add_patch(plt.Rectangle((col, row), 1, 1, facecolor=face, edgecolor="white", linewidth=2))
                ax.text(col + 0.5, row + 0.5, text.replace(" (", "\n("), ha="center", va="center", fontsize=8, color=ink)

        ax.set_xlim(0, len(skews))
        ax.set_ylim(len(ws), 0)
        ax.set_xticks([c + 0.5 for c in range(len(skews))], [axis_label(s) for s in skews])
        ax.set_yticks([r + 0.5 for r in range(len(ws))], [axis_label(w) for w in ws])
        ax.set_xlabel("Zipf skew")
        ax.set_ylabel("working set ratio (configured)")
        ax.tick_params(length=0)
        for spine in ax.spines.values():
            spine.set_visible(False)
        ax.set_title(f"at {load}", fontsize=9)

    fig.suptitle(f"Δ goodput, {data['challenger']} against {data['baseline']}\n"
                 f"positive = {data['challenger']} served more inside the SLO; "
                 f"grey = inside the run-to-run spread", fontsize=9)
    fig.colorbar(matplotlib.cm.ScalarMappable(norm=norm, cmap=cmap), ax=axes[0].tolist(),
                 label="Δ goodput, %", shrink=0.8)
    notes = ["⚠ " + cell for cell in data["surfaced"]]
    if notes:
        notes.append(FAILING_NOTE)
    if data["missing"]:
        notes.append(f"not run: {', '.join(data['missing'])}")
    if notes:
        fig.text(0.01, -0.02, "\n".join(notes), fontsize=7, va="top")
    return fig


def _series(points, policy, driver):
    return sorted((p for p in points if p["policy"] == policy and p["driver"] == driver), key=lambda p: p["load"])


def _drivers(points):
    order = {"closed-loop": 0, "open-loop": 1}
    return sorted({p["driver"] for p in points}, key=lambda d: order.get(d, 2))


def _load_axis(ax, driver, loads):
    """Label a load axis with the rungs that were run, and nothing between them."""
    if driver == "closed-loop":
        ax.set_xscale("log", base=2)
        ax.set_xlabel("concurrency, virtual users (closed-loop)")
    else:
        ax.set_xlabel("arrival rate, req/s offered (open-loop)")
    ax.set_xticks(sorted(set(loads)))
    ax.xaxis.set_major_formatter(matplotlib.ticker.FuncFormatter(lambda v, _: axis_label(v)))
    ax.xaxis.set_minor_locator(matplotlib.ticker.NullLocator())


def _loads(points, driver):
    return [p["load"] for p in points if p["driver"] == driver]


def goodput(data):
    """Goodput against load, one panel per driver: the median with the range of
    the repetitions behind it."""
    drivers = _drivers(data["points"])
    fig, axes = plt.subplots(1, max(len(drivers), 1), figsize=(5.2 * max(len(drivers), 1), 3.6), squeeze=False)
    failing = saturated = False
    for ax, driver in zip(axes[0], drivers):
        for policy in data["policies"]:
            series = _series(data["points"], policy, driver)
            if not series:
                continue
            x = [p["load"] for p in series]
            ax.plot(x, [p["goodput_median"] for p in series], marker="o", markersize=3, color=colour(policy), label=policy)
            ax.fill_between(x, [p["goodput_min"] for p in series], [p["goodput_max"] for p in series],
                            color=colour(policy), alpha=0.15, linewidth=0)
            failing = failing or any(p["over_failure_threshold"] for p in series)
            saturated = saturated or any(p["saturated"] for p in series)
            marked = [p for p in series if p["over_failure_threshold"] or p["saturated"]]
            if marked:
                ax.plot([p["load"] for p in marked], [p["goodput_median"] for p in marked], linestyle="none",
                        marker="o", markersize=9, markerfacecolor="none", markeredgecolor="black", label="_marked")
        _load_axis(ax, driver, _loads(data["points"], driver))
        ax.set_ylabel("goodput, req/s inside the SLO")
        ax.set_ylim(bottom=0)
        ax.grid(alpha=0.3)
        ax.legend(fontsize=7)
    slo = data["slo"]
    fig.suptitle(f"Goodput against load — SLO TTFT < {slo['ttft_ms']:g} ms, inter-token p50 < {slo['itl_ms']:g} ms; "
                 f"band = range across repetitions", fontsize=9)
    notes = [note for note, shown in ((FAILING_NOTE, failing), (SATURATED_NOTE, saturated)) if shown]
    if notes:
        fig.text(0.01, -0.02, "ringed: " + "\nringed: ".join(notes), fontsize=7, va="top")
    return fig


def _gapped(series, key, scale=1.0):
    """A column as plot values, with an unread counter as a gap rather than a zero."""
    return [math.nan if p[key] is None else p[key] * scale for p in series]


def cache(data):
    """The mechanism: what each policy left the prefix caches serving, and the
    prefill per request it left the GPUs over the policy that computed least."""
    drivers = _drivers(data["points"])
    fig, axes = plt.subplots(2, max(len(drivers), 1), figsize=(5.2 * max(len(drivers), 1), 6), squeeze=False)
    for col, driver in enumerate(drivers):
        hit, redundant = axes[0][col], axes[1][col]
        for policy in data["policies"]:
            series = _series(data["points"], policy, driver)
            if not series:
                continue
            x = [p["load"] for p in series]
            hit.plot(x, _gapped(series, "prefix_cache_hit_rate", 100), marker="o", markersize=3,
                     color=colour(policy), label=policy)
            redundant.plot(x, _gapped(series, "redundant_prefill_per_request"), marker="o", markersize=3,
                           color=colour(policy), label=policy)
        for ax in (hit, redundant):
            _load_axis(ax, driver, _loads(data["points"], driver))
            ax.grid(alpha=0.3)
        hit.set_ylabel("prefix cache hit rate, %")
        hit.set_ylim(0, 100)
        hit.legend(fontsize=7)
        redundant.set_ylabel("redundant prefill, tokens / request")
        redundant.set_ylim(bottom=0)
    fig.suptitle("What produced it — vLLM's own counters; a gap is a counter nobody read, never a zero", fontsize=9)
    fig.text(0.01, -0.01, "Redundant prefill is tokens per request over the policy that recomputed least per request on "
             "identical bytes, so it is also a gap wherever any policy had no usable cell.\nPer request because under a "
             "closed loop a faster policy offers more prompts in the same window: absolute totals rise with throughput "
             "and credit the slower policy with wasting less.", fontsize=7, va="top")
    return fig


def recovery(data):
    """Goodput through one replica failure, per policy, with the replica's
    events and each run's drops."""
    fig, ax = plt.subplots(figsize=(7.5, 3.6))
    for run in data["runs"]:
        ax.plot([p["at_s"] for p in run["curve"]], [p["goodput_rps"] for p in run["curve"]],
                drawstyle="steps-post", color=colour(run["policy"]),
                label=f"{run['policy']} — {run['dropped_mid_stream']} dropped mid-stream, "
                      f"{run['dropped_unplaced']} never placed, {run['rerouted']} rerouted")
        for event in run["events"]:
            if event["kind"] in ("out_of_rotation", "in_rotation"):
                ax.axvline(event["at_s"], color=colour(run["policy"]), linestyle=":", linewidth=1)
    ax.axvline(0, color="black", linewidth=1)
    ax.text(0, 1.01, f" {data['replica']} {data['fault_verb']}", transform=ax.get_xaxis_transform(), fontsize=7)
    ax.axvline(data["recover_at_s"], color="black", linestyle="--", linewidth=1)
    ax.text(data["recover_at_s"], 1.01, " restart begun", transform=ax.get_xaxis_transform(), fontsize=7)
    ax.set_xlabel(f"seconds from the fault ({data['bucket_s']:g} s buckets, by when a request was offered)")
    ax.set_ylabel("goodput, req/s inside the SLO")
    ax.set_ylim(bottom=0)
    ax.grid(alpha=0.3)
    ax.legend(fontsize=7, loc="lower right")
    ax.set_title(f"Recovery at {data['arrival_rate']:g} req/s offered — dotted: out of and back into rotation",
                 fontsize=9, pad=14)
    return fig


def overhead(data):
    """The router's own cost per policy, accept to first dispatch."""
    policies = data["policies"]
    fig, ax = plt.subplots(figsize=(6, 0.9 + 0.5 * len(policies)))
    names = [p["policy"] for p in policies]
    y = range(len(policies))
    ax.barh([i + 0.2 for i in y], [p["p99_us"] for p in policies], height=0.4, color="#c7c7c7", label="p99")
    ax.barh([i - 0.2 for i in y], [p["p50_us"] for p in policies], height=0.4,
            color=[colour(n) for n in names], label="p50")
    ax.set_yticks(list(y), [f"{n}\n({p['dispatched']:,} requests)" for n, p in zip(names, policies)])
    ax.invert_yaxis()
    ax.set_xlabel("router overhead, µs")
    ax.grid(axis="x", alpha=0.3)
    ax.legend(fontsize=7)
    ax.set_title("Router overhead — accept to first dispatch, off the router's own rows", fontsize=9)
    return fig


def draw(path):
    """Every figure a data file draws, as (suffix, figure) pairs."""
    data = json.loads(Path(path).read_text())
    kind = Path(path).stem.split("-")[0]
    if kind == "pressuremap":
        return [("", pressure_map(data))]
    if kind == "comparison":
        return [("-goodput", goodput(data)), ("-cache", cache(data))]
    if kind == "recovery":
        return [("", recovery(data))]
    if kind == "overhead":
        return [("", overhead(data))]
    raise ValueError(f"{path}: not a kind of figure data this draws (pressuremap, comparison, recovery, overhead)")


def main(data_dir, out_dir):
    sources = sorted(Path(data_dir).glob("*.json"))
    if not sources:
        raise SystemExit(f"figures: no figure data in {data_dir}; run `make figures`")
    Path(out_dir).mkdir(parents=True, exist_ok=True)
    written = []
    for source in sources:
        for suffix, fig in draw(source):
            target = Path(out_dir) / f"{source.stem}{suffix}.svg"
            fig.savefig(target, **SAVE)
            plt.close(fig)
            written.append(target)
            print(f"figures: wrote {target}", file=sys.stderr)
    return written


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("usage: figures.py <data dir> <out dir>")
    main(sys.argv[1], sys.argv[2])
