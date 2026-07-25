# Benchmarks

**context-guru is the cheapest and highest-reward context-compaction layer on
SWE-bench Verified** — evaluated live, end-to-end, with the **claude-code** agent on
**`aws/claude-sonnet-5`**, against a no-compaction baseline and against
[**headroom**](https://pypi.org/project/headroom-ai/) (`headroom-ai` v0.32.1).

50 tasks, all of which scored under all three arms (zero infrastructure exceptions).

![headline](img/benchmark/headline.png)

| dimension | baseline | **context-guru** | headroom |
|---|--:|--:|--:|
| reward (solved / 50) | 43 | **44** | 40 |
| **billed cost** (matched total) | $31.98 | **$27.77 (−13.2%)** | $30.30 (−5.3%) |
| cache-read tokens | 102.8M | **84.5M** | 96.4M |
| cache-write tokens | 1.855M | 1.847M | 1.839M |
| mean steps / task | 36.1 | **31.1** | 35.1 |
| added latency / req | — | 117 ms | 63 ms |
| tool LLM cost | $0 | $0.31 | $0 |

<div class="cg-chart-box"><canvas data-cg-chart="billed-cost"></canvas></div>
<div class="cg-chart-box"><canvas data-cg-chart="cache-read"></canvas></div>

**context-guru wins on cost, cache usage, steps, and reward-vs-headroom.** headroom keeps
an edge on added latency and tool cost (it is fully deterministic). The reason
context-guru is cheaper despite removing less *raw* content per request: it **freezes
each compaction and re-applies it byte-identically every turn**, so the reduction
compounds across the whole session's cache-reads while never mutating the cached prefix.

## The results suite

- **[Full comparison](results/comparison.md)** — all dimensions, cost decomposition,
  per-task plots, per-component breakdown, and the honest caveats.
- **[Component internals & real examples](results/components.md)** — how every
  context-guru component and headroom compressor works, when it triggers, and real
  before→after compactions from the run logs, side by side.
- Per-config detail (per-task tables + totals):
  [baseline](results/baseline.md) · [context-guru](results/context-guru.md) ·
  [headroom](results/headroom.md).
- **[Reproduce](results/REPRODUCE.md)** — install and run all three arms yourself.

Method note: cache-aware billed **input** cost = fresh $2/M · cache-read $0.20/M ·
cache-write $2.50/M (recomputed from each trial's own token tiers) + output $10/M;
**total** adds the tool's own compaction-model cost. Reward/step counts carry agent
run-to-run nondeterminism at n=1/task; the deterministic cache-write and per-component
token signals are the fully trustworthy ones.
