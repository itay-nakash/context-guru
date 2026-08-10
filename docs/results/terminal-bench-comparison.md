# Benchmark: Terminal-Bench 2.0 — baseline vs context-guru vs headroom vs rtk

**Terminal-Bench 2.0 · 89 tasks · `claude-code` agent on `aws/claude-sonnet-5`**, run live
through the harness. This is the second benchmark of the study; the [SWE-bench Verified
four-way](comparison.md) is the first. The four arms are the same:

- **baseline** — no compaction (context-guru `off` passthrough; identical routing).
- **context-guru** (`codesmart`) — cache-aware request-stream proxy, hybrid deterministic + a
  cheap **haiku** LLM (`extract_llm`).
- **headroom** (`hd-cache`) — deterministic request-stream proxy (`--no-ccr`, streaming-safe).
- **rtk** — shell-level `PreToolUse` hook, compresses Bash output in-container.

**Methodology.** Cache-aware billed **input** cost (fresh $2/M · cache-read $0.20/M ·
cache-write $2.50/M) + output $10/M, recomputed from each trial's own token tiers; **total**
adds the tool's own compaction-LLM cost (context-guru's haiku calls). All 89 tasks carry a
scored outcome; timeouts (agent exceeded its wall-clock budget) count as reward-0 failures.
See [REPRODUCE.md](REPRODUCE.md) and the [baseline page](terminal-bench-baseline.md).

!!! danger "Correction pending — the cost figures below overstate the regression"
    Six baseline trials are **degenerate**: the baseline aborted in 2–6 steps (16–800 s)
    where the compaction arms ran 50–160 steps, so the per-task cost delta on those six is
    an artifact of the baseline not doing the work, not of compaction. `extract-moves-from-video`
    alone accounts for **$24.10 of headroom's $24.65 "regression"**.

    Recomputed over the **83 clean tasks**: context-guru **−9.7%**, headroom **−16.0%**,
    rtk **+6.4%**. Both proxies *save* on Terminal-Bench; only rtk regresses. The tables
    below are the raw 89-task figures and will be regenerated once those six baselines are
    re-run at low concurrency. See [improvement-plan.md](improvement-plan.md) §1 for the
    full recompute.

!!! warning "Two caveats that shape how to read this"
    **1. Single trial per task (`n-attempts=1`).** Unlike the SWE study (2 trials), each task
    ran once per arm. **Solve-rate deltas carry real run-to-run noise** — the per-task flip
    churn below (e.g. context-guru +10/−8) shows the net reward differences are mostly within
    that noise. The **cost, cache, and step aggregates are robust** (sums over 89 tasks), and
    are where the real signal is.
    **2. Budget policy.** Framework arms ran at a flat **4× wall-clock budget**; the baseline
    used 1.5× for most tasks and 4× for the long-horizon set. This is fair: every task a
    framework "gained" over baseline was one the baseline *completed and got wrong* at 1.5×
    (more time would not have changed it), not a baseline timeout.

## Headline

**On long-horizon terminal tasks, compaction is far harder to make pay off than on SWE-bench:
no arm beats baseline on cost.** context-guru stays roughly cost-neutral while nudging reward
up; headroom buys the most reward at a real cost premium; **rtk backfires** — the mirror image
of its SWE-bench result.

| dimension | baseline | **context-guru** | headroom | **rtk** | best |
|---|--:|--:|--:|--:|:--|
| solved / 89 | 56 (62.9%) | 58 (65.2%) | **64 (71.9%)** | 55 (61.8%) | headroom |
| **total billed cost** | **$100.81** | $102.55 (+1.7%) | $114.75 (+13.8%) | $118.83 (+17.9%) | **baseline** |
| cache-read tokens | 216.0M | 204.6M (−5.3%) | **198.0M (−8.3%)** | 254.0M (+17.6%) | headroom |
| cache-write tokens | **4.01M** | 6.53M | 12.37M | 7.05M | **baseline** |
| cache-hit rate | **98.2%** | 96.9% | 94.1% | 97.3% | **baseline** |
| mean steps (completed) | **31.5** | 34.7 | 32.0 | 40.0 | **baseline** |
| timeouts (of 89) | **7** | 11 | 11 | **7** | baseline / rtk |
| tool's own LLM cost | $0 | $3.26 | **$0** | **$0** | headroom / rtk |
| content removed / req | 0 | 10.7% (whole req) | 1.27% (whole req) | 94.4% *of bash only* | — (diff. denominators) |

### Verdict

- **baseline is the cheapest and most cache-friendly arm.** With a 98.2% cache-hit and the
  lowest cache-write, it is the arm to beat — and on this benchmark none of the three
  compaction layers beats it on cost.
- **context-guru is the only cost-neutral compaction arm (+1.7%)** and nudges reward up (+2,
  within noise). It genuinely cuts the cache-aware **input** bill (−1.5%, cache-read −5.3%),
  but on Terminal-Bench's huge contexts its `extract_llm` haiku pass costs **$3.26** and adds
  **450 ms/req**, which erases the input saving. Nothing like its **−13%** SWE-bench win.
- **headroom solves the most (+8, and +5 on `hard` tasks)** — the clearest real reward signal —
  but at **+13.8%**, because compressing the live zone mutates cached content and triggers a
  **3× cache-write blow-up** (12.4M vs 4.0M tokens).
- **rtk backfires (+17.9%, −1 solved).** On SWE-bench it was the −9% efficiency surprise; here
  its 94% Bash-output compression **discards information the agent needs on open-ended tasks**,
  so the agent takes **+27% more steps (40 vs 31.5)** → more round-trips → the **highest
  cache-read of any arm (+17.6%)** and the highest cost.

## Why cost goes up here but down on SWE-bench

The decomposition points at one mechanism: **cache-write**.

| arm | fresh $ | cache-read $ | **cache-write $** | output $ | + tool-LLM | total |
|---|--:|--:|--:|--:|--:|--:|
| baseline | 0.12 | 43.19 | **10.03** | 47.47 | — | $100.81 |
| context-guru | 0.21 | 40.93 | **16.31** | 41.83 | 3.26 | $102.55 |
| headroom | 0.20 | 39.60 | **30.92** | 44.03 | — | $114.75 |
| rtk | 0.11 | 50.80 | **17.62** | 50.30 | — | $118.83 |

- On **SWE-bench**, contexts are smaller and the proxies' *freeze-and-replay* keeps the cached
  prefix byte-stable, so cache-write stayed within 1% of baseline and the cache-read savings
  won → context-guru −13%.
- On **Terminal-Bench**, contexts are ~1.7M tokens and outputs are large and varied. Any layer
  that mutates cached content pays for it in **cache-write**: headroom's live-zone rewriting
  triples it; context-guru's freeze-replay is less clean on huge outputs (+63%); rtk's extra
  steps inflate every tier. The cache-write premium (plus context-guru's LLM cost) swamps the
  cache-read saving. Cache-write, a rounding error on SWE-bench, is the deciding term here.

## Per-component / per-compressor

**context-guru** — unique tokens saved (whole-request savings **10.7%**):

| component | acts | unique tokens saved | note |
|---|--:|--:|---|
| `extract_llm` | 684 | 197,548 | haiku skeletonization of big reads/logs — 271 calls, $3.26, ~1,592 s cumulative latency (the cost + latency source) |
| `extract` | 1,446 | 59,728 | deterministic ANSI/CR + noise, ~0 latency |
| `format` | 51 | 6,929 | JSON repack |
| `dedup` | 99 | 3,828 | duplicate tool outputs |
| `cmdfilter` | 34 | 886 | DSL log/test trims |
| `failed_run` / `cacheinject` | 0 | 0 | cache-aware auto-off / systemic |

**headroom** — tokens saved by strategy (live-zone content; the headline `saved`=971k is mostly
tool-schema compaction, content savings **1.27%**):

| strategy | events | tokens saved |
|---|--:|--:|
| `text` (Kompress) | 435 | 34,994 |
| `code_aware` (AST) | 96 | 17,551 |
| `html` | 26 | 6,015 |
| `search` | 15 | 4,917 |
| `smart_crusher` (JSON) | 60 | 1,922 |
| `tabular` / `log` | 30 | 877 |

**rtk** — in-container Bash-output compression (its own `bytes/4` estimate, bash-output
denominator): **1,185 commands, 49.4M → 2.78M tokens (94.4% of bash output)**. The compression
is real and huge — but it is exactly what drives the agent to re-issue commands and take more
steps on open-ended tasks, so the net effect on billed cost is **negative**.

## Reward: where the arms win and lose

Solve rate by difficulty (solved / n):

| arm | easy (4) | medium (55) | hard (30) |
|---|--:|--:|--:|
| baseline | 3 | 39 | 14 |
| context-guru | 4 | 41 | 13 |
| headroom | 3 | **42** | **19** |
| rtk | 3 | 38 | 14 |

Net solve vs baseline (gains / losses — note the churn, i.e. single-trial noise):

- **context-guru** +10 / −8 → **net +2** (within noise)
- **headroom** +13 / −5 → **net +8** (gains cluster on `hard`; the clearest real signal)
- **rtk** +8 / −9 → **net −1** (reward-neutral; the cost regression is the real story)

Both compaction proxies solved `path-tracing-reverse` — a task the **baseline timed out on even
at 4×** — showing that a smaller context can let a long task finish in time. That is the
upside; on Terminal-Bench it is not (yet) enough to offset the cache-write and LLM costs.

## Bottom line

On Terminal-Bench 2.0 the ranking inverts the SWE-bench story. **baseline wins on cost and
cache-friendliness**; **headroom wins on reward** (+8 solved) but pays +14%; **context-guru** is
the balanced middle (cost-neutral, small reward gain); **rtk regresses** on this long-horizon,
open-ended workload. The transferable lesson: a compaction layer's value is workload-dependent —
what compounds into savings on localized, smaller-context SWE-bench tasks becomes a cache-write
(and, for LLM/hook layers, a compute/step) tax on Terminal-Bench's long-horizon contexts.
