# Benchmarks

**context-guru is the cheapest and highest-reward context-compaction layer on
SWE-bench Verified** — evaluated live, end-to-end, with the **claude-code** agent on
**`aws/claude-sonnet-5`**, against a no-compaction baseline, against
[**headroom**](https://pypi.org/project/headroom-ai/) (a request-stream proxy), and against
[**rtk**](https://github.com/rtk-ai/rtk) (Rust Token Killer, a shell-level Bash-output hook).

50 tasks, all of which scored under all **four** arms (zero infrastructure exceptions).

![headline](img/benchmark/headline.png)

| dimension | baseline | **context-guru** | headroom | rtk |
|---|--:|--:|--:|--:|
| reward (solved / 50) | 43 | **44** | 40 | 43 |
| **billed cost** (matched total) | $31.98 | **$27.77 (−13.2%)** | $30.30 (−5.3%) | $29.09 (−9.0%) |
| cache-read tokens | 102.8M | **84.5M** | 96.4M | 91.7M |
| cache-write tokens | 1.855M | 1.847M | 1.839M | 1.835M |
| mean steps / task | 36.1 | **31.1** | 35.1 | 33.2 |
| added latency / req | — | 117 ms | 63 ms | **0 ms** |
| tool LLM cost | $0 | $0.31 | $0 | $0 |

<div class="cg-chart-box"><canvas data-cg-chart="billed-cost"></canvas></div>
<div class="cg-chart-box"><canvas data-cg-chart="cache-read"></canvas></div>

**context-guru wins on cost, cache usage, steps, and reward.** It is cheaper despite
removing less *raw* content per request because it **freezes each compaction and re-applies
it byte-identically every turn**, so the reduction compounds across the session's
cache-reads while never mutating the cached prefix. The surprise is **rtk**: a simple
deterministic shell filter is the **2nd-cheapest** arm (−9.0%), **reward-neutral** (43 = 43),
at **zero request-path latency and $0 tool cost** — it **beats the headroom proxy on both
cost and reward**. Its ceiling is that it only compresses **Bash-tool** output (built-in
`Read`/`Grep`/`Glob` bypass its hook), which is why the whole-request proxy goes deeper.

## The results suite

- **[Full comparison](results/comparison.md)** — all four arms, cost decomposition,
  per-task plots, per-component breakdown, and the honest caveats.
- **[Component internals & real examples](results/components.md)** — how every
  context-guru component, headroom compressor, and rtk command filter works, when it
  triggers, and real before→after compactions from the run logs, side by side.
- Per-config detail (per-task tables + totals):
  [baseline](results/baseline.md) · [context-guru](results/context-guru.md) ·
  [headroom](results/headroom.md) · [rtk](results/rtk.md).
- **[Reproduce](results/REPRODUCE.md)** — install and run all four arms yourself.

Method note: cache-aware billed **input** cost = fresh $2/M · cache-read $0.20/M ·
cache-write $2.50/M (recomputed from each trial's own token tiers) + output $10/M;
**total** adds the tool's own compaction-model cost. Reward/step counts carry agent
run-to-run nondeterminism at n=1/task; the deterministic cache-write and per-component
token signals are the fully trustworthy ones.
