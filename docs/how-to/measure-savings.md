# Measure savings

The honest answer to "what did context-guru save me?" is **several numbers, each with its
denominator named**. This guide walks the fastest path to those numbers, then explains
which one to quote and why the obvious one is usually the wrong one.

Savings are measured on **message content text** — what the model reads — not the JSON
envelope, so a control directive like `cacheinject` never looks "worse".

## The fast path: turn on the dashboard

```sh
context-guru-proxy --preset codesmart --dashboard
# point your agent at http://localhost:4000/anthropic (or /openai), then open:
# http://localhost:4000/dashboard/
```

Within a few turns the [Overview](../dashboard.md) shows tokens before/after, gross vs
unique vs net-of-restores savings, baseline vs actual dollars, the cumulative-cost chart
with the saved area shaded, and the honest savings waterfall.

![The dashboard's Overview](../img/dashboard/01-overview.jpg)

Everything below is about reading those numbers correctly.

## Which savings number to quote

context-guru reports **four** savings ratios because there is no single honest one. Pick
by the question you are actually asking:

| If you want to know… | Use | Why |
|---|---|---|
| Is compaction working when there is something to compact? | **of what we tried to compact** | Divides by the tokens compaction was *allowed* to touch — the uncached tail on a caching backend. Excludes the frozen prefix we deliberately never touched. |
| What is the economic effect? | **of new provider-billed input** | Divides by fresh input + cache writes + what we removed. Does not recount transcript history the provider served from cache and never re-billed. |
| What is the most conservative claim I can make? | **unique, of the whole request** | Each distinct compaction counted once, over every content token in every request. |
| Why does my long session read ~0%? | **of the whole request (diluted)** | This one. A 200-turn session re-sends its history every turn, so the denominator grows quadratically. It is not a bug; it is what a whole-request ratio *means*. |

!!! warning "The trap"
    On this workload a whole-request ratio is dominated by re-sent history, so it trends
    toward zero however well compaction performs. If a tool quotes one savings percentage
    and does not say what it divided by, you cannot tell which of these you are looking at.

## Gross vs unique vs adjusted

Three savings figures, all true, all different:

| Figure | Meaning |
|---|---|
| **gross** | Every token removed, re-counted each turn the agent re-sends the same transcript. |
| **unique** | Each distinct compaction counted once (deduped by the content key the offloader stashed). |
| **net of restores** | Unique, minus content the model asked back for via `context_guru_expand`. |

`overcount_ratio = gross ÷ unique` is how inflated the gross figure is. **7×** means the
gross number counted the same compaction seven times. Quote unique or adjusted; use gross
only when you say it is cumulative.

## Read the cost, not just the tokens

The counterintuitive result from [the SWE-bench study](../results/comparison.md): on a
prompt-caching backend the request is ~99.95% cached and a **cache write bills ~11.5× a
cache read**, so removing unique tokens moves 0.02–0.13% of the billed total while cost
tracks agent **steps** at r = 0.95.

So the dashboard prices every request at write time and shows:

- **baseline cost** — what the same requests would have cost with nothing removed (the
  tokens we removed priced at the cache-write rate they would have entered as);
- **actual cost** — as billed;
- **context-guru's own LLM spend** — what `extract_llm` and friends cost us;
- **net dollars saved** — baseline − actual − our own spend.

The waterfall walks exactly that, and will show a negative net if we spent more than we
saved.

!!! note "`token_accounting` gates all of it"
    A request is priced only when the provider reported all four token tiers **and** the
    model's rates are known. Otherwise the row is marked `partial` or `missing` and its
    cost reads *unknown* — never zero. Filter to `complete` before quoting a dollar figure.

## Check what it cost you

Savings without their costs is not a measurement. The dashboard's **"What our own safety
mechanisms cost"** panel reports, beside the benefit:

- **frozen for cache safety** — compaction we did not do on the already-cached prefix;
- **restored after offload** — premature offloads the model asked back for;
- **reverted component runs** — the never-worse guard firing;
- **context-guru's own latency and LLM spend**.

And the **"why didn't you compact this?"** panel turns non-events into data:
`bypassed · below_trigger · cache_frozen · found_nothing · reverted · no_messages`.

## Find the components that earn their place

![Per-component economics](../img/dashboard/03-component-metrics.jpg)

Per component: runs · acted · act rate · reverted · unique/gross saved · overcount · own
latency · errors · verdict. This is how you find:

- a component that never fires on your traffic (**inert here**) — drop it from the pipeline;
- one that spent real wall time and returned nothing (**costly and inert**) — the most
  expensive kind of dead weight;
- one whose latency dwarfs its yield (**expensive for its yield**);
- `cacheinject`, which always reads **mutates, saves no content** because its win is a
  provider-side KV-cache hit, invisible to content-token counts.

Click a component to filter the request list to the requests it ran on, then open one and
read the diff.

## See exactly what changed

![Git-style content diff](../img/dashboard/09-content-git-diff.jpg)

The request drawer shows every rewritten message as a Git-style diff (plus side-by-side
and after-only views), ordered biggest-saving first. This is the check that a savings
number cannot give you: *did it remove the right thing?*

Content capture is on by default, redacted and size-capped before storage, and visible
from loopback or a trusted CIDR only. Disable it with `--dashboard-content=false`.

## A real session end to end

`scripts/cc-demo.sh` routes a real `claude` CLI session through the proxy: it builds a
tiny repo, starts the proxy, points Claude Code's `ANTHROPIC_BASE_URL` at it, and runs one
`claude -p` task. Add `--dashboard` to the proxy it starts and you get the whole picture
rather than a stats delta:

```sh
export ANTHROPIC_BASE_URL=...            # upstream Anthropic-compatible endpoint
export ANTHROPIC_AUTH_TOKEN=...
scripts/cc-demo.sh
# then open http://localhost:4000/dashboard/#sessions and click the session
```

It's the shortest way to see real savings on your own model without a full benchmark
harness.

## `GET /stats` — the scriptable snapshot

`/stats` remains the in-process snapshot the benchmark harnesses parse. Its shape is
**stable**: fields are only ever added, guarded by a golden test, because
`deploy/harbor/*.py` reads it by name and a rename would invalidate the published
reproduction path silently.

| Field | Meaning |
|---|---|
| `savings_pct` | Token-weighted Σ saved / Σ before — the whole-request (diluted) ratio |
| `savings_pct_attempted` | Σ saved / Σ attempted — the "of what we tried to compact" ratio |
| `savings_pct_new_input` | Σ saved / (fresh + cache-write + saved); **0** when the provider reported no usage, never ~100% |
| `attempted_tokens` / `frozen_tokens` | What compaction was allowed to touch, and what cache safety made us leave |
| `fresh_input_tokens` / `cache_read_tokens` / `cache_write_tokens` / `output_tokens` | The four billed tiers |
| `wasted_tokens` / `bounces` | Content offloaded then re-served via expand |
| `adjusted_saved` | `saved − wasted` — may be negative |
| `top_passthrough` | Components that ran but never changed a request |
| `components.<name>.saved_tokens_unique` / `.overcount_ratio` | Per-component unique savings and inflation |
| `cg_added_ms_avg` / `upstream_ms_avg` / `upstream_ms_avg_bypassed` | Latency, split by whether the request bypassed us |

!!! tip "Reading `top_passthrough`"
    A component listed there isn't necessarily broken. `cacheinject` always lands there —
    its savings are provider-side cache hits, invisible to content-token counts. But a
    content offloader that never fires is a candidate to drop.

`/stats` is in-memory and resets with the process. For history, retention, filtering,
sessions and diffs, use the dashboard.

## The Emitter interface

The pipeline depends only on the `Emitter` interface (`Component(Report)` +
`Run(RunReport)`), so it carries no telemetry-backend dependency. Swap implementations to
route metrics where you need:

| Emitter | Role |
|---|---|
| `Slog` | logs in the `context_engineering.*` vocabulary |
| `Aggregator` | in-process rollups behind `/stats` |
| `Tee` | fan-out to several emitters |
| `NopEmitter` | discard |

The dashboard does not replace any of these — it captures out of band from
[`apply.BodyTrace`](../design.md), so the aggregator stays the fast in-process counter.

## Benchmarks

For the full per-component SWE-bench evaluation — where `mask` delivers ~27%
content-token savings with no reward loss, and how the `/stats` within-run metric is
derived — see [Benchmarks](../RESULTS.md). To view a harness run in the dashboard, point
`--dashboard-bench-dirs` at its jobs root: each run's `summary.json` + `rows-<arm>.json`
is ingested, with cost-vs-reward per arm and per-task drill-down.

![Benchmark comparison](../img/dashboard/11-benchmark-comparison.jpg)

Quote **cost per solve**, not cost: an arm that spends less by solving fewer tasks has not
saved anything.
