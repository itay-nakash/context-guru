# Components

Every registered component, exactly as it behaves in code. All operate on tool-output
messages (`role:"tool"`; for Anthropic, `tool_result` blocks normalized to that shape by
`apply`). **Reformat** = lossless. **Offload** = drops bytes, stashes the original, leaves a
`<<cg:HASH>>` marker recoverable via `context_guru_expand` / `GET /expand`.

## Summary

| Component | Kind | What it drops | Recoverable | Fires on | Key config (default) |
|---|---|---|---|---|---|
| `format` | Reformat | nothing (compacts JSON) | n/a (lossless) | pretty-printed JSON tool output | `min_tokens` (50) |
| `cacheinject` | Reformat | nothing (adds `cache_control`) | n/a (lossless) | Anthropic-family requests | — |
| `skeleton` | Offload | function/method bodies | via expand | fenced ` ```lang ` code blocks | `min_tokens` (80) |
| `dedup` | Offload | later byte-identical tool outputs | via expand | repeated identical outputs | `min_tokens` (100) |
| `collapse` | Offload | middle of an oversized output | via expand | any large tool output (fallback) | `max_tokens` (2000), `head_lines` (20), `tail_lines` (20) |
| `failed_run` | Offload | earlier superseded test/build runs | via expand | ≥2 run-like outputs | `min_tokens` (100) |
| `cmdfilter` | Offload | lines per declarative DSL filter | via expand | output matching a filter | `filters` ([]), `disable_builtins` (false) |
| `extract` | Offload | query-irrelevant lines | via expand | large output + a recent query | `min_tokens` (300), `head_lines`/`tail_lines` (5), `strategy` (deterministic) |
| `smartcrush` | Offload | middle items of a JSON array | via expand | JSON-array tool output | `min_items` (5), `min_tokens` (200), `keep_first` (3), `keep_last` (2) |
| `mask` | Offload | older tool outputs (age-based) | via expand | more than `keep_recent` outputs | `keep_recent` (3), `min_tokens` (100) |
| `phi_evict` | Offload | lowest-scoring outputs over budget | via expand | transcript over `budget_tokens` | `budget_tokens` (120000), `weights` (balanced) |

Presets (`config`): `off` `[]` · `safe` `[format, cacheinject]` · `balanced`
`[format, dedup, failed_run, cmdfilter, cacheinject]` · `aggressive` adds `smartcrush, extract` ·
`coding` `[format, skeleton, cmdfilter, cacheinject]` · `mcp` `[format, smartcrush, cacheinject]`.

Common gates every Offload respects: skip non-text (`Rewritable`) messages, skip content already
carrying a marker (no double-offload), and skip if the rewrite (marker + hint included) isn't
actually smaller.

---

## Reformat (lossless)

### `format`
Re-encodes a pretty-printed JSON tool output as compact JSON — same value, fewer whitespace
tokens. Only acts on tool messages whose trimmed text starts with `{`/`[`, is valid JSON, is
≥ `min_tokens`, and gets smaller. v1 is json-compact only (a TOON encoder is planned).

```
before:  { "id": 1,           after:  {"id":1,"name":"ada","tags":["x","y"]}
           "name": "ada",
           "tags": [ "x", "y" ] }
```

- **Lossiness:** none — nothing stashed. **Shines:** verbose pretty-printed JSON/MCP payloads.
  **Inert:** already-compact JSON, non-JSON text, small outputs.

### `cacheinject`
Places an Anthropic `cache_control: {type: ephemeral}` breakpoint on the last content block of
the message just **before** the newest turn (a stable prefix boundary), so the provider KV cache
hits across turns. Adds a control directive, changes no model-visible content.

- **Lossiness:** none. **Shines:** Anthropic/Bedrock/Vertex agents that don't self-cache (the
  savings lever is provider-side cache hits, invisible to `/stats` token counts). **Inert:**
  non-cache-aware providers, string-content messages (can't carry a block breakpoint), a
  breakpoint already present. `/stats` will list it under `top_passthrough` since it saves no
  *content* tokens — that's expected, not dead weight.

---

## Offload (lossy, reversible)

### `skeleton`
Parses fenced ` ```lang ` code blocks with tree-sitter and replaces function/method/constructor
**bodies** with a placeholder, keeping signatures, imports, types, and class bodies (so method
signatures survive). Stashes the whole original message.

```mermaid
flowchart LR
  A["```go fenced block<br/>full func bodies"] --> B{tree-sitter parse<br/>lang known? body ≥ min_tokens?}
  B -->|no| A
  B -->|yes| C["signatures + { … }<br/>+ <<cg:HASH>> marker"]
  C --> D[(Store: original)]
```

```
before:  func Add(a, b int) int {          after:  func Add(a, b int) int { … }
             return a + b                           func Sub(a, b int) int { … }
         }                                          <<cg:9f2a…>> [full source: call context_guru_expand]
```

- **Config:** `min_tokens` (80, per body). Grammars: go, python, js/ts/tsx, rust, java, c/cpp,
  ruby, php, c#, kotlin, swift, scala. **Shines:** the `coding` preset — the agent reads big
  source files but mostly needs the shape. **Inert:** no fenced blocks, unfenced file reads,
  unknown language, skeleton not smaller than the body.

### `dedup`
Replaces a tool output byte-identical to an earlier one in the same request with a short pointer +
marker. Exact match only (near-duplicate is deferred).

```
before:  <big config dump>  … (later, identical) <same big config dump>
after:   <big config dump>  … [identical to an earlier tool output] <<cg:1c8e…>>
```

- **Config:** `min_tokens` (100). **Shines:** agents that re-read the same file/command output
  repeatedly. **Inert:** no exact repeats, small outputs.

### `collapse`
Content-agnostic fallback for an oversized tool output nothing more specific handled: keep a
`head_lines` + `tail_lines` window, stash the full original. Runs late (after cmdfilter/format);
skips content already marked.

```
before:  <2,000-line log>
after:   <first 20 lines>
         ... (1960 lines omitted) <<cg:44ab…>> [full output: call context_guru_expand]
         <last 20 lines>
```

- **Config:** `max_tokens` (2000 threshold), `head_lines` (20), `tail_lines` (20). **Shines:** a
  catch-all last stage for huge outputs. **Inert:** output ≤ `max_tokens`, or too few lines for
  head/tail to help.

### `failed_run`
Recognizes test/build run output (regex: `N passed/failed`, `BUILD SUCCESS/FAIL`, `Traceback`,
`FAILED`, `panic:`, `npm ERR!`, pytest session banners). Keeps the **most recent** run in full,
collapses every earlier run to a pointer + marker — a superseded run is safely recoverable.

```
before:  [run 1] 3 failed, 5 passed …   [run 2 after fix] 8 passed
after:   [superseded by a later run] <<cg:7d1c…>> [full output: …]   [run 2] 8 passed
```

- **Config:** `min_tokens` (100). Needs ≥2 run-like outputs. **Shines:** iterative fix→re-run
  loops. **Inert:** <2 runs detected, small outputs. False positives cost only an expand
  round-trip, never data.

### `cmdfilter`
Shrinks tool output with **declarative DSL filters** (see below). Matches a filter on the output's
first non-empty line, applies its 8-stage pipeline, stashes the original, and appends a recovery
hint only when the filter was actually lossy. Ships builtin `pytest` / `npm-install` / `make`
filters.

```
before:  pytest … 100 lines of PASSED + warnings + 1 failure
after:   <failures + summary, passing noise stripped, ≤80 lines> <<cg:…>> [full output: …]
```

- **Config:** `filters` (inline filter YAML docs, added with no recompile), `disable_builtins`.
  `Enabled` only when ≥1 filter is loaded. **Shines:** noisy but structured command/log output
  (test runners, package managers, build tools). **Inert:** output whose first line matches no
  filter, or where filtering doesn't shrink it.

### `extract`
Projects a large tool output down to what's relevant to the **most recent user query**. v1 ships
the `deterministic` strategy: keep `head`/`tail` context, every line overlapping the query's
keywords, and every line carrying an error word; collapse the gaps with `…`. Stashes the original.

```
before:  <300-line API dump>   (query mentions "auth timeout")
after:   <head> … lines mentioning auth/timeout + any error lines … <tail>
         <<cg:…>> [full output: call context_guru_expand]
```

- **Config:** `min_tokens` (300), `head_lines`/`tail_lines` (5), `strategy`
  (`deterministic` | `code` | `rlm`). `NeedsModel()` is true for `code`/`rlm`, but the cheap-LLM
  client isn't wired yet — those select-and-fall-back to deterministic. **Shines:** big
  query-focused MCP/API outputs. **Inert:** no user query to score against, output < `min_tokens`,
  projection not smaller.

### `smartcrush`
Statistical JSON-**array** compressor: parse the array, keep `keep_first` + `keep_last` items plus
any item whose raw JSON carries an error signal, drop the rest, stash the full original. Kept items
are verbatim (schema-preserving).

```
before:  [ {…}, {…}, … 200 items … ]
after:   [ item0, item1, item2, item198, item199 ] [5 of 200 items shown; full array: call …] <<cg:…>>
```

- **Config:** `min_items` (5), `min_tokens` (200), `keep_first` (3), `keep_last` (2). **Shines:**
  long homogeneous JSON arrays (list endpoints, search hits) — the `mcp` preset. **Inert:**
  non-array output, fewer than `min_items`, nothing to drop. v1 uses fixed anchors (headroom's
  Kneedle adaptive-K is a documented refinement).

### `mask`
Age-based garbage collection: keep the newest `keep_recent` tool outputs verbatim, replace older
ones (≥ `min_tokens`) with a short marker + stash. Complementary to the content-based offloaders.

```
after (older):  [older tool output masked] <<cg:…>> [full output: call context_guru_expand]
```

- **Config:** `keep_recent` (3), `min_tokens` (100). **Shines:** long agent trajectories where old
  tool results are unlikely to matter. **Inert:** ≤ `keep_recent` tool outputs, small outputs.

### `phi_evict`
Ranks tool outputs by a lean-ctx-style context-field score and evicts the lowest until the
transcript fits `budget_tokens`. Never evicts the most recent tool output.

$$\Phi = w_R\cdot\text{relevance} + w_H\cdot\text{recency} - w_C\cdot\text{cost} - w_D\cdot\text{redundancy}$$

- **relevance** = keyword overlap with the last user message; **recency** = position (newest = 1);
  **cost** = share of total tool tokens; **redundancy** = 1 if a duplicate seen earlier.
- **Config:** `budget_tokens` (120000), `weights` — `balanced` `{R.40,H.20,C.30,D.10}`,
  `aggressive` (punish cost: `{.30,.10,.45,.15}`), `conservative` (`{.45,.20,.05,.05}`).
- **Shines:** very long transcripts that must fit a context budget. **Inert:** total tool tokens ≤
  budget, or ≤1 tool output. The scalar-ranking essence of lean-ctx's Context Field (the full
  heat-diffusion/MMR/bandit machinery is a documented refinement).

---

## The DSL filter engine

`components/dsl` is a declarative, user-extensible text-filter engine (adapted from rtk), wrapped
by `cmdfilter`. Filters are authored in YAML (no recompile), matched first-by-sorted-name, and each
runs a fixed **8-stage** pipeline. Because filters drop lines they are lossy, which is why the
wrapping `cmdfilter` component is an Offload (it stashes the original first).

```mermaid
flowchart LR
  I[input] --> S1[1 strip_ansi] --> S2[2 replace[]] --> S3[3 match_output[] + unless]
  S3 --> S4[4 strip / keep lines] --> S5[5 truncate_lines_at] --> S6[6 head / tail]
  S6 --> S7[7 max_lines] --> S8[8 on_empty] --> O[output + Lossiness]
```

Filter fields (all optional except `match`): `match` (regex vs the selector = first non-empty
line), `strip_ansi`, `replace` (chained `pattern`→`replacement`, `$1` backrefs), `match_output`
(whole-blob short-circuit: `pattern`/`message`/`unless`), `strip_lines_matching` **xor**
`keep_lines_matching`, `truncate_lines_at` (per-line char cap), `head_lines`/`tail_lines`,
`max_lines` (absolute cap with omission marker), `on_empty` (replacement when output is blank).

`Lossiness` reported back to `cmdfilter` (drives whether a recovery hint is appended): `None`
(nothing dropped / reversible reformat), `Tail` (a clean contiguous tail dropped), `Whole`
(non-contiguous or whole-blob loss).

```yaml
schema_version: 1
filters:
  pytest:
    description: keep failures + summary, drop passing noise
    match: "(pytest|=+ test session starts)"
    strip_lines_matching: ["^\\s*$", " PASSED", "^\\.+$"]
    max_lines: 80
    on_empty: "pytest: all passed"
tests:                       # inline; run via dsl.RunTests (a `verify` command)
  pytest:
    - name: all-green
      input: "pytest\n....\n"
      expected: "pytest: all passed"
```

Documents load with `schema_version: 1` and strict unknown-field rejection. Inline `tests`
(input → expected) run via `dsl.RunTests`, so a filter ships with its own regression check.
