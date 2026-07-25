#!/usr/bin/env python3
"""Generate the SWE-bench baseline-vs-config figures for RESULTS.md as PNGs.
Applies the dataviz method: validated CVD-safe categorical palette (blue/orange),
one axis per chart, thin marks, recessive grid, legend for 2 series, text in ink
(not series color). Run with /tmp/cg-runs/plotenv/bin/python.

Usage: plots.py <analysis.json> <cachecost.json> --out DIR
"""
import argparse, json
from pathlib import Path
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.ticker import FuncFormatter

# validated palette (light surface)
SURFACE = "#fcfcfb"; INK = "#0b0b0b"; INK2 = "#52514e"; GRID = "#e6e5e2"
BASE = "#2a78d6"   # slot 1 blue  = baseline
CFG = "#eb6834"    # slot 2 orange = config
COMP = ["#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4", "#008300"]

plt.rcParams.update({
    "figure.facecolor": SURFACE, "axes.facecolor": SURFACE, "savefig.facecolor": SURFACE,
    "text.color": INK, "axes.labelcolor": INK2, "xtick.color": INK2, "ytick.color": INK2,
    "axes.edgecolor": GRID, "font.size": 10, "axes.titlesize": 12, "axes.titleweight": "bold",
    "axes.grid": True, "grid.color": GRID, "grid.linewidth": 0.8, "axes.axisbelow": True,
    "figure.dpi": 130,
})


def style(ax):
    for s in ("top", "right"):
        ax.spines[s].set_visible(False)
    ax.tick_params(length=0)


def dumbbell_cost(rows, out):
    rows = sorted(rows, key=lambda r: r["c_cost"])
    n = len(rows); y = range(n)
    fig, ax = plt.subplots(figsize=(8, max(6, n * 0.22)))
    for i, r in enumerate(rows):
        ax.plot([r["b_cost"], r["c_cost"]], [i, i], color=GRID, lw=2, zorder=1, solid_capstyle="round")
    ax.scatter([r["b_cost"] for r in rows], list(y), s=42, color=BASE, zorder=3, label="baseline (off)")
    ax.scatter([r["c_cost"] for r in rows], list(y), s=42, color=CFG, zorder=3, label="codesmart")
    ax.set_yticks(list(y)); ax.set_yticklabels([r["task"] for r in rows], fontsize=7)
    ax.set_xlabel("billed input cost per task ($)"); ax.set_title("Per-task cost: baseline vs codesmart")
    ax.legend(loc="lower right", frameon=False)
    ax.grid(axis="y", visible=False)
    style(ax); fig.tight_layout(); fig.savefig(out); plt.close(fig)


def diverging_delta(rows, key_b, key_c, title, xlabel, out, fmt=None):
    rows = sorted(rows, key=lambda r: r[key_c] - r[key_b])
    n = len(rows)
    fig, ax = plt.subplots(figsize=(8, max(6, n * 0.22)))
    for i, r in enumerate(rows):
        d = r[key_c] - r[key_b]
        ax.barh(i, d, color=(CFG if d > 0 else BASE), height=0.62, zorder=2)
    ax.axvline(0, color=INK2, lw=1)
    ax.set_yticks(range(n)); ax.set_yticklabels([r["task"] for r in rows], fontsize=7)
    ax.set_xlabel(xlabel); ax.set_title(title)
    if fmt: ax.xaxis.set_major_formatter(FuncFormatter(fmt))
    ax.grid(axis="y", visible=False)
    # annotate legend meaning
    ax.text(0.98, 0.02, "orange = codesmart higher   ·   blue = codesmart lower",
            transform=ax.transAxes, ha="right", va="bottom", fontsize=8, color=INK2)
    style(ax); fig.tight_layout(); fig.savefig(out); plt.close(fig)


def component_savings(per_comp, out):
    items = [(k, v["saved_tokens"]) for k, v in per_comp.items() if v.get("saved_tokens")]
    items.sort(key=lambda x: x[1])
    fig, ax = plt.subplots(figsize=(7, 3.2))
    for i, (k, v) in enumerate(items):
        ax.barh(i, v, color=COMP[i % len(COMP)], height=0.6, zorder=2)
        ax.text(v, i, f"  {v:,}", va="center", fontsize=9, color=INK)
    ax.set_yticks(range(len(items))); ax.set_yticklabels([k for k, _ in items])
    ax.set_xlabel("content tokens removed (whole run)")
    ax.set_title("Per-component content savings")
    ax.grid(axis="y", visible=False)
    style(ax); fig.tight_layout(); fig.savefig(out); plt.close(fig)


def cost_waterfall(analysis, out):
    d = analysis["decomposition"]; b = analysis["cost"][0]
    steps = [("baseline", b, INK2), ("+cache_write", d["cache_write"], CFG),
             ("−cache_read", d["cache_read"], BASE), ("+fresh", d["fresh"], CFG),
             ("+CG LLM", d["cg_llm"], COMP[3])]
    fig, ax = plt.subplots(figsize=(7, 3.6))
    run = 0; x = 0
    ax.bar(x, b, color=INK2, width=0.6, zorder=2); ax.text(x, b, f"${b:.1f}", ha="center", va="bottom", fontsize=9)
    run = b
    for name, val, col in steps[1:]:
        x += 1
        ax.bar(x, val, bottom=run, color=col, width=0.6, zorder=2)
        ax.text(x, run + max(val, 0), f"{val:+.2f}", ha="center", va="bottom", fontsize=8, color=INK)
        run += val
    x += 1
    ax.bar(x, run, color=CFG, width=0.6, zorder=2); ax.text(x, run, f"${run:.1f}", ha="center", va="bottom", fontsize=9)
    ax.set_xticks(range(len(steps) + 1)); ax.set_xticklabels([s[0] for s in steps] + ["codesmart"], fontsize=8, rotation=20, ha="right")
    ax.set_ylabel("matched-total billed cost ($)"); ax.set_title("What moves the cost (live run — step-noise included)")
    ax.grid(axis="x", visible=False)
    style(ax); fig.tight_layout(); fig.savefig(out); plt.close(fig)


def headline(cachecost, out):
    # deterministic: content saved vs cached-cost vs non-cached-cost
    cc = cachecost
    content = 100 * (1 - (cc["config"]["cache_read"] + cc["config"]["cache_write"]) /
                     (cc["baseline"]["cache_read"] + cc["baseline"]["cache_write"]))
    cached = cc["delta_pct"]
    noncached = -content  # content reduction = direct cost saving with no cache
    labels = ["content tokens", "cost — cached agent", "cost — non-cached agent"]
    vals = [-content, cached, noncached]
    fig, ax = plt.subplots(figsize=(7.5, 3))
    lim = max(abs(min(vals)), abs(max(vals))) * 1.35 + 1
    ax.set_xlim(-lim, lim)
    for i, (l, v) in enumerate(zip(labels, vals)):
        ax.barh(i, v, color=(BASE if v < 0 else CFG), height=0.55, zorder=2)
        # label just beyond the bar tip, pointing outward, clear of the y-axis labels
        ax.text(v + (0.15 if v >= 0 else -0.15), i, f"{v:+.1f}%", va="center",
                ha=("left" if v >= 0 else "right"), fontsize=10, color=INK)
    ax.axvline(0, color=INK2, lw=1)
    ax.set_yticks(range(3)); ax.set_yticklabels(labels)
    ax.set_xlabel("% change vs baseline (deterministic, identical traffic)")
    ax.set_title("The honest headline: content saved ≠ billed-cost saved")
    ax.grid(axis="y", visible=False)
    style(ax); fig.tight_layout(); fig.savefig(out); plt.close(fig)


def unique_vs_cumulative(A, out):
    us = A.get("unique_savings") or {}
    if not us:
        return
    items = [(k, v.get("cum_saved", 0), v.get("uniq_saved", 0)) for k, v in us.items()]
    items = [it for it in items if it[1] or it[2]]
    items.sort(key=lambda x: x[2])
    n = len(items)
    fig, ax = plt.subplots(figsize=(7.5, max(2.4, n * 0.5)))
    y = range(n)
    for i, (k, cum, uq) in enumerate(items):
        ax.barh(i + 0.18, cum, height=0.34, color=BASE, zorder=2, label="cumulative (/stats)" if i == 0 else "")
        ax.barh(i - 0.18, uq, height=0.34, color=CFG, zorder=2, label="unique (deduped)" if i == 0 else "")
        r = cum / uq if uq else 0
        ax.text(cum, i + 0.18, f"  {cum:,} ({r:.0f}×)", va="center", fontsize=8, color=INK2)
        ax.text(uq, i - 0.18, f"  {uq:,}", va="center", fontsize=8, color=INK)
    ax.set_yticks(list(y)); ax.set_yticklabels([k for k, _, _ in items], fontsize=8)
    ax.set_xscale("log"); ax.set_xlabel("tokens saved (log scale)")
    ax.set_title("Per-component savings: cumulative vs UNIQUE (over-count exposed)")
    ax.legend(loc="lower right", frameon=False, fontsize=8)
    ax.grid(axis="y", visible=False)
    style(ax); fig.tight_layout(); fig.savefig(out); plt.close(fig)


def latency_cache(A, out):
    lat = A.get("latency") or {}
    ch = A.get("cache_hit") or [None, None]
    fig, axes = plt.subplots(1, 3, figsize=(9, 3))
    # CG-added latency per request
    axes[0].bar(["baseline", "codesmart"], [lat.get("agent_wall_base") or 0, lat.get("agent_wall_cfg") or 0],
                color=[BASE, CFG], width=0.6, zorder=2)
    axes[0].set_title("agent wall / task (s)"); axes[0].set_ylabel("seconds")
    # cache hit
    axes[1].bar(["baseline", "codesmart"], [100 * (ch[0] or 0), 100 * (ch[1] or 0)], color=[BASE, CFG], width=0.6, zorder=2)
    axes[1].set_ylim(90, 100); axes[1].set_title("cache-hit rate (%)")
    # CG added ms/req
    axes[2].bar(["codesmart"], [lat.get("cg_added_ms") or 0], color=CFG, width=0.4, zorder=2)
    axes[2].set_title("CG-added latency / req (ms)")
    for ax in axes:
        style(ax); ax.grid(axis="x", visible=False)
    fig.suptitle("Latency & cache — with vs without context-guru", fontsize=12, fontweight="bold")
    fig.tight_layout(); fig.savefig(out); plt.close(fig)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("analysis"); ap.add_argument("--cachecost", default=""); ap.add_argument("--out", default="/tmp/cg-runs/plots")
    a = ap.parse_args()
    A = json.load(open(a.analysis))
    Path(a.out).mkdir(parents=True, exist_ok=True)
    rows = A["rows"]
    dumbbell_cost(rows, f"{a.out}/per_task_cost.png")
    diverging_delta(rows, "b_cost", "c_cost", "Per-task cost delta (codesmart − baseline)", "Δ billed cost ($)", f"{a.out}/per_task_dcost.png")
    diverging_delta(rows, "b_steps", "c_steps", "Per-task step delta", "Δ steps", f"{a.out}/per_task_dsteps.png")
    component_savings(A["per_component"], f"{a.out}/component_savings.png")
    cost_waterfall(A, f"{a.out}/cost_waterfall.png")
    unique_vs_cumulative(A, f"{a.out}/unique_vs_cumulative.png")
    latency_cache(A, f"{a.out}/latency_cache.png")
    n = 7
    if a.cachecost:
        headline(json.load(open(a.cachecost)), f"{a.out}/headline.png"); n += 1
    print(f"wrote {n} figures to {a.out}")


if __name__ == "__main__":
    main()
