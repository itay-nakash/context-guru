# extract_llm

!!! info "Offload — lossy, reversible (LLM-written filter)"
    A cheap model writes a small program that projects a large tool output down to what the agent
    actually needs, deletes the rest, and stashes the original. The powerful, relevance-aware
    counterpart to the deterministic [`extract`](extract.md).

## How it works

For a large tool output, `extract_llm` asks a **cheap model** to write a short **Starlark filter**
specific to that content. The program sees the full output (bounded to ~32k chars) and the recent
conversation, so it can delete the exact irrelevant lines/records — and, in `rewrite` mode, reword
or collapse spans — while keeping ids, paths, and error lines verbatim. The program runs in a
**sandbox** (no imports/IO, step + 2s limits) and the result must pass a sanity check
(non-empty, strictly smaller, required ids present); on any miss the output is left verbatim. It has
RE2 regex helpers (`re_sub` / `re_findall` / `re_split` / `re_match`). JSON bodies are decoded and
filtered structurally.

- **Deletion-only guarantee (opt-in):** set `rewrite: false` and the result is accepted only if it is
  an in-order **character subsequence** of the input — the model can trim anything but provably
  cannot fabricate, reorder, or reword. Default `rewrite: true` is the more powerful mode (reword /
  summarize allowed; ids/paths/errors still required verbatim by the sanity check).
- **Model source:** `model.source` is `incoming` (default — reuse the proxied request's own model +
  key) or `config` (a dedicated cheap model set via `CHEAP_MODEL*` env / the gateway's `CheapModel`).
  With no model available it degrades to a no-op (the deterministic `extract` still runs if present).
- **Throttled + reused:** this is the expensive pass, so it is gated by `trigger`
  (`min_output_tokens`, `min_request_tokens`, `min_messages`) and throttled per session
  (`llm_every_n_requests`) and per request (`llm_max_per_request`). A reduced output is **checkpointed
  per session by content hash** — the same output re-sent on a later turn reuses the prior compaction
  (no new model call, byte-identical result → prefix stays KV-cache stable).
- **Cache-aware `skip_file_reads`:** tri-state. Unset = AUTO — skip line-numbered source-file dumps
  when the request is prompt-cached (they already bill at the cheap cache-read rate, so reducing them
  costs more than it saves), reduce them otherwise. See the cache-aware rationale in
  [design.md](../design.md).

## Before → After

Captured **live** through the proxy (`pipeline: [extract_llm]`, `strategy: code`,
`model.source: config` → `aws/claude-haiku-4-5`). The query was *"find the auth timeout error and
nearby context"*; the model kept the error plus a few surrounding requests and elided ~118 repetitive
successful-request lines:

```
before:  2024 GET /users/0 200 12ms          ← 60 near-identical lines
         … 58 more …
         2024 GET /users/59 200 12ms
         ERROR auth timeout on token refresh
         2024 GET /items/0 200 8ms            ← 60 more near-identical lines
         … 59 more …

after:   2024 GET /users/58 200 12ms
         2024 GET /users/59 200 12ms
         ERROR auth timeout on token refresh
         2024 GET /items/0 200 8ms
         2024 GET /items/1 200 8ms
         [auth timeout error + context; repetitive successful requests elided]
         <<cg:923fff04ab267215>> [full output: call context_guru_expand]
```

## Lossiness

Lossy but reversible — the original is stashed and recovered via `context_guru_expand` /
`GET /expand`. The default `rewrite: true` mode is unverified (sanity + strictly-smaller only);
`rewrite: false` gives the verified deletion-only (character-subsequence) guarantee.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `min_tokens` | — | Output floor (folds into `trigger.min_output_tokens`). |
| `strategy` | `code` | `code` \| `single` \| `rlm` \| `auto` (`rlm` maps to `code`). |
| `model.source` | `incoming` | `incoming` (proxied model+key) or `config` (cheap model via `CHEAP_MODEL*`). |
| `trigger` | — | Gates a model call: `min_output_tokens`, `min_request_tokens`, `min_messages`. |
| `llm_every_n_requests` | — | Fire the LLM path at most once per N requests per session. |
| `llm_max_per_request` | 0 | Cap LLM calls per firing request (0 = unlimited). |
| `rewrite` | `true` | `false` forces the verified deletion-only (subsequence) guarantee. |
| `skip_file_reads` | auto | Skip line-numbered source dumps when cached; `true`/`false` to force. |
| `marker_mode` | `full` | How the recovery marker is emitted: `full` \| `summary` \| `off`. |

## When it shines

Big, query-focused MCP/API outputs, logs, and file reads; structured JSON where a filter can select
records precisely. It is the largest deterministic saving in the SWE-bench sweep alongside the cheap
`extract`/`dedup`/`cmdfilter` passes — see [RESULTS.md](../RESULTS.md).

## When it's inert

Output below the floor, request below `trigger`, throttled out this turn, projection not smaller, or
no model available.

See also: [`extract`](extract.md) · [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
