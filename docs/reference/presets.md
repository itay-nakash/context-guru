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

!!! warning "`extract_llm` now declines uneconomic calls (`codesmart`, `aggressive`)"
    `extract_llm` is the only component that spends money to save money, and on a
    prompt-caching backend it was measured **~8× underwater**: a token removed from a cached
    region saves the cache-read rate (`$0.30/MTok`), not the fresh-input rate (`$3/MTok`), so
    break-even is **~12,700 tokens per output** — far above a typical tool output.

    Since #28 it applies an [economic gate](../components/extract_llm.md#economics): it calls
    the LLM only when the expected saving exceeds the expected cost. On a caching backend that
    means **most candidates are suppressed** and `codesmart`/`aggressive` make far fewer model
    calls than before — cheaper and faster, with savings coming mainly from the (now global)
    result cache. On a **non-caching** backend the gate permits far more, because there the
    reduction is worth 10× more.

    Check `/stats` → `extract.net_value_usd`. If it is negative on your workload, drop
    `extract_llm` from the pipeline (or use `codesafe`, which never had it). `codesmart`'s
    pinned `min_tokens: 3000` still governs its per-output floor, so that part is unchanged.

Not sure which to pick? See [Choose a preset](../how-to/choose-a-preset.md).
Every component's config lives in [Components](../components.md).
