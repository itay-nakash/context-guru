# extract

!!! info "Offload — lossy, reversible"
    Projects a large tool output down to what's relevant to the most recent user query; optionally an LLM writes the filter.

## How it works

`extract` projects a large tool output down to what's relevant to the **most recent user query**.
v1 ships the `deterministic` strategy: keep `head`/`tail` context, every line overlapping the
query's keywords, and every line carrying an error word; collapse the gaps with `…`. It stashes the
original behind a `<<cg:HASH>>` marker.

The relevance signal is the **full conversational context** (the task in the first user turn + the
most recent assistant/user turns), not just one trailing sentence — so it keeps what the agent
actually needs on any agent/benchmark.

With `code`, a cheap LLM writes a Starlark filter run in a sandbox (no imports/IO, step + 2s
limits). It sees the **full tool output** (bounded to ~32k chars) so it writes code **specific to
that content** — deleting the exact irrelevant lines/records AND words/sentences/parts within lines
— not a blind generic filter. It has regex helpers (`re_sub`/`re_findall`/`re_split`/`re_match`,
RE2, pure-Go). **Guarantee (default): deletion-only** — the result is accepted only if it is an
in-order **character subsequence** of the input (obtainable by deleting characters), so the model
can trim anything but provably cannot fabricate, reorder, or reword; else it falls back to
deterministic. JSON bodies are decoded and filtered structurally. `rlm` currently maps to `code`.

`rewrite` (opt-in, `code` only) drops the containment proof so the model may reword / summarize /
rewrite freely (lossy, **unverified** — only sanity + strictly-smaller apply). Pair it with a
non-`full` `marker_mode`. Default `false` keeps the verified deletion-only guarantee.

The LLM strategies (`code`/`rlm`) call a model chosen by `model.source`: `incoming` (default —
reuse the proxied request's own model + key) or `config` (a dedicated cheap model set via
`CHEAP_MODEL*` env / the gateway's `CheapModel`). When no model is available, `extract` degrades to
its deterministic projection.

**Gating + reuse (LLM strategies):** a `trigger` decides whether to spend a model call —
`min_output_tokens` (per tool output; folds in legacy `min_tokens`), `min_request_tokens`,
`min_messages` — so `code` fires only on a large output in a large request, not every turn. A
reduced output is cached per session by content hash, so the **same output re-sent on a later turn
reuses the prior compaction** (no new model call, byte-identical result → prefix stays KV-cache
stable).

## Before → After

```
before:  <300-line API dump>   (query mentions "auth timeout")
after:   <head> … lines mentioning auth/timeout + any error lines … <tail>
         <<cg:…>> [full output: call context_guru_expand]
```

## Lossiness

Lossy but reversible — the original is stashed and recovered via `context_guru_expand` /
`GET /expand`. The default `code` guarantee is deletion-only (a verified character subsequence);
`rewrite` mode is unverified and lossier.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `min_tokens` | 300 | Output floor before extraction runs. |
| `head_lines` / `tail_lines` | 5 | Context lines kept at start/end. |
| `strategy` | `deterministic` | `deterministic` \| `code` \| `rlm` (`rlm` maps to `code`). |
| `model.source` | `incoming` | LLM source for `code`/`rlm`: `incoming` (proxied model+key) or `config` (cheap model). |
| `trigger` | — | Gates a model call: `min_output_tokens`, `min_request_tokens`, `min_messages`. |
| `marker_mode` | — | How the recovery marker is emitted; pair a non-`full` mode with `rewrite`. |
| `rewrite` | `false` | Opt-in (`code` only): drop the containment proof for free rewording (lossy, unverified). |

## When it shines

Big query-focused MCP/API outputs, logs and file reads; structured JSON where a filter selects
records precisely.

## When it's inert

No user query, output below floor, request below `trigger`, projection not smaller, or (for `code`)
no model available → deterministic.

See also: [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
