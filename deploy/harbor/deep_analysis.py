#!/usr/bin/env python3
"""Deep three-way analysis: baseline vs context-guru vs headroom on SWE-bench Verified.
Computes EVERY dimension per-task, aggregated, and per-component, and emits one JSON
(deep_analysis.json) that the plotting + doc scripts consume.

Dimensions: reward, steps, cache_read, cache_write, fresh_input, output, cache-aware
billed cost (+ decomposition), cache-hit rate, agent wall, tool's own LLM cost, added
latency, tokens removed (cumulative + unique), per-component / per-strategy savings.

Usage: deep_analysis.py --out /tmp/cg-runs/deep
"""
import argparse, json, subprocess
from pathlib import Path
from statistics import mean

IN, OUT, CREAD, CWRITE = 2e-6, 10e-6, 0.2e-6, 2.5e-6
CG = Path("/home/vpcuser/projects/context-engineering/context-guru")

SRC = {
    "baseline":     ("/tmp/cg-runs/final50/rows-off.json",            None),
    "context-guru": ("/tmp/cg-runs/final50-v6/rows-codesmart.json",   "/tmp/cg-runs/final50-v6/summary.json"),
    "headroom":     ("/tmp/hd-runs/swe50/rows-hd-cache.json",         None),
}


def billed(r):
    return (r.get("fresh_input", 0) * IN + r.get("cache_read", 0) * CREAD +
            r.get("cache_write", 0) * CWRITE + r.get("completion_tokens", 0) * OUT)


def load(p):
    return {r["task"]: r for r in json.load(open(p))}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="/tmp/cg-runs/deep")
    a = ap.parse_args()
    Path(a.out).mkdir(parents=True, exist_ok=True)

    rows = {k: load(v[0]) for k, v in SRC.items()}
    # tasks scored (no exception, reward not None) in ALL three
    common = sorted(t for t in rows["baseline"]
                    if all(t in rows[c] and not rows[c][t].get("exception")
                           and rows[c][t].get("reward") is not None for c in rows))
    configs = list(SRC)

    # per-task records
    per_task = []
    for t in common:
        rec = {"task": t.split("__")[-1]}
        for c in configs:
            r = rows[c][t]
            rec[c] = dict(reward=int(r.get("reward") or 0), steps=r.get("steps", 0),
                          cache_read=r.get("cache_read", 0), cache_write=r.get("cache_write", 0),
                          fresh=r.get("fresh_input", 0), out=r.get("completion_tokens", 0),
                          cost=round(billed(r), 4), wall=r.get("agent_wall_s") or 0)
        per_task.append(rec)

    def agg(c, key):
        return sum(rows[c][t][{"cache_read": "cache_read", "cache_write": "cache_write",
                               "fresh": "fresh_input", "out": "completion_tokens",
                               "steps": "steps"}[key]] or 0 for t in common)

    # per-config aggregate over matched tasks
    aggregate = {}
    for c in configs:
        solved = sum(1 for t in common if (rows[c][t].get("reward") or 0) >= 1)
        cr, cw, fr = agg(c, "cache_read"), agg(c, "cache_write"), agg(c, "fresh")
        cost = sum(billed(rows[c][t]) for t in common)
        cachecost = cr * CREAD + cw * CWRITE + fr * IN
        aggregate[c] = dict(
            n=len(common), solved=solved, rate=round(solved / len(common), 3),
            mean_steps=round(mean(rows[c][t]["steps"] for t in common), 1),
            cache_read=cr, cache_write=cw, fresh=fr, out=agg(c, "out"),
            cache_hit=round(100 * cr / max(cr + cw + fr, 1), 2),
            billed_cost=round(cost, 2), input_cache_cost=round(cachecost, 2),
            cache_read_cost=round(cr * CREAD, 2), cache_write_cost=round(cw * CWRITE, 2),
            mean_wall_s=round(mean((rows[c][t].get("agent_wall_s") or 0) for t in common), 1),
        )

    # tool's own LLM cost + added latency + content% + per-component (context-guru)
    tool = {c: dict(llm_cost=0.0, added_ms=0.0, content_pct=0.0, bounces=0, per_component={}) for c in configs}
    cs = json.load(open(SRC["context-guru"][1]))["configs"]
    cg = next((x for x in cs if x.get("per_component")), cs[0])
    tool["context-guru"] = dict(
        llm_cost=cg.get("cg_llm_cost", 0), added_ms=round(cg.get("cg_added_ms_avg") or 0, 1),
        content_pct=cg.get("proxy_savings_pct", 0), bounces=cg.get("proxy_bounces", 0),
        per_component={k: {"cum": v.get("saved_tokens"), "runs": v.get("runs"),
                            "ms": round(v.get("duration_ms") or 0, 1)}
                       for k, v in (cg.get("per_component") or {}).items()})
    # unique per-component (context-guru) from the dump
    try:
        subprocess.run(["python3", str(CG / "deploy/harbor/dump_unique.py"),
                        "/tmp/cg-runs/dump-swebench-codesmart.jsonl", "--json", f"{a.out}/cg_unique.json"], check=False)
        uq = json.load(open(f"{a.out}/cg_unique.json"))
        tool["context-guru"]["unique"] = uq
    except Exception:
        pass
    # headroom per-strategy + tool metrics from its RESULTS.json
    try:
        hr = json.load(open("/tmp/hd-runs/RESULTS.json"))["headroom_hd_cache"]
        tool["headroom"] = dict(llm_cost=hr.get("added_llm_cost", 0),
                                added_ms=hr.get("proxy_overhead_avg_ms", 0),
                                content_pct=hr.get("content_savings_pct", 0), bounces=0,
                                per_strategy=hr.get("per_strategy_saved", {}),
                                saved_tokens=hr.get("saved_tokens"))
    except Exception:
        pass

    out = dict(matched_tasks=len(common), configs=configs, aggregate=aggregate,
               tool=tool, per_task=per_task)
    Path(f"{a.out}/deep_analysis.json").write_text(json.dumps(out, indent=1))
    # console summary
    print(f"matched {len(common)} tasks (scored+no-exception in all 3)\n")
    hdr = f"{'metric':<22}" + "".join(f"{c:>16}" for c in configs)
    print(hdr); print("-" * len(hdr))
    for m, fn in [("solved", lambda x: f"{x['solved']}/{x['n']}"), ("rate", lambda x: f"{x['rate']:.0%}"),
                  ("mean_steps", lambda x: x["mean_steps"]), ("billed_cost $", lambda x: f"${x['billed_cost']}"),
                  ("cache_read (M)", lambda x: f"{x['cache_read']/1e6:.1f}"),
                  ("cache_write (M)", lambda x: f"{x['cache_write']/1e6:.2f}"),
                  ("cache_read_cost $", lambda x: f"${x['cache_read_cost']}"),
                  ("cache_write_cost $", lambda x: f"${x['cache_write_cost']}"),
                  ("cache_hit %", lambda x: x["cache_hit"]), ("mean_wall_s", lambda x: x["mean_wall_s"])]:
        print(f"{m:<22}" + "".join(f"{str(fn(aggregate[c])):>16}" for c in configs))
    print(f"\n{'tool llm $':<22}" + "".join(f"{str(tool[c]['llm_cost']):>16}" for c in configs))
    print(f"{'added ms/req':<22}" + "".join(f"{str(tool[c]['added_ms']):>16}" for c in configs))
    print(f"{'content %':<22}" + "".join(f"{str(tool[c]['content_pct']):>16}" for c in configs))
    print(f"\nwrote {a.out}/deep_analysis.json")


if __name__ == "__main__":
    main()
