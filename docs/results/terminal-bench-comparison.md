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

!!! danger "Read the 89-task cost figures with this correction"
    **Six baseline trials are degenerate.** On these tasks the baseline aborted almost
    immediately while the compaction arms did the real work, so the per-task cost delta
    measures *the baseline not doing the job*, not the cost of compaction:

    | task | baseline | context-guru |
    |---|--:|--:|
    | `mteb-leaderboard` | $0.08 (4 steps) | $6.05 (147 steps) |
    | `polyglot-rust-c` | $0.08 (3 steps) | $2.81 (50 steps) |
    | `extract-moves-from-video` | $0.02 (2 steps) | $2.69 (112 steps) |
    | `regex-chess` · `write-compressor` · `code-from-image` | 4–6 steps each | comparable |

    Those three rows alone are **$11.5 of apparent regression**. Recomputed over the
    **83 clean tasks** (same cache-aware model, context-guru's own haiku cost included):

    | | baseline | context-guru | delta |
    |---|--:|--:|--:|
    | billed model cost | $100.17 | $87.47 | **−12.7%** |
    | + context-guru's LLM cost | — | $90.34 | **−9.8%** |
    | solved | 54 | **56** | +2 |
    | total steps | 3,160 | **2,899** | **−8.3%** |

    So on the clean set context-guru is **cheaper, solves more, and takes fewer steps** —
    the same direction as its SWE-bench result, not the reversal the 89-task total implies.
    headroom recomputes to ≈**−16%** and rtk remains a genuine regression (≈**+6%**).

    The tables below are the raw 89-task figures, kept as measured. They will be
    regenerated once the six baselines are re-run at low concurrency; that re-run is
    tracked as follow-up work. See [improvement-plan.md](improvement-plan.md) §1.

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

**On long-horizon terminal tasks the raw 89-task totals show no arm beating baseline on cost —
but that is dominated by six degenerate baseline trials** (see the correction above). On the
83 clean tasks **both proxies save**: context-guru −9.8% (solving +2, with 8.3% fewer steps)
and headroom ≈−16%. **rtk is the one genuine cost regression**, the mirror image of its
SWE-bench result. Read the tables below as measured, and the clean-set figures as the
conclusion.

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

- **baseline has the best cache behaviour** — a 98.2% cache-hit and the lowest cache-write —
  and on the *raw* 89-task total it is the cheapest arm. On the 83 clean tasks it is not.
- **context-guru saves on the clean set: −9.8% including its own LLM cost** (−12.7% on model
  cost alone), while solving **+2** and taking **8.3% fewer steps** — the same shape as its
  −13% SWE-bench win. Its `extract_llm` haiku pass still costs **$3.26** and adds **450 ms/req**,
  which is a real drag on the margin (and is why that component is being reworked), but it does
  not erase the saving. The **+1.7%** in the table below is the six degenerate baselines.
- **headroom solves the most (+8, and +5 on `hard` tasks)** — the clearest real reward signal —
  and recomputes to ≈**−16%** on the clean set. Its **3× cache-write blow-up** (12.4M vs 4.0M
  tokens) from rewriting the live zone is nonetheless real, and is the mechanism to avoid.
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

Terminal-Bench 2.0 does **not** invert the SWE-bench story once the six degenerate baselines are
removed. On the 83 clean tasks **context-guru saves −9.8%** while solving +2 with 8.3% fewer
steps, and **headroom saves ≈−16%** while solving +8 (its gains clustered on `hard`). **rtk is
the one real regression.**

What *is* genuinely different here is the **mechanism**, and it survives the correction:
**cache-write, a rounding error on SWE-bench, is the deciding term on Terminal-Bench's ~1.7M-token
contexts.** Any layer that mutates already-cached content pays 11.5× for it — headroom's live-zone
rewriting triples cache-write, and rtk's information loss costs +27% more steps. The transferable
lesson is therefore not "compaction fails on long horizons" but **"on long horizons, cache-write
avoidance and step count dominate token removal"** — which is exactly what the
[improvement plan](improvement-plan.md) is built around.

Two methodological lessons worth carrying forward, both learned the hard way here:
**a trial where the baseline aborts is not a measurement**, and it must be excluded rather than
averaged; and **per-arm totals hide this**, so a per-task paired comparison is the only honest
default.
