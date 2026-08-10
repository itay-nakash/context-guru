# Config & environment

One strict YAML struct serves both hosts (the proxy loads a file; the AuthBridge
plugin hands its `config:` subtree to the same loader). A `preset` expands to a
default pipeline; explicit fields override it.

## Config shape

The document has six top-level fields (from the `Config` struct in
`config/config.go`):

| Field | Type | Role |
|---|---|---|
| `preset` | string | Named default pipeline (see [Presets](presets.md)). |
| `pipeline` | `[]string` | Ordered component names — controls **order + enablement**. Overrides the preset's pipeline when present. |
| `components:<name>` | map | Each component's typed config block, handed to its constructor verbatim. |
| `store` | object | State store options — see [`store`](#store) below. |
| `mode` | string | Operating mode: `sync` (default) \| `observe`. See [Operating modes](../how-to/operating-modes.md). |
| `observe` | object | Observe-mode tuning; ignored in sync mode. |

### `store`

| Field | Default | Purpose |
|---|---|---|
| `enabled` | `true` | Toggles the state store. `false` wires a `store.Nop`: nothing is stashed, so offloads become **one-way** and must run `marker_mode: off`. |
| `ttl_seconds` | `10000` | Entry lifetime, and it **slides** — a `Get` refreshes the deadline, so an entry replayed every turn never ages out. Raised from 1800 because Terminal-Bench tasks average ~1975 s of wall clock and run to 4 h, so the old default expired live frozen decisions mid-task. |
| `max_entries` | `1000` | LRU cap. Frozen-decision keys (`cg:frz:`, `cg:res:`, `cg:len:`) are **pinned** — exempt from LRU eviction, because losing one is cache-destructive rather than merely a miss. The pin is capped at half `max_entries`, and eviction reclaims **expired** entries first (pinned included). |
| `max_sessions` | `100` | Cap on per-session sticky-id sets. |

The pinned prefixes are a code-level property of the key layout, supplied by their owners via
`store.Options.PinPrefixes` — not a YAML knob.

### `mode`

| Value | Behavior |
|---|---|
| `sync` (default) | Compact inline; the caller waits. Byte-identical to the behavior before modes existed. |
| `observe` | Forward the request untouched and report what compaction *would* have saved, under `potential_*` / `projected_*` keys. The request path never runs the pipeline and skips `expand.Inject` too (a tool declaration is a modification), so byte-identity is **structural**. |

Always explicit — nothing infers it from the rest of the configuration.

!!! note "An `async` mode is designed but not shipped"
    A third mode deferring compaction off the request path is implemented on a separate
    branch and deliberately held pending a benchmark arm establishing a benefit. `sync` and
    `observe` are the only values the loader accepts.

### `observe`

| Field | Default | Purpose |
|---|---|---|
| `max_queue` | `256` | Bound on the off-path measurement queue. A full queue **drops** (counted as `dropped`) and never blocks the request path. |
| `workers` | `1` | Drain goroutines. One keeps a single measurement's cheap-model call in flight per process, which keeps that spend and gateway rate limits predictable. |

!!! warning "Strict: unknown keys are rejected"
    The YAML loader runs with `KnownFields(true)`, so a typo'd key fails loudly
    at load time rather than being silently ignored.

## Example

```yaml
preset: balanced
pipeline: [format, dedup, failed_run, cmdfilter, cachesplit]   # order + enable
components:
  collapse:   { max_tokens: 2000, head_lines: 20, tail_lines: 20 }
  smartcrush: { min_items: 5, keep_first: 3, keep_last: 2 }
  cmdfilter:  { min_size: 500 }
store: { ttl_seconds: 10000, max_entries: 1000 }
mode: sync                          # sync | observe
```

A component registers its constructor + config type via `init()`, so adding one
makes it YAML-configurable with no core edit. See [Components](../components.md)
for every component's config block.

## Flags & environment

| Flag / env | Default | Purpose |
|---|---|---|
| `--preset` / `PRESET` | `codesmart` | Pipeline preset when no `--config`. |
| `--config` / `CONFIG` | — | YAML config file (overrides preset). |
| `LISTEN_ADDR` | `:4000` | Listen address. |
| `--openai-upstream` / `OPENAI_UPSTREAM` | `https://api.openai.com` | OpenAI upstream base. |
| `--anthropic-upstream` / `ANTHROPIC_UPSTREAM` | `https://api.anthropic.com` | Anthropic upstream base. |
| `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | — | Real key injected on forward (gateway mode); empty = pass client auth through. |
| `CHEAP_MODEL` (+ `CHEAP_MODEL_BASE` / `_KEY` / `_AUTH` / `_PROVIDER`) | — | Dedicated cheap model for the LLM components (`extract_llm`, `summarize`) — the `model.source: config` client. Without it they no-op. |
| `FORCE_MODEL` | — | Overwrite the request `model` (eval-containers uses `EVAL_MODEL`). |
| `INJECT_EXPAND` | `auto` | Whether the `context_guru_expand` tool is advertised: `auto` (only when the request already declares tools and the store persists) \| `always` \| `never`. |
| `CACHE_MODE` | `auto` | Cache-aware compaction: `auto` (on when the agent sets its own breakpoints) \| `on` \| `off`. |
| `MODEL_INFO_URL` / `MODEL_INFO` | LiteLLM map | Source for context-window sizes (used by the fractional triggers). `MODEL_INFO=off` disables the lookup; fractions are then ignored and absolutes apply. |
| `--store` / `STORE` | on | Enable/disable the state store; `--store=false` disables offload reversibility. Wins over the file's `store:` block. |
| `--mode` / `MODE` | `sync` | Operating mode: `sync` \| `observe`. Wins over the file's `mode:`. |

### Extraction-model pricing

[`extract_llm`](../components/extract_llm.md)'s economic gate only calls the LLM when the
expected saving exceeds the expected cost, so it needs the real price of a call. The cost is
computed from **observed token usage × these rates** — never a hard-coded per-call constant.
Defaults are `claude-haiku-4-5` list rates; override them to match your contract.

| Env | Default | Purpose |
|---|---|---|
| `CHEAP_MODEL_PRICE_IN` | `1.00` | Extraction-model input price, **dollars per million tokens**. |
| `CHEAP_MODEL_PRICE_OUT` | `5.00` | Output price per MTok. |
| `CHEAP_MODEL_PRICE_CACHE_WRITE` | `1.25` | Cache-write price per MTok (1.25× input). |
| `CHEAP_MODEL_PRICE_CACHE_READ` | `0.10` | Cache-read price per MTok (0.1× input). |

An unparseable or absent value silently keeps the default — pricing must never fail a request.

## Diagnostics

| Env | Effect |
|---|---|
| `CONTEXT_GURU_DEBUG=1` | Logs each tool output's token count + first line. |
| `CONTEXT_GURU_DUMP=<file>` | Appends a before → after JSON record per rewritten message. |
| `CONTEXT_GURU_CAPTURE=<file>` | Appends the pristine inbound request body to a JSONL file — the input for offline replay. |
