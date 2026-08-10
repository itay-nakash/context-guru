# Presets

A preset is a named default pipeline — an ordered list of component names.
Selecting one (`--preset <name>` / `PRESET`, or `preset:` in YAML) expands to
its pipeline; explicit config fields always override it. Pipelines below are
taken exactly from the `presets` map in `config/config.go`.

| Preset | Ordered pipeline | When to use |
|---|---|---|
| `codesmart` | `format` → `dedup` → `failed_run` → `cmdfilter` → `extract_llm` → `extract` → `cacheinject` | **The default.** The SWE-bench-winning cache-aware config: structural offloaders + a cheap-model relevance-trimmer (`extract_llm`, routed to `CHEAP_MODEL`, gated so most turns make no model call) + deterministic `extract`. `extract_llm` no-ops (→ deterministic) when no cheap model is configured. |
| `codesafe` | `format` → `dedup` → `failed_run` → `cmdfilter` → `extract` → `collapse` → `cacheinject` | `codesmart` minus the LLM pass — **deterministic-only, zero model calls by policy**. The safe control / the choice when you don't want an LLM on the hot path. |
| `off` | *(empty)* | Passthrough — no components. The baseline / A-B control. |
| `safe` | `format` → `cacheinject` | Lossless only: repack JSON compactly and add `cache_control`. Zero risk of dropping content. |
| `balanced` | `format` → `dedup` → `failed_run` → `cmdfilter` → `cacheinject` | Lossless repack + conservative offloads (dedupe, drop superseded/failed runs, filter command noise) + cache. |
| `aggressive` | `format` → `dedup` → `failed_run` → `cmdfilter` → `smartcrush` → `extract` → `extract_llm` → `cacheinject` | `balanced` plus `smartcrush` (crush long homogeneous arrays), deterministic `extract` (noise collapse), and `extract_llm` (cheap-model relevance trim) for deeper savings. |
| `coding` | `format` → `skeleton` → `cmdfilter` → `cacheinject` | Coding agents: `skeleton` reduces big source-file reads to their structure via tree-sitter. |
| `mcp` | `format` → `smartcrush` → `cacheinject` | Tool/MCP servers returning long homogeneous JSON arrays (list endpoints, search hits). |
| `agent` | `format` → `dedup` → `failed_run` → `mask` → `extract` → `cacheinject` | Long agentic sessions (e.g. Claude Code on SWE-bench) where re-sent tool outputs dominate cost. `mask` is the biggest lever — ~27% content-token savings with no task-reward loss (see [Benchmarks](../RESULTS.md)). |
| `summarize` | `summarize` | Long trajectories where the transcript itself is the cost. **Runs alone** — it restructures the whole transcript (changes the message count), so no other component's in-place edits race the rebuild. |

!!! tip "Order matters"
    Components run in pipeline order: lossless repack first, then offloads
    (old-then-large), with `cacheinject` last so it keeps the reduced prefix
    cacheable.

!!! warning "`extract_llm` is now OFF by default on prompt-caching backends (`codesmart`, `aggressive`)"
    `extract_llm` is the only component that spends money to save money, and on a
    prompt-caching backend it was measured **~8× underwater**: a token removed from a cached
    region saves the cache-read rate (`$0.30/MTok`), not the fresh-input rate (`$3/MTok`), so
    break-even is **~30,500 tokens per output** at the measured compression ratio — far above a
    typical tool output (the largest in one capture was 2,053).

    Since #28 the component **declines to run at all on caching backends** unless
    `allow_on_caching_backend: true` is set. This is enforced in code rather than documented as
    advice, because every caching workload measured came out net-negative even with the
    [economic gate](../components/extract_llm.md#economics) working correctly. So `codesmart`
    and `aggressive` still list `extract_llm`, but on caching traffic it makes **zero calls**
    and costs nothing; the deterministic passes do the work.

    On **non-caching** traffic it runs, and the gate decides per call — a strict improvement
    over the old behavior in every arm measured (waste cut 68% while saving more tokens on one
    capture; 26 calls reduced to 1 on another). Even there the honest result on those captures
    is break-even rather than profit: it earns its place when outputs are genuinely large.
    See [the component's measured tables](../components/extract_llm.md#measured-after-28-replay-of-real-captures-awsclaude-haiku-4-5).

    `codesmart`'s pinned `min_tokens: 3000` still governs its per-output floor, unchanged.

Not sure which to pick? See [Choose a preset](../how-to/choose-a-preset.md).
Every component's config lives in [Components](../components.md).
