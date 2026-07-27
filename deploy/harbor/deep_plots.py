#!/usr/bin/env python3
"""Comprehensive 3-way figures (baseline / context-guru / headroom) from deep_analysis.json.
Validated CVD-safe palette, one axis per chart, thin marks, recessive grid, legend for
multi-series. Run with /tmp/cg-runs/plotenv/bin/python.

Usage: deep_plots.py deep_analysis.json --out DIR
"""
import argparse, json
from pathlib import Path
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

SURFACE = "#fcfcfb"; INK = "#0b0b0b"; INK2 = "#52514e"; GRID = "#e6e5e2"
# baseline grey, context-guru blue, headroom orange, rtk teal-green
COL = {"baseline": "#8a8985", "context-guru": "#2a78d6", "headroom": "#eb6834", "rtk": "#1baf7a"}
COMP = ["#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4", "#008300"]
plt.rcParams.update({
    "figure.facecolor": SURFACE, "axes.facecolor": SURFACE, "savefig.facecolor": SURFACE,
    "text.color": INK, "axes.labelcolor": INK2, "xtick.color": INK2, "ytick.color": INK2,
    "axes.edgecolor": GRID, "font.size": 10, "axes.titlesize": 12, "axes.titleweight": "bold",
    "axes.grid": True, "grid.color": GRID, "grid.linewidth": 0.8, "axes.axisbelow": True, "figure.dpi": 130,
})


def style(ax):
    for s in ("top", "right"):
        ax.spines[s].set_visible(False)
    ax.tick_params(length=0)


def bars(ax, agg, fn, title, ylabel, fmt="{:.0f}", cfgs=None):
    cfgs = cfgs or list(agg)
    vals = [fn(agg[c]) for c in cfgs]
    xs = range(len(cfgs))
    ax.bar(xs, vals, color=[COL[c] for c in cfgs], width=0.6, zorder=2)
    for x, v in zip(xs, vals):
        ax.text(x, v, "  " + fmt.format(v), ha="center", va="bottom", fontsize=9, color=INK)
    ax.set_xticks(list(xs)); ax.set_xticklabels(cfgs, fontsize=9)
    ax.set_title(title); ax.set_ylabel(ylabel)
    ax.grid(axis="x", visible=False); style(ax)
    ax.set_ylim(0, max(vals) * 1.18)


def fig_headline(A, out):
    agg, cfgs = A["aggregate"], A["configs"]
    fig, ax = plt.subplots(2, 3, figsize=(12, 6.4))
    bars(ax[0][0], agg, lambda x: x["solved"], f"Reward (solved / {A['matched_tasks']})", "tasks", "{:.0f}")
    bars(ax[0][1], agg, lambda x: x["billed_cost"], "Billed input cost (matched total)", "$", "${:.1f}")
    bars(ax[0][2], agg, lambda x: x["mean_steps"], "Mean agent steps / task", "steps", "{:.1f}")
    bars(ax[1][0], agg, lambda x: x["cache_read"] / 1e6, "Cache-read tokens", "M tokens", "{:.1f}")
    bars(ax[1][1], agg, lambda x: x["cache_write"] / 1e6, "Cache-write tokens", "M tokens", "{:.2f}")
    tool = A["tool"]
    lat = {c: tool[c]["added_ms"] for c in cfgs}
    ax[1][2].bar(range(len(cfgs)), [lat[c] for c in cfgs], color=[COL[c] for c in cfgs], width=0.6, zorder=2)
    for x, c in enumerate(cfgs):
        ax[1][2].text(x, lat[c], f"  {lat[c]:.0f}", ha="center", va="bottom", fontsize=9, color=INK)
    ax[1][2].set_xticks(range(len(cfgs))); ax[1][2].set_xticklabels(cfgs, fontsize=9)
    ax[1][2].set_title("Compaction latency added / request"); ax[1][2].set_ylabel("ms")
    ax[1][2].grid(axis="x", visible=False); style(ax[1][2])
    fig.suptitle("%s — SWE-bench Verified (matched %d tasks)" % (" vs ".join(cfgs), A["matched_tasks"]),
                 fontsize=13, fontweight="bold")
    fig.tight_layout(); fig.savefig(out); plt.close(fig)


def fig_cost_decomp(A, out):
    agg, cfgs = A["aggregate"], A["configs"]
    fig, ax = plt.subplots(figsize=(8, 4))
    import numpy as np
    x = np.arange(len(cfgs)); w = 0.6
    cr = [agg[c]["cache_read_cost"] for c in cfgs]
    cw = [agg[c]["cache_write_cost"] for c in cfgs]
    llm = [A["tool"][c]["llm_cost"] for c in cfgs]
    ax.bar(x, cr, w, label="cache-read", color="#2a78d6", zorder=2)
    ax.bar(x, cw, w, bottom=cr, label="cache-write", color="#eb6834", zorder=2)
    ax.bar(x, llm, w, bottom=[a + b for a, b in zip(cr, cw)], label="tool LLM", color="#eda100", zorder=2)
    for xi, c in enumerate(cfgs):
        tot = cr[xi] + cw[xi] + llm[xi]
        ax.text(xi, tot, f"  ${tot:.1f}", ha="center", va="bottom", fontsize=9, color=INK)
    ax.set_xticks(x); ax.set_xticklabels(cfgs, fontsize=9)
    ax.set_ylabel("$ (matched total)"); ax.set_title("Cost decomposition: cache-read + cache-write + tool LLM")
    ax.legend(frameon=False, fontsize=8); ax.grid(axis="x", visible=False); style(ax)
    fig.tight_layout(); fig.savefig(out); plt.close(fig)


def fig_per_task_cost(A, out):
    rows = sorted(A["per_task"], key=lambda r: r["baseline"]["cost"])
    n = len(rows)
    fig, ax = plt.subplots(figsize=(8, max(6, n * 0.22)))
    lo = lambda r: min(r[c]["cost"] for c in A["configs"] if c != "baseline")
    hi = lambda r: max(r[c]["cost"] for c in A["configs"] if c != "baseline")
    for i, r in enumerate(rows):
        ax.plot([lo(r), hi(r)], [i, i], color=GRID, lw=1.5, zorder=1)
    markers = {"baseline": "D", "headroom": "o", "context-guru": "o", "rtk": "s"}
    for c in A["configs"]:
        ax.scatter([r[c]["cost"] for r in rows], range(n), s=26, color=COL[c], zorder=3, label=c,
                   marker=markers.get(c, "o"))
    ax.set_yticks(range(n)); ax.set_yticklabels([r["task"] for r in rows], fontsize=6)
    ax.set_xlabel("billed input cost per task ($)"); ax.set_title("Per-task cost")
    ax.legend(loc="lower right", frameon=False, fontsize=8); ax.grid(axis="y", visible=False); style(ax)
    fig.tight_layout(); fig.savefig(out); plt.close(fig)


def fig_per_task_delta(A, key, title, xlabel, out):
    rows = sorted(A["per_task"], key=lambda r: r["context-guru"][key] - r["baseline"][key])
    n = len(rows)
    fig, ax = plt.subplots(figsize=(8, max(6, n * 0.22)))
    for i, r in enumerate(rows):
        d = r["context-guru"][key] - r["baseline"][key]
        ax.barh(i, d, color=(COL["context-guru"] if d < 0 else COL["headroom"]), height=0.6, zorder=2)
    ax.axvline(0, color=INK2, lw=1)
    ax.set_yticks(range(n)); ax.set_yticklabels([r["task"] for r in rows], fontsize=6)
    ax.set_xlabel(xlabel); ax.set_title(title + " (context-guru − baseline)")
    ax.text(0.98, 0.02, "blue = context-guru lower (better)", transform=ax.transAxes, ha="right",
            va="bottom", fontsize=8, color=INK2)
    ax.grid(axis="y", visible=False); style(ax)
    fig.tight_layout(); fig.savefig(out); plt.close(fig)


def fig_components(A, out):
    cg = A["tool"]["context-guru"]
    uq = (cg.get("unique") or {}).get("components", {})
    hr = A["tool"]["headroom"].get("per_strategy", {})
    rtk = (A["tool"].get("rtk") or {}).get("per_command", {})
    npanel = 3 if rtk else 2
    fig, axes = plt.subplots(1, npanel, figsize=(5.5 * npanel, 3.6))
    # context-guru: cumulative vs unique
    items = [(k, v.get("cum", 0), (uq.get(k, {}) or {}).get("uniq_saved", 0)) for k, v in cg["per_component"].items() if v.get("cum")]
    # map dump categories to component names best-effort
    items.sort(key=lambda x: x[1])
    ax = axes[0]; y = range(len(items))
    ax.barh([i + 0.2 for i in y], [c for _, c, _ in items], height=0.36, color="#8a8985", label="cumulative", zorder=2)
    ax.barh([i - 0.2 for i in y], [u for _, _, u in items], height=0.36, color="#2a78d6", label="unique", zorder=2)
    ax.set_yticks(list(y)); ax.set_yticklabels([k for k, _, _ in items], fontsize=8)
    ax.set_xlabel("tokens removed"); ax.set_title("context-guru — per component"); ax.set_xscale("symlog")
    ax.legend(frameon=False, fontsize=8, loc="lower right"); ax.grid(axis="y", visible=False); style(ax)
    # headroom per-strategy
    hs = sorted([(k, v) for k, v in hr.items() if v], key=lambda x: x[1])
    ax = axes[1]
    ax.barh(range(len(hs)), [v for _, v in hs], color="#eb6834", height=0.6, zorder=2)
    for i, (k, v) in enumerate(hs):
        ax.text(v, i, f"  {v:,}", va="center", fontsize=8, color=INK)
    ax.set_yticks(range(len(hs))); ax.set_yticklabels([k for k, _ in hs], fontsize=8)
    ax.set_xlabel("tokens removed"); ax.set_title("headroom — per compressor")
    ax.grid(axis="y", visible=False); style(ax)
    # rtk per-command (bash-output tokens saved)
    if rtk:
        rs = sorted([(k, v.get("saved", 0)) for k, v in rtk.items() if v.get("saved")], key=lambda x: x[1])
        ax = axes[2]
        ax.barh(range(len(rs)), [v for _, v in rs], color="#1baf7a", height=0.6, zorder=2)
        for i, (k, v) in enumerate(rs):
            ax.text(v, i, f"  {int(v):,}", va="center", fontsize=8, color=INK)
        ax.set_yticks(range(len(rs))); ax.set_yticklabels([k for k, _ in rs], fontsize=8)
        ax.set_xlabel("bash-output tokens removed"); ax.set_title("rtk — per command")
        ax.grid(axis="y", visible=False); style(ax)
    fig.tight_layout(); fig.savefig(out); plt.close(fig)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("analysis"); ap.add_argument("--out", default="/tmp/cg-runs/deep/img")
    a = ap.parse_args()
    A = json.load(open(a.analysis)); Path(a.out).mkdir(parents=True, exist_ok=True)
    fig_headline(A, f"{a.out}/headline.png")
    fig_cost_decomp(A, f"{a.out}/cost_decomposition.png")
    fig_per_task_cost(A, f"{a.out}/per_task_cost.png")
    fig_per_task_delta(A, "cost", "Per-task cost delta", "Δ billed cost ($)", f"{a.out}/per_task_dcost.png")
    fig_per_task_delta(A, "steps", "Per-task step delta", "Δ steps", f"{a.out}/per_task_dsteps.png")
    fig_components(A, f"{a.out}/components.png")
    print(f"wrote 6 figures to {a.out}")


if __name__ == "__main__":
    main()
