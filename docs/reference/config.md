# Config & environment

One strict YAML struct serves both hosts (the proxy loads a file; the AuthBridge
plugin hands its `config:` subtree to the same loader). A `preset` expands to a
default pipeline; explicit fields override it.

## Config shape

The document has four top-level fields (from the `Config` struct in
`config/config.go`):

| Field | Type | Role |
|---|---|---|
| `preset` | string | Named default pipeline (see [Presets](presets.md)). |
| `pipeline` | `[]string` | Ordered component names — controls **order + enablement**. Overrides the preset's pipeline when present. |
| `components:<name>` | map | Each component's typed config block, handed to its constructor verbatim. |
| `store` | object | State store options (`enabled`, `ttl_seconds`, `max_entries`, …). |

!!! warning "Strict: unknown keys are rejected"
    The YAML loader runs with `KnownFields(true)`, so a typo'd key fails loudly
    at load time rather than being silently ignored.

## Example

```yaml
preset: balanced
pipeline: [format, dedup, failed_run, cmdfilter, cacheinject]   # order + enable
components:
  collapse:   { max_tokens: 2000, head_lines: 20, tail_lines: 20 }
  smartcrush: { min_items: 5, keep_first: 3, keep_last: 2 }
store: { ttl_seconds: 1800, max_entries: 1000 }
```

A component registers its constructor + config type via `init()`, so adding one
makes it YAML-configurable with no core edit. See [Components](../components.md)
for every component's config block.

## Flags & environment

| Flag / env | Default | Purpose |
|---|---|---|
| `--preset` / `PRESET` | `balanced` | Pipeline preset when no `--config`. |
| `--config` / `CONFIG` | — | YAML config file (overrides preset). |
| `LISTEN_ADDR` | `:4000` | Listen address. |
| `--openai-upstream` / `OPENAI_UPSTREAM` | `https://api.openai.com` | OpenAI upstream base. |
| `--anthropic-upstream` / `ANTHROPIC_UPSTREAM` | `https://api.anthropic.com` | Anthropic upstream base. |
| `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | — | Real key injected on forward (gateway mode); empty = pass client auth through. |
| `FORCE_MODEL` | — | Overwrite the request `model` (eval-containers uses `EVAL_MODEL`). |
| `--store` / `STORE` | on | Enable/disable the state store; `--store=false` disables offload reversibility. Wins over the file's `store:` block. |

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

!!! note "`extract_llm` is off by default on caching backends"
    Independently of pricing, the component declines to run on prompt-caching traffic unless
    `allow_on_caching_backend: true` is set — measured net-negative there. See
    [extract_llm](../components/extract_llm.md#the-honest-verdict).

## Diagnostics

| Env | Effect |
|---|---|
| `CONTEXT_GURU_DEBUG=1` | Logs each tool output's token count + first line. |
| `CONTEXT_GURU_DUMP=<file>` | Appends a before → after JSON record per rewritten message. |
