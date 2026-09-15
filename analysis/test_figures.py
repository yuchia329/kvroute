"""Tests for the plotting script: each figure draws from figure data shaped as
the Go commands write it, and the marks that qualify a number appear.

    make figures-test
"""

import json
import math

import pytest

import figures

SLO = {"ttft_ms": 990, "itl_ms": 24}


def point(policy, load, median, **extra):
    p = {
        "policy": policy, "driver": "closed-loop", "load": load, "load_label": f"{load} users",
        "repetitions": 3, "goodput_median": median, "goodput_min": median - 1, "goodput_max": median + 1,
        "over_failure_threshold": 0, "saturated": 0, "ttft_p50_ms": 300, "ttft_p90_ms": 500, "ttft_p99_ms": 900,
        "prefix_cache_hit_rate": 0.5, "requests": 100.0,
        "recomputed_prefill": 1000.0, "recomputed_prefill_per_request": 10.0,
        "redundant_prefill": 0.0, "redundant_prefill_per_request": 0.0,
    }
    p.update(extra)
    return p


COMPARISON = {
    "slo": SLO, "workload": "fixture", "policies": ["session_affinity", "prefix_affinity"],
    "points": [
        point("session_affinity", 8, 5),
        point("session_affinity", 32, 6, prefix_cache_hit_rate=None,
              redundant_prefill=None, redundant_prefill_per_request=None),
        point("prefix_affinity", 8, 7), point("prefix_affinity", 32, 9, over_failure_threshold=1),
    ],
    "excluded": [], "surfaced": ["`prefix_affinity-c32-r2`: failure rate 5.00% exceeds the 1.00% threshold"],
}

PRESSURE_MAP = {
    "baseline": "session_affinity", "challenger": "prefix_affinity", "slo": SLO,
    "policies": ["session_affinity", "prefix_affinity"], "working_sets": [0.25, 1], "skews": [0, 1.4],
    "deltas": [
        {"working_set": 0.25, "skew": 0, "load_label": "32 users", "measured": True, "replicated": True,
         "separated": False, "baseline_zero": False, "percent_change": 9.1, "label": "+9.1% (within spread)"},
        {"working_set": 1, "skew": 1.4, "load_label": "32 users", "measured": True, "replicated": True,
         "separated": True, "baseline_zero": False, "percent_change": 200, "label": "+200.0%"},
    ],
    "goodput": [], "missing": ["WS 1, skew 0"], "refused": [], "excluded": [], "surfaced": [],
}

RECOVERY = {
    "replica": "replica-2", "fault": "kill", "fault_verb": "killed", "arrival_rate": 8, "slo": SLO,
    "bucket_s": 5, "recover_at_s": 60, "tolerance": 0.1,
    "runs": [{
        "policy": "session_affinity",
        "curve": [{"at_s": -5, "goodput_rps": 8, "offered": 40, "rerouted": 0, "dropped": 0},
                  {"at_s": 0, "goodput_rps": 3, "offered": 40, "rerouted": 4, "dropped": 6}],
        "events": [{"at_s": 0, "kind": "fault", "label": "fault injected"},
                   {"at_s": 2.1, "kind": "out_of_rotation", "label": "out of rotation"}],
        "baseline_rps": 8, "trough_rps": 3, "trough_at_s": 0, "deficit_requests": 25, "steady": False,
        "steady_at_s": 0, "rerouted": 4, "dropped_mid_stream": 6, "dropped_unplaced": 0, "drop_accounting": "",
    }],
}

OVERHEAD = {
    "sources": ["router.jsonl"],
    "policies": [{"policy": "round_robin", "dispatched": 3, "p50_us": 200, "p99_us": 300, "max_us": 300,
                  "tokenized": 0, "tokenize_p50_us": None, "tokenize_p99_us": None}],
}


def texts(fig):
    return [t.get_text() for ax in fig.axes for t in ax.texts] + [t.get_text() for t in fig.texts]


def test_the_pressure_map_labels_each_point_as_the_table_does():
    shown = texts(figures.pressure_map(PRESSURE_MAP))
    assert "+9.1%\n(within spread)" in shown
    assert "+200.0%" in shown
    assert "not run" in shown, "a grid point with no cells must read as not run, not as zero"


def test_the_pressure_map_colours_only_what_separated():
    fig = figures.pressure_map(PRESSURE_MAP)
    faces = {tuple(round(c, 3) for c in patch.get_facecolor()[:3]) for patch in fig.axes[0].patches}
    grey = tuple(round(c, 3) for c in matplotlib_rgb(figures.WITHIN_SPREAD))
    assert grey in faces, "the within-spread point is not drawn in the neutral grey"
    assert len(faces) == 3, "want one colour for the separated point, grey, and white for the point not run"


def matplotlib_rgb(hex_colour):
    import matplotlib.colors
    return matplotlib.colors.to_rgb(hex_colour)


def test_a_second_load_rung_gets_its_own_map():
    """The grid runs at one rung today, and a map that silently drew only the
    first would average two rungs' behaviour into one claim."""
    two = dict(PRESSURE_MAP, deltas=PRESSURE_MAP["deltas"] + [
        dict(PRESSURE_MAP["deltas"][1], load_label="64 users", percent_change=10, label="+10.0%")])

    fig = figures.pressure_map(two)

    drawn = [ax for ax in fig.axes if ax.patches]
    assert len(drawn) == 2, "a second load rung was dropped from the map"
    assert "+10.0%" in texts(fig)


def test_the_pressure_map_names_the_cells_a_figure_rests_on():
    named = dict(PRESSURE_MAP, surfaced=["`session_affinity-c32-r2`: failure rate 5.00% exceeds the 1.00% threshold"])

    shown = "\n".join(texts(figures.pressure_map(named)))

    assert "session_affinity-c32-r2" in shown, "the map counts failing cells without naming them"


def test_goodput_rings_a_figure_resting_on_a_failing_repetition():
    fig = figures.goodput(COMPARISON)
    ringed = [line for line in fig.axes[0].lines if line.get_label() == "_marked"]
    assert len(ringed) == 1 and list(ringed[0].get_xdata()) == [32]
    assert any("failed more requests" in t for t in texts(fig))


def test_goodput_rings_a_figure_resting_on_a_repetition_past_saturation():
    saturated = dict(COMPARISON, points=[
        point("session_affinity", 8, 5), point("prefix_affinity", 8, 0.1, saturated=3, ttft_p50_ms=None)])

    fig = figures.goodput(saturated)

    ringed = [line for line in fig.axes[0].lines if line.get_label() == "_marked"]
    assert len(ringed) == 1 and list(ringed[0].get_xdata()) == [8]
    assert any("fell behind the load it was offered" in t for t in texts(fig))
    assert not any("failed more requests" in t for t in texts(fig)), "a saturated point was explained as a failing one"


def test_an_unread_counter_is_a_gap_in_the_cache_figure_not_a_zero():
    fig = figures.cache(COMPARISON)
    session = next(line for line in fig.axes[0].lines if line.get_label() == "session_affinity")
    y = list(session.get_ydata())
    assert y[0] == 50
    assert math.isnan(y[1]), "an unread hit rate was drawn as a number"


def test_the_recovery_figure_carries_each_runs_drops():
    fig = figures.recovery(RECOVERY)
    labels = [line.get_label() for line in fig.axes[0].lines]
    assert any("6 dropped mid-stream, 0 never placed, 4 rerouted" in label for label in labels)


def test_every_data_file_is_drawn_to_an_svg(tmp_path):
    data = tmp_path / "data"
    data.mkdir()
    for name, payload in [("pressuremap", PRESSURE_MAP), ("comparison", COMPARISON),
                          ("recovery-kill", RECOVERY), ("overhead", OVERHEAD)]:
        (data / f"{name}.json").write_text(json.dumps(payload))

    written = figures.main(data, tmp_path / "out")

    assert sorted(p.name for p in written) == [
        "comparison-cache.svg", "comparison-goodput.svg", "overhead.svg", "pressuremap.svg", "recovery-kill.svg"]
    first = (tmp_path / "out" / "pressuremap.svg").read_bytes()
    figures.main(data, tmp_path / "out")
    assert (tmp_path / "out" / "pressuremap.svg").read_bytes() == first, "the same data drew different bytes"


def test_an_unknown_kind_of_data_is_refused(tmp_path):
    (tmp_path / "heatmap.json").write_text("{}")
    with pytest.raises(ValueError):
        figures.draw(tmp_path / "heatmap.json")
