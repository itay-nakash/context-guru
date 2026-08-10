# Choose a preset

A preset is a named, ordered pipeline. Set it once (`--preset` / `PRESET`, or `preset:` in
YAML) and every explicit field you add overrides it. This guide maps workloads to presets and
names the caveats before you commit.

!!! tip "Presets are just defaults"
    A preset expands to a default `pipeline:` list. Explicit `pipeline:` / `components:` blocks
    always win, so start from the closest preset and tune from there. See
    [Architecture](../design.md#config-registry).

## Which preset?

| Your workload | Preset |
|---|---|
| **Most agents — the default (SWE-bench-winning cache-aware config)** | **`codesmart`** |
| Same, but no LLM on the hot path (deterministic-only) | `codesafe` |
| Nothing — A/B baseline / passthrough control | `off` |
| Any traffic, want a guaranteed-safe win only | `safe` |
| Squeeze harder, tolerate LLM/structural offload | `aggressive` |
| Coding agent reading big source files | `coding` |
| MCP / list-endpoint JSON arrays | `mcp` |
| Long agentic sessions, age-based masking | `agent` / `general` |
| One long transcript to compress, run standalone | `summarize` |

## The presets

Every preset below is exactly the list in [`config/config.go`](../design.md#config-registry);
per-component behavior is in [Components](../components.md).

### `off` — `[]`
No components. Passthrough. Use it as the A/B control when you measure savings — the baseline in
[Benchmarks](../RESULTS.md) is this preset.

### `safe` — `[format, cachesplit]`
Two lossless [Reformat](../components.md#reformat-lossless) components only: compact JSON
(`format`) and the Anthropic volatile-tail split (`cachesplit`). Nothing is ever dropped, so there
is nothing to expand.

- **Fits:** any traffic where you want a zero-risk win and no reversibility surface.
- **Caveat:** `cachesplit`'s savings are provider-side cache hits, invisible to `/stats` token
  counts — it will show up under `top_passthrough`. That's expected, not dead weight.
- **Note:** breakpoint *placement* (`cacheinject`) is deliberately **not** here — it is unmeasured
  and opt-in since [#32](https://github.com/rossoctl/context-guru/issues/32). `cachesplit` carries
  the part with measured savings.

### `balanced` — `[format, dedup, failed_run, cmdfilter, cachesplit]`
The default. Adds three cheap, high-precision offloaders: exact-dup removal (`dedup`), superseded
test/build runs (`failed_run`), and DSL command-log filtering (`cmdfilter`).

- **Fits:** general agent traffic; the safe everyday choice.
- **Caveat:** `cmdfilter` only fires when ≥1 filter is loaded and the output's first line matches
  one. Its builtins cover pytest / npm-install / make; author more with a
  [custom DSL filter](custom-dsl-filter.md).

### `aggressive` — `[format, dedup, failed_run, cmdfilter, smartcrush, extract, cachesplit]`
`balanced` plus JSON-array crushing (`smartcrush`) and query-relevance projection (`extract`).

- **Fits:** you want more savings and accept structural/LLM offload with expand recovery.
- **Caveat:** `extract` with `strategy: code`/`rlm` spends a model call (gated by its `trigger`);
  the default `deterministic` strategy is free. Keep the [store](recover-context.md) on so the
  extra offloads stay recoverable.

### `coding` — `[format, skeleton, cmdfilter, cachesplit]`
Swaps in `skeleton`, which tree-sitter-parses fenced code blocks and replaces function bodies with
`{ … }`, keeping signatures/imports/types.

- **Fits:** a coding agent that reads large source files but mostly needs the shape.
- **Caveat:** `skeleton` is inert on unfenced file reads, unknown languages, or when the skeleton
  isn't smaller than the body.

### `mcp` — `[format, smartcrush, cachesplit]`
Targets homogeneous JSON arrays (list endpoints, search hits): keep `keep_first` + `keep_last`
items plus any item carrying an error signal, drop the middle.

- **Fits:** MCP tools and REST list endpoints returning long uniform arrays.
- **Caveat:** inert on non-array output or arrays below `min_items`.

### `agent` — `[format, dedup, failed_run, mask, extract, cachesplit]`
Tuned for long agentic sessions (e.g. Claude Code on SWE-bench) where the dominant cost is the
transcript of old tool outputs re-sent every turn.

- **Fits:** long-running agents with a growing transcript.
- **Caveat:** **`mask` is the biggest lever here** — age-based GC of tool outputs older than
  `keep_recent`. In the SWE-bench sweep it delivered ~27% mean content-token savings (up to 93.5%
  on a long session) with no reward loss ([Benchmarks](../RESULTS.md)). Order matters: lossless
  first, then offload old-then-large, cache last.

### `summarize` — `[summarize]`
One LLM component that collapses the middle of the transcript into a single
`=== History Summary ===` message, keeping the head + last few turns.

- **Fits:** long agentic sessions where the stale middle is the token cost.
- **Caveat:** **run it alone.** It changes the message count and restructures the whole transcript,
  so `apply.Body` rebuilds the body — no other component's in-place edits can race that rebuild.
  It needs a model; with none it no-ops.

!!! warning "LLM presets cost model calls"
    `aggressive` (via `extract` code/rlm) and `summarize` call a model. Both are gated by a
    `trigger` and reuse prior compactions per session, so they don't fire every turn. Pick the
    model with `model.source` (`incoming` reuses the request's own model+key; `config` uses a
    dedicated cheap model). See [LLM components](../design.md#llm-components).
