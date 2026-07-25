# Benchmark: context-guru vs headroom vs baseline

**SWE-bench Verified · 50 tasks · `claude-code` agent on `aws/claude-sonnet-5`**, run live
through each proxy. All **50 tasks** scored (no infrastructure exception) under all three
arms, so every number below is apples-to-apples on the same tasks. Reproduce: [REPRODUCE.md](REPRODUCE.md). Per-config detail:
[baseline.md](baseline.md) · [context-guru.md](context-guru.md) · [headroom.md](headroom.md).
Component internals & real examples: [components.md](components.md).

Cache-aware billed **input** cost = fresh $2/M · cache-read $0.20/M · cache-write $2.50/M,
recomputed from each trial's own token tiers; output billed at $10/M. **Total** adds the
tool's own compaction-LLM cost.

## Headline

**context-guru is the cheapest *and* highest-reward compaction layer on this benchmark** —
it beats both the no-compaction baseline and headroom on billed cost, and solves the most
tasks of the three arms.

![headline](../img/benchmark/headline.png)

| dimension | baseline `off` | **context-guru** | headroom | winner |
|---|--:|--:|--:|:--|
| reward (solved / 50) | 43 (86%) | **44 (88%)** | 40 (80%) | **context-guru** |
| **total billed cost** | $31.98 | **$27.77 (−13.2%)** | $30.30 (−5.3%) | **context-guru** |
| cache-read tokens | 102.8M | **84.5M (−17.8%)** | 96.4M (−6.3%) | **context-guru** |
| cache-write tokens | 1.855M | 1.847M | **1.839M** | ≈ tie (all within 0.9%) |
| cache-read $ | $20.57 | **$16.90** | $19.28 | **context-guru** |
| cache-write $ | $4.64 | $4.62 | **$4.60** | ≈ tie |
| cache-hit rate | 98.14% | 97.73% | 98.01% | ≈ tie |
| mean steps / task | 36.1 | **31.1** | 35.1 | **context-guru** |
| mean agent wall / task | 380 s | **352 s** | 364 s | **context-guru** |
| compaction added latency / req | — | 117 ms | **63 ms** | **headroom** |
| tool's own LLM cost | $0 | $0.31 | **$0** | **headroom** |
| raw content removed (per req) | 0 | 1.09% | **2.64%** | **headroom** |
| exceptions (of 50) | 0 | 0 | 0 | tie |

### Verdict

- **context-guru wins the dollar-and-reward metrics** — lowest billed cost ($27.77,
  **−13.2%** vs baseline; headroom −5.3%), lowest cache-read, fewest steps, *and* the most
  tasks solved (44 vs baseline 43 vs headroom 40). All three arms ran all 50 tasks with
  **zero** infrastructure exceptions.
- **headroom wins the overhead metrics** — half the added latency (63 vs 117 ms/req),
  zero tool cost, and more *raw* content removed (2.64% vs 1.09%) — all because it is
  **fully deterministic** (no model on the hot path). Cache-**write** is a three-way wash
  (1.855M / 1.847M / 1.839M — within 0.9%): none of the arms busts the cache.
- **Why context-guru is cheaper despite removing less content per request:** it
  **freezes each compaction and replays it byte-identically on every later turn**, so a
  reduction compounds across the whole session's cache-reads (−17.8% cache-read vs
  headroom's −6.3%). Removing a little from *every* turn's re-sent history beats removing
  more from one turn. It also drives the agent to **fewer steps** (31.1 vs 35.1), which
  compounds the saving further (a partial step-count effect — see the honest caveat).

## Cost decomposition

Where every dollar goes (matched total): cache-read is the dominant term on a
~98%-cached agent; context-guru shrinks it the most, at the price of a small $0.31 haiku
bill; headroom adds no tool cost but shrinks cache-read less.

![cost decomposition](../img/benchmark/cost_decomposition.png)

## Per-task

Per-task billed cost (baseline ◆ vs headroom ● vs context-guru ●) and the per-task deltas
vs baseline — context-guru is at/below baseline on nearly every task:

![per-task cost](../img/benchmark/per_task_cost.png)
![per-task cost delta](../img/benchmark/per_task_dcost.png)
![per-task step delta](../img/benchmark/per_task_dsteps.png)

## Per-component / per-compressor

context-guru's savings come from `extract_llm` (LLM skeletonization of large file
reads/logs) + the deterministic `extract` (ANSI/CR + noise) + `cmdfilter`/`dedup`;
headroom's from its deterministic `text` and `code_aware` (AST) compressors. Cumulative
vs unique tokens (context-guru re-applies the same compaction every turn, so cumulative ≫
unique):

![components](../img/benchmark/components.png)

Full component internals, trigger conditions, and real before→after examples for both
tools are in **[components.md](components.md)**.
