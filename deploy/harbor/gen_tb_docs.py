#!/usr/bin/env python3
"""Generate the Terminal-Bench 2.0 BASELINE results doc from a rows.json + task
metadata, mirroring the SWE-bench result pages (totals + cache-aware cost model +
per-task table) but adding TB-specific breakdowns by difficulty and category and a
first-class treatment of timeouts — a baseline characterization of *where* the
claude-code agent is strong/weak, so the later compaction arms have a like-for-like
reference.

All 89 attempted tasks are included. A task that hit AgentTimeout/VerifierTimeout is a
genuine failure (reward 0, agent did not finish within its 1.5× wall-clock budget on
this ~26 s/request gateway) and is flagged as a `timeout`; its partial spend still
counts toward cost. The primary solve rate is solved / attempted (the standard
Terminal-Bench metric); a secondary rate over completed-only tasks is also reported.

Usage:
  gen_tb_docs.py <rows.json> <out.md> [--meta /tmp/tb-runs/task_meta.json] [--summary summary.json]
"""
import argparse, json
from collections import defaultdict

IN, OUT, CREAD, CWRITE = 2e-6, 10e-6, 0.2e-6, 2.5e-6  # same cache-aware price model as the SWE study


def billed(r):
    return (r.get("fresh_input", 0) * IN + r.get("cache_read", 0) * CREAD +
            r.get("cache_write", 0) * CWRITE + r.get("completion_tokens", 0) * OUT)


def agg(rows):
    n = len(rows)
    solved = sum(1 for r in rows if (r.get("reward") or 0) >= 1)
    cost = sum(billed(r) for r in rows)
    steps = sum(r.get("steps", 0) or 0 for r in rows)
    wall = sum(r.get("agent_wall_s", 0) or 0 for r in rows)
    return n, solved, cost, steps, wall


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("rows"); ap.add_argument("out")
    ap.add_argument("--meta", default="/tmp/tb-runs/task_meta.json")
    ap.add_argument("--summary", default="")
    ap.add_argument("--label", default="baseline", help="arm label for the page header")
    ap.add_argument("--kind", default="baseline", choices=["baseline", "arm"],
                    help="baseline emits the full narrative; arm emits a compact per-arm page")
    a = ap.parse_args()
    rows = json.load(open(a.rows))
    rows.sort(key=lambda r: r["task"])
    meta = {}
    try:
        meta = json.load(open(a.meta))
    except Exception:
        pass
    for r in rows:
        m = meta.get(r["task"], {})
        r["_diff"] = m.get("difficulty", "unknown")
        r["_cat"] = m.get("category", "unknown")
        r["_timeout"] = bool(r.get("exception"))

    scored = [r for r in rows if not r["_timeout"]]     # completed within budget
    timeouts = [r for r in rows if r["_timeout"]]

    is_base = a.kind == "baseline"
    L = []
    L.append(f"# Full results — {a.label} (Terminal-Bench 2.0, 89 tasks)\n")
    if is_base:
        L.append("Baseline arm: **no compaction** — the `claude-code` agent on `aws/claude-sonnet-5`, "
                 "run LIVE through the harness against Terminal-Bench 2.0's 89 tasks. Routing goes "
                 "through the context-guru `off` transparent passthrough proxy (identical plumbing to "
                 "the compaction arms; zero content change), so this is the like-for-like reference the "
                 "framework arms are measured against. Cache-aware billed input cost (fresh $2/M · "
                 "cache-read $0.20/M · cache-write $2.50/M) + output $10/M, recomputed from each trial's "
                 "own token tiers — the same model as the SWE-bench study. See [REPRODUCE.md](REPRODUCE.md).\n")
    else:
        L.append(f"Full per-task results for the **{a.label}** arm on Terminal-Bench 2.0 (`claude-code` on "
                 "`aws/claude-sonnet-5`, live). Same cache-aware cost model and 4× budget as the other arms. "
                 "For the four-way analysis (cost decomposition, per-component, verdict) see the "
                 "**[Terminal-Bench comparison](terminal-bench-comparison.md)**; the reference arm is the "
                 "**[baseline](terminal-bench-baseline.md)**. See [REPRODUCE.md](REPRODUCE.md).\n")

    n = len(rows)
    solved = sum(1 for r in rows if (r.get("reward") or 0) >= 1)
    cost = sum(billed(r) for r in rows)
    steps_c = sum(r.get("steps", 0) or 0 for r in scored)
    tcr = sum(r.get("cache_read", 0) for r in rows)
    tcw = sum(r.get("cache_write", 0) for r in rows)
    tfresh = sum(r.get("fresh_input", 0) for r in rows)
    tout = sum(r.get("completion_tokens", 0) for r in rows)
    hit = 100 * tcr / max(tcr + tcw + tfresh, 1)
    wall_c = sum(r.get("agent_wall_s", 0) or 0 for r in scored)

    L.append("## Totals\n")
    L.append("| attempted | solved | solve rate | completed | timed out | total billed cost | mean steps* | cache-hit |")
    L.append("|--:|--:|--:|--:|--:|--:|--:|--:|")
    L.append(f"| {n} | {solved} | **{solved/max(n,1):.1%}** | {len(scored)} | {len(timeouts)} | "
             f"${cost:.2f} | {steps_c/max(len(scored),1):.1f} | {hit:.1f}% |")
    L.append(f"\n\\* mean steps over the {len(scored)} completed tasks (timed-out runs are truncated). "
             f"Solve rate over **completed-only** tasks: **{sum(1 for r in scored if (r.get('reward') or 0)>=1)}/{len(scored)} "
             f"= {sum(1 for r in scored if (r.get('reward') or 0)>=1)/max(len(scored),1):.1%}**.\n")
    if is_base:
        L.append("**Time-budget policy.** Wall-clock budget = the task-authored timeout × a multiplier. Most tasks "
                 "ran at **1.5×**; the long-horizon tasks that first timed out were retried at low concurrency and, "
                 "if still short, given an extended **4×** budget (up to ~4 h) to measure capability rather than a "
                 "latency-truncated result. 3 tasks solved only under the 4× budget (counted as solved here); the "
                 f"{len(timeouts)} below exhausted even 4×.\n")

    L.append("### Token & cost accounting (cache-aware, all 89 tasks)\n")
    L.append("| tier | tokens | $/M | billed |")
    L.append("|---|--:|--:|--:|")
    L.append(f"| cache-read (input) | {tcr:,} | 0.20 | ${tcr*CREAD:.2f} |")
    L.append(f"| cache-write (input) | {tcw:,} | 2.50 | ${tcw*CWRITE:.2f} |")
    L.append(f"| fresh (input) | {tfresh:,} | 2.00 | ${tfresh*IN:.2f} |")
    L.append(f"| completion (output) | {tout:,} | 10.00 | ${tout*OUT:.2f} |")
    L.append(f"| **total** | | | **${cost:.2f}** |")
    L.append(f"\nCache-read is **{100*tcr*CREAD/max(cost,1e-9):.0f}%** of the bill at a **{hit:.1f}%** cache-hit "
             "rate — as on SWE-bench, a heavily-cached agent, so the lever a compaction layer must pull is "
             "cache-read tokens.\n")

    # timeouts — first-class bucket
    L.append(f"## Timeouts ({len(timeouts)} long-horizon tasks)\n")
    L.append("These tasks still hit the wall-clock budget under the **extended 4×** timeout (up to ~4 h each) "
             "and scored **reward 0** — counted as failures in the solve rate above. A large part of the cause "
             "is **gateway latency, not only agent capability**: Terminal-Bench's timeouts assume a fast "
             "endpoint (~2–5 s/request), but this IBM LiteLLM gateway runs **~26 s/request** (5–10× slower), so "
             "long-horizon tasks that need many round-trips run out of clock (concurrency is *not* the cause — "
             "latency was flat ~23–30 s/req from n=1 to n=24). They are all `hard`/long software-engineering "
             "and compute tasks (path-tracing, a MIPS Doom port, a metacircular evaluator, COBOL modernization, "
             "GPT-2 code-golf, CIFAR training). A compaction arm that cuts round-trips could bring some under "
             "budget, so the timeout count is itself a comparison metric.\n")
    L.append("| task | difficulty | category | steps before timeout | partial billed | budget (4×) |")
    L.append("|---|---|---|--:|--:|--:|")
    for r in sorted(timeouts, key=lambda x: x["task"]):
        bud = (meta.get(r["task"], {}) or {}).get("agent_timeout_sec")
        bud = f"{bud*4/60:.0f} min" if bud else "—"
        L.append(f"| {r['task']} | {r['_diff']} | {r['_cat']} | {r.get('steps')} | ${billed(r):.2f} | {bud} |")

    # by difficulty (all 89; timeouts count as failures)
    L.append("\n## By difficulty (all 89 tasks; timeouts = failures)\n")
    L.append("| difficulty | tasks | solved | rate | timed out | mean $/task |")
    L.append("|---|--:|--:|--:|--:|--:|")
    order = {"easy": 0, "medium": 1, "hard": 2, "unknown": 3}
    byd = defaultdict(list)
    for r in rows:
        byd[r["_diff"]].append(r)
    for d in sorted(byd, key=lambda x: order.get(x, 9)):
        rs = byd[d]
        nn, sv, cc, st, wl = agg(rs)
        to = sum(1 for r in rs if r["_timeout"])
        L.append(f"| {d} | {nn} | {sv} | {sv/max(nn,1):.0%} | {to} | ${cc/max(nn,1):.3f} |")

    # by category
    L.append("\n## By category (all 89 tasks)\n")
    L.append("| category | tasks | solved | rate | mean $/task | mean steps* |")
    L.append("|---|--:|--:|--:|--:|--:|")
    byc = defaultdict(list)
    for r in rows:
        byc[r["_cat"]].append(r)
    for c in sorted(byc, key=lambda x: (-agg(byc[x])[1] / max(len(byc[x]), 1), x)):
        rs = byc[c]
        nn, sv, cc, st, wl = agg(rs)
        rs_c = [r for r in rs if not r["_timeout"]]
        st_c = sum(r.get("steps", 0) or 0 for r in rs_c)
        L.append(f"| {c} | {nn} | {sv} | {sv/max(nn,1):.0%} | ${cc/max(nn,1):.3f} | "
                 f"{st_c/max(len(rs_c),1):.1f} |")

    # per-task (all 89)
    L.append("\n## Per-task (all 89)\n")
    L.append("| task | difficulty | category | outcome | steps | cache_read | cache_write | billed | wall |")
    L.append("|---|---|---|:--:|--:|--:|--:|--:|--:|")
    for r in rows:
        w = r.get("agent_wall_s")
        if r["_timeout"]:
            outcome = "⏱ timeout"
        elif (r.get("reward") or 0) >= 1:
            outcome = "✅ solved"
        else:
            outcome = "❌ failed"
        L.append(f"| {r['task']} | {r['_diff']} | {r['_cat']} | {outcome} | "
                 f"{r.get('steps')} | {r.get('cache_read',0):,} | {r.get('cache_write',0):,} | "
                 f"${billed(r):.3f} | {(str(round(w/60,1))+' min') if w else '—'} |")

    open(a.out, "w").write("\n".join(L) + "\n")
    print(f"wrote {a.out}: {n} attempted, {solved} solved ({solved/max(n,1):.1%}), "
          f"{len(scored)} completed, {len(timeouts)} timeout, ${cost:.2f}")


if __name__ == "__main__":
    main()
