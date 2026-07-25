#!/usr/bin/env python3
"""Generate a per-config results doc (per-task table + totals) from a rows.json.
Usage: gen_result_docs.py <rows.json> <label> <out.md> [--summary summary.json]
"""
import argparse, json
IN, OUT, CREAD, CWRITE = 2e-6, 10e-6, 0.2e-6, 2.5e-6


def billed(r):
    return (r.get("fresh_input", 0) * IN + r.get("cache_read", 0) * CREAD +
            r.get("cache_write", 0) * CWRITE + r.get("completion_tokens", 0) * OUT)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("rows"); ap.add_argument("label"); ap.add_argument("out")
    ap.add_argument("--summary", default="")
    a = ap.parse_args()
    rows = json.load(open(a.rows))
    rows = [r for r in rows if not r.get("exception")]
    rows.sort(key=lambda r: r["task"])
    L = []
    L.append(f"# Full results — {a.label} (SWE-bench Verified, 50 tasks)\n")
    L.append("Live through the harness, `claude-code` agent on `aws/claude-sonnet-5`. "
             "Cache-aware billed input cost (fresh $2/M · cache-read $0.20/M · cache-write "
             "$2.50/M) + output $10/M, recomputed from each trial's token tiers. See "
             "[REPRODUCE.md](REPRODUCE.md).\n")
    solved = sum(1 for r in rows if (r.get("reward") or 0) >= 1)
    tcost = sum(billed(r) for r in rows)
    tsteps = sum(r.get("steps", 0) for r in rows)
    tcr = sum(r.get("cache_read", 0) for r in rows)
    tcw = sum(r.get("cache_write", 0) for r in rows)
    tfresh = sum(r.get("fresh_input", 0) for r in rows)
    twall = sum(r.get("agent_wall_s", 0) or 0 for r in rows)
    hit = 100 * tcr / max(tcr + tcw + tfresh, 1)
    L.append("## Totals\n")
    L.append(f"| tasks scored | solved | rate | total billed cost | mean steps | cache-hit | agent wall (sum) |")
    L.append(f"|---|---|---|---|---|---|---|")
    L.append(f"| {len(rows)} | {solved} | {solved/max(len(rows),1):.0%} | ${tcost:.2f} | "
             f"{tsteps/max(len(rows),1):.1f} | {hit:.1f}% | {twall/60:.0f} min |")
    if a.summary:
        try:
            s = json.load(open(a.summary))["configs"]
            s = next((x for x in s if x.get("per_component")), None)
            if s:
                L.append(f"\nContext-guru proxy savings: **{s.get('proxy_savings_pct')}%** content; "
                         f"own LLM cost ${s.get('cg_llm_cost')}; added latency/req "
                         f"{round(s.get('cg_added_ms_avg') or 0,1)} ms; expand bounces {s.get('proxy_bounces')}.")
                pc = s.get("per_component") or {}
                L.append("\nPer-component tokens removed (cumulative): " +
                         ", ".join(f"`{k}` {v['saved_tokens']:,}" for k, v in
                                   sorted(pc.items(), key=lambda x: -(x[1]['saved_tokens'] or 0)) if v['saved_tokens']))
        except Exception:
            pass
    L.append("\n## Per-task\n")
    L.append("| task | reward | steps | cache_read | cache_write | billed cost |")
    L.append("|---|---|---|---|---|---|")
    for r in rows:
        L.append(f"| {r['task']} | {int(r.get('reward') or 0)} | {r.get('steps')} | "
                 f"{r.get('cache_read',0):,} | {r.get('cache_write',0):,} | ${billed(r):.3f} |")
    open(a.out, "w").write("\n".join(L) + "\n")
    print(f"wrote {a.out}: {len(rows)} tasks, {solved} solved, ${tcost:.2f}")


if __name__ == "__main__":
    main()
