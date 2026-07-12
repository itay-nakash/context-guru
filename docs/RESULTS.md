# Benchmark results

Per-component evaluation of context-guru on **SWE-bench** with the **Claude Code**
agent and model **`claude-sonnet-4-6`**, run through the context-guru gateway in
[eval-containers](setup.md). Baseline = the same gateway with an empty pipeline
(passthrough); each `cg-*` row = that one component alone (default config, **no
task-specific tuning**); `cg-balanced` / `cg-agent` = presets.

## Headline

- **Zero task-reward regression, every component.** On the tasks the baseline
  resolved, every configuration resolved them too (6/6). Even the lossy
  offloaders (`mask`, `extract`, `collapse`) did not break a task — the offloaded
  old tool outputs were not re-needed.
- **`mask` is the biggest lever: ~27% content-token reduction, no reward loss.**
  Claude Code's context is dominated by file-read tool outputs re-sent every turn;
  dropping the old ones (keep-recent window) attacks that directly.

## Savings on the 6 baseline-resolved tasks (honest reward-parity basis)

| config | mean content-token savings | reward kept | mechanism |
|---|---:|:---:|---|
| `cg-mask` | **26.8%** | 6/6 | offload tool outputs older than the keep-recent window |
| `cg-balanced` | 12.2% | 6/6 | format + dedup + failed_run + cmdfilter + cacheinject |
| `cg-extract` | 11.1% | 6/6 | project each large tool output to query-relevant lines |
| `cg-failed_run` | 5.8% | 6/6 | collapse superseded test/build runs |
| `cg-collapse` | ~0% | 6/6 | fires only on >2000-token outputs (rare here) |
| `cg-dedup` | ~0.2% | 6/6 | exact within-request duplicates (rare here) |
| `cg-format`, `cg-cmdfilter`, `cg-cacheinject`, `cg-skeleton`, `cg-smartcrush`, `cg-phi_evict` | 0% | 6/6 | inert on this traffic — see below |

The **`agent` preset** (`format, dedup, failed_run, mask, extract, cacheinject`)
combines the winners; use it for long agentic sessions.

## Why the 0% components are inert here (not bugs)

A debug capture of real Claude Code traffic showed the tool outputs are
**line-numbered file reads** (source code) and git — repeated across turns as the
transcript grows. So:

| component | needs | this traffic |
|---|---|---|
| `format` | pretty-printed JSON | file reads, not JSON |
| `cmdfilter` | shell-command banners (pytest/npm/make) | file reads, not command output |
| `skeleton` | fenced ` ```lang ` code blocks | Read output is line-numbered, unfenced |
| `smartcrush` | JSON arrays | none |
| `phi_evict` | transcript over `budget_tokens` (120k) | under budget |
| `cacheinject` | (saves provider **KV-cache** tokens, not content tokens) | works, but invisible to content-token metric |

## Methodology & caveats

- Harness: [`deploy/eval-containers/sweep.py`](../deploy/eval-containers/sweep.py)
  (resumable) → `aggregate.py`. One cell = one `(task, config)` run through the
  compose stack; reward from `output/task/result.json`, savings from the gateway
  `/stats` (within-run `tokens_before → tokens_after`).
- **Within-run savings %** is the honest metric — before/after are the same
  request stream, immune to run-to-run agent nondeterminism. Cumulative token
  totals across a growing transcript overstate per-task savings and are not used
  for the headline.
- Reward parity is measured only on tasks the **baseline resolved** (6 of 10);
  the others neither baseline nor context-guru resolved (SWE-bench resolve rates
  vary), so they can't show "did reduction hurt correctness".
- Grading requires **offline-gradable** tasks; network-dependent tasks (e.g.
  `psf/requests`) grade as 0 under the runner's isolated network and are excluded.
- Host was arm64 (SWE-bench images run under emulation); results are functional,
  not latency benchmarks.

## Tasks

`sympy-13647/16766/20438`, `sphinx-7910/9320`, `scikit-learn-12973/25931`,
`xarray-4629`, `django-11820/14089`.
