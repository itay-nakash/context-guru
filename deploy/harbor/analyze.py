#!/usr/bin/env python3
"""Deep per-task + per-component analysis of a baseline-vs-config SWE run.

The point: separate the two things that were being conflated —
  (1) CONTENT tokens context-guru removed from the request body (the "savings"), and
  (2) the BILLED cost delta once Anthropic prompt-cache tiers are applied
      (cache-read $0.20/M, cache-write $2.50/M, fresh input $2/M, output $10/M).
On a heavily-cached agent (2) can go the opposite way from (1): removing old content
mutates the cached prefix, forcing cache-WRITES that outweigh the content saved.

Outputs: matched per-task table, aggregate, a cost-delta DECOMPOSITION (how much of
the delta is cache-write vs cache-read vs output/steps), and per-component content
savings + CG's own LLM cost. Writes <out>/analysis.json for plotting.

Usage: analyze.py <baseline_rows.json> <config_rows.json> <config_summary.json> [--out DIR] [--label NAME]
"""
import argparse, json, statistics as st
from pathlib import Path

# litellm claude-sonnet-5 rates ($/token)
IN, OUT, CREAD, CWRITE = 2e-6, 10e-6, 0.2e-6, 2.5e-6


def billed(r):
    """Recompute billed input-side cost from the trial's token tiers (cache-aware)."""
    return (r.get("fresh_input", 0) * IN + r.get("cache_read", 0) * CREAD +
            r.get("cache_write", 0) * CWRITE + r.get("completion_tokens", 0) * OUT)


def load(p):
    return {r["task"]: r for r in json.load(open(p))}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("baseline"); ap.add_argument("config"); ap.add_argument("summary")
    ap.add_argument("--out", default="/tmp/cg-runs/analysis")
    ap.add_argument("--label", default="config")
    ap.add_argument("--dump", default="", help="CONTEXT_GURU_DUMP jsonl for UNIQUE savings")
    ap.add_argument("--baseline-summary", default="", help="baseline summary for cache-hit/latency A/B")
    a = ap.parse_args()
    base, cfg = load(a.baseline), load(a.config)
    allc = json.load(open(a.summary))["configs"]
    # pick the config summary (the one with per-component data / not the 'off' baseline)
    summ = next((x for x in allc if x.get("per_component")), allc[-1])
    bsumm = next((x for x in allc if x.get("config") == "off"), None)
    common = [t for t in base if t in cfg and not base[t].get("exception")
              and not cfg[t].get("exception") and base[t]["reward"] is not None
              and cfg[t]["reward"] is not None]
    common.sort()
    rows = []
    for t in common:
        b, c = base[t], cfg[t]
        rows.append(dict(task=t.split("__")[-1],
            b_reward=b["reward"], c_reward=c["reward"],
            b_steps=b["steps"], c_steps=c["steps"],
            b_prompt=b["prompt_tokens"], c_prompt=c["prompt_tokens"],
            b_cread=b["cache_read"], c_cread=c["cache_read"],
            b_cwrite=b["cache_write"], c_cwrite=c["cache_write"],
            b_fresh=b["fresh_input"], c_fresh=c["fresh_input"],
            b_cost=round(billed(b), 4), c_cost=round(billed(c), 4)))

    def col(rows, k): return [r[k] for r in rows]
    def s(k): return sum(col(rows, k))
    bsolved = sum(1 for r in rows if r["b_reward"] >= 1)
    csolved = sum(1 for r in rows if r["c_reward"] >= 1)
    print(f"\n=== {a.label}: matched {len(rows)} tasks (scored in both, no exceptions) ===")
    print(f"REWARD: baseline {bsolved}/{len(rows)}   {a.label} {csolved}/{len(rows)}")
    print(f"  {a.label}-only solves: {[r['task'] for r in rows if r['c_reward']>r['b_reward']]}")
    print(f"  baseline-only solves : {[r['task'] for r in rows if r['c_reward']<r['b_reward']]}")

    # cost decomposition: delta split into the tiers that moved it
    d_cwrite = (s("c_cwrite") - s("b_cwrite")) * CWRITE
    d_cread = (s("c_cread") - s("b_cread")) * CREAD
    d_fresh = (s("c_fresh") - s("b_fresh")) * IN
    cg_llm = summ.get("cg_llm_cost", 0)
    bcost, ccost = s("b_cost"), s("c_cost")
    print(f"\nBILLED COST (matched total): baseline ${bcost:.2f}  {a.label} ${ccost:.2f} (agent)  + CG-LLM ${cg_llm:.2f}"
          f"  => {a.label} total ${ccost+cg_llm:.2f}  ({100*(ccost+cg_llm-bcost)/bcost:+.0f}%)")
    print("cost-delta decomposition (why it moved):")
    print(f"  cache_write: {s('c_cwrite')-s('b_cwrite'):+,} tok  = ${d_cwrite:+.2f}   <-- offload re-caches the mutated prefix")
    print(f"  cache_read : {s('c_cread')-s('b_cread'):+,} tok  = ${d_cread:+.2f}")
    print(f"  fresh_input: {s('c_fresh')-s('b_fresh'):+,} tok  = ${d_fresh:+.2f}")
    print(f"  CG own LLM : ${cg_llm:+.2f}")
    ch = lambda pre: 100*s(pre+"cread")/(s(pre+"cread")+s(pre+"cwrite")+s(pre+"fresh"))
    print(f"cache-hit: baseline {ch('b_'):.1f}%  {a.label} {ch('c_'):.1f}%   steps {st.mean(col(rows,'b_steps')):.1f}->{st.mean(col(rows,'c_steps')):.1f}")

    # content savings the proxy achieved (separate from billed cost!)
    print(f"\nCONTENT removed by CE (proxy /stats): {summ.get('proxy_savings_pct')}%  per-component:")
    for k, v in sorted((summ.get("per_component") or {}).items(), key=lambda x: -(x[1]["saved_tokens"] or 0)):
        if v["saved_tokens"]:
            print(f"  {k:<12} {v['saved_tokens']:>10,} tok  {v['runs']} runs  own-latency {round((v['duration_ms'] or 0)/1000,1)}s")
    print(f"  CG LLM: {summ.get('cg_llm_calls')} calls, ${summ.get('cg_llm_cost')}")

    # UNIQUE vs cumulative savings (dedup by content) — the honest per-component number
    uniq = {}
    if a.dump:
        import subprocess
        jp = f"{a.out}/dump_unique.json"
        Path(a.out).mkdir(parents=True, exist_ok=True)
        subprocess.run(["python3", str(Path(__file__).parent / "dump_unique.py"), a.dump, "--json", jp])
        try:
            uniq = json.load(open(jp))
        except Exception:
            uniq = {}

    # latency + cache-hit + restoration A/B (with vs without CG)
    print("\n=== LATENCY / CACHE / RESTORATION (with vs without CG) ===")
    print(f"  CG-added latency/req: {round(summ.get('cg_added_ms_avg') or 0,1)} ms   "
          f"(baseline {round((bsumm or {}).get('cg_added_ms_avg') or 0,1)} ms)")
    print(f"  agent wall/task: baseline {round((bsumm or {}).get('mean_agent_wall_s') or 0,1)}s  "
          f"{a.label} {round(summ.get('mean_agent_wall_s') or 0,1)}s")
    print(f"  cache_hit_rate: baseline {(bsumm or {}).get('cache_hit_rate')}  {a.label} {summ.get('cache_hit_rate')}")
    print(f"  expand bounces (restoration fired): {summ.get('proxy_bounces')}")

    print("\n=== PER-TASK (sorted by cost delta) ===")
    rows_sorted = sorted(rows, key=lambda r: (r["c_cost"] - r["b_cost"]))
    print(f"{'task':<22}{'rew b/c':>8}{'steps b/c':>11}{'cost_b':>8}{'cost_c':>8}{'Δcost':>8}{'Δcwrite':>10}")
    for r in rows_sorted:
        rew = f"{int(r['b_reward'])}/{int(r['c_reward'])}"
        steps = f"{r['b_steps']}/{r['c_steps']}"
        dcost = r["c_cost"] - r["b_cost"]
        dcw = r["c_cwrite"] - r["b_cwrite"]
        print(f"{r['task']:<22}{rew:>8}{steps:>11}{r['b_cost']:>8.3f}{r['c_cost']:>8.3f}{dcost:>+8.3f}{dcw:>+10,}")
    Path(a.out).mkdir(parents=True, exist_ok=True)
    Path(f"{a.out}/analysis.json").write_text(json.dumps(dict(
        label=a.label, matched=len(rows), reward=[bsolved, csolved],
        cost=[round(bcost, 3), round(ccost, 3), round(cg_llm, 3)],
        decomposition=dict(cache_write=round(d_cwrite, 3), cache_read=round(d_cread, 3),
                           fresh=round(d_fresh, 3), cg_llm=round(cg_llm, 3)),
        per_component=summ.get("per_component"),
        unique_savings=uniq.get("components", {}) if uniq else {},
        latency=dict(cg_added_ms=summ.get("cg_added_ms_avg"),
                     agent_wall_base=(bsumm or {}).get("mean_agent_wall_s"),
                     agent_wall_cfg=summ.get("mean_agent_wall_s")),
        cache_hit=[(bsumm or {}).get("cache_hit_rate"), summ.get("cache_hit_rate")],
        bounces=summ.get("proxy_bounces"),
        proxy_savings_pct=summ.get("proxy_savings_pct"),
        rows=rows), indent=1))
    print(f"\nwrote {a.out}/analysis.json")


if __name__ == "__main__":
    main()
