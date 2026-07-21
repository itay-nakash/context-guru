# Presets

A preset is a named default pipeline — an ordered list of component names.
Selecting one (`--preset <name>` / `PRESET`, or `preset:` in YAML) expands to
its pipeline; explicit config fields always override it. Pipelines below are
taken exactly from the `presets` map in `config/config.go`.

| Preset | Ordered pipeline | When to use |
|---|---|---|
| `off` | *(empty)* | Passthrough — no components. The baseline / A-B control. |
| `safe` | `format` → `cacheinject` | Lossless only: repack JSON compactly and add `cache_control`. Zero risk of dropping content. |
| `balanced` | `format` → `dedup` → `failed_run` → `cmdfilter` → `cacheinject` | The default. Lossless repack + conservative offloads (dedupe, drop superseded/failed runs, filter command noise) + cache. |
| `aggressive` | `format` → `dedup` → `failed_run` → `cmdfilter` → `smartcrush` → `extract` → `cacheinject` | `balanced` plus `smartcrush` (crush long homogeneous arrays) and `extract` (LLM relevance filter) for deeper savings. |
| `coding` | `format` → `skeleton` → `cmdfilter` → `cacheinject` | Coding agents: `skeleton` reduces big source-file reads to their structure via tree-sitter. |
| `mcp` | `format` → `smartcrush` → `cacheinject` | Tool/MCP servers returning long homogeneous JSON arrays (list endpoints, search hits). |
| `agent` | `format` → `dedup` → `failed_run` → `mask` → `extract` → `cacheinject` | Long agentic sessions (e.g. Claude Code on SWE-bench) where re-sent tool outputs dominate cost. `mask` is the biggest lever — ~27% content-token savings with no task-reward loss (see [Benchmarks](../RESULTS.md)). |
| `summarize` | `summarize` | Long trajectories where the transcript itself is the cost. **Runs alone** — it restructures the whole transcript (changes the message count), so no other component's in-place edits race the rebuild. |

!!! tip "Order matters"
    Components run in pipeline order: lossless repack first, then offloads
    (old-then-large), with `cacheinject` last so it keeps the reduced prefix
    cacheable.

Not sure which to pick? See [Choose a preset](../how-to/choose-a-preset.md).
Every component's config lives in [Components](../components.md).
