# Running and measuring lab-cx

How to run each component and how to measure it. Every command here is real against the
current flags/targets. CGO is required throughout (`CGO_ENABLED=1`) — the skeletonizer
uses tree-sitter via cgo.

## Build

```sh
make build            # ./bin/lab-cx  (exports CGO_ENABLED=1)
./bin/lab-cx version
```

## Run the proxy

```sh
# preset mode (default :8080), routes by provider
./bin/lab-cx proxy --preset balanced

# config mode, forwarding all traffic to one upstream gateway, extractor on
./bin/lab-cx proxy --config configs/lab-cx.yaml \
  --upstream https://gateway.example/v1 \
  --extract-model claude-haiku-4-5 --extract-provider anthropic --extract-auth bearer \
  --extract-base https://gateway.example/v1
```

Point an agent at it:

```sh
ANTHROPIC_BASE_URL=http://localhost:8080         # Anthropic-wire agents
OPENAI_BASE_URL=http://localhost:8080/v1         # OpenAI-wire agents
```

Flags: `--addr`, `--preset` (`safe|balanced|aggressive|cache|coding|mcp`), `--config`,
`--upstream`, `--extract-model/-provider/-auth/-base`,
`--summarize-model/-provider/-auth/-base`, `--reduce-cached-prefix` (run the reducers +
extractor on a self-caching client's cached prefix instead of deferring to its cache —
see below), `--max-body-bytes` (default 33554432 = 32 MiB; `0` = no cap),
`--upstream-timeout` (default `0s`; non-zero caps the whole request incl. streamed
responses).

Self-caching agents (Claude Code): by default lab-cx defers to the client's own
`cache_control` (the cache stage stands down) and reduces little. Pass
`--reduce-cached-prefix` (or `reduce.reduce_cached_prefix: true`) to run the deterministic
reducers and the LLM extractor on the cached prefix too — real token reduction at the cost
of invalidating the client's cached prefix.

Model credentials: an inline `api_key` (or `key_env`) in the config wins; else the
compactor's env var (`LABCX_EXTRACT_KEY` / `LABCX_SUMMARIZE_KEY`); else the provider
default (`ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN`, or `OPENAI_API_KEY`). Set
`source: incoming` to reuse the proxied request's own model + credentials instead.

Disable / bypass:

- `LABCX_DISABLE=1` — transparent passthrough for the whole process.
- `x-labcx-bypass: true` header — skip reduction for one request.

## Read /stats

```sh
curl -s http://localhost:8080/stats          # raw JSON
./bin/lab-cx stats                            # JSON + one-line summary (default addr)
./bin/lab-cx stats --addr http://localhost:8080
```

`/stats` is comprehensive — global totals, a per-session breakdown, and a recent-per-call
ring:

- **Global:** `requests`, `tokens_before/after/saved`, `reduction_ratio`, `cache_injected`,
  `extracted`, `reduced_blocks`, `extract_candidates`, `stage_errors`, `cost_saved_usd`,
  `sessions`, and a `latency` object (`p50_ms`, `p95_ms`, `p99_ms`, `max_ms`, `mean_ms`;
  the flat `added_latency_p50_ms`/`p95_ms` are kept for compatibility).
- **`session_stats[]`** — per conversation: the same token/cache/extract/reduced metrics,
  `cost_saved_usd`, `tools_total`, `tool_def_tokens`, `model`, `surface`, and its own
  `latency` distribution.
- **`recent_calls[]`** (last 256) — every metric for each call: tokens + ratio,
  `cache_injected`, `extracted`, `reduced_blocks`, `extract_candidates`, `frozen_messages`,
  `rehydrated`, `at_compaction`, `session_id`, `request_model`, and `added_latency_ms`.

The same per-call fields are emitted as a structured slog event per request
(`gen_ai.*` + `context_engineering.*`). Recover an omitted block: `GET /labcx/expand?id=<marker-id>`.

## Measure: offline deterministic harness

No model in the loop — proves each deterministic component on real fixtures under
`testdata/fixtures/`. Token counts use the same `o200k_base` BPE counter the engine uses.

```sh
CGO_ENABLED=1 go run ./cmd/labcx-bench            # human-readable Markdown table
CGO_ENABLED=1 go run ./cmd/labcx-bench --json     # machine-readable rows
```

It wraps each fixture as a canonical Anthropic-shaped request, runs the engine with one
component enabled at a time plus the full deterministic pipeline, and verifies every row
is reversible. Recorded run: [RESULTS-offline.md](RESULTS-offline.md).

## Measure: cheap-model extractor harness (online)

`--extract` flips the harness into ONLINE mode against a real Anthropic-compatible
gateway (`claude-haiku-4-5`, bearer auth). Requires the gateway env:

```sh
export ANTHROPIC_BASE_URL=https://gateway.example/v1   # the model gateway
export ANTHROPIC_AUTH_TOKEN=<token>                    # never printed/committed
CGO_ENABLED=1 go run ./cmd/labcx-bench --extract          # Markdown table
CGO_ENABLED=1 go run ./cmd/labcx-bench --extract --json   # machine-readable rows
```

It fails fast if `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN` are unset. Recorded run:
[RESULTS-extract.md](RESULTS-extract.md).

## Run the Claude Code demo

```sh
ANTHROPIC_BASE_URL=https://gateway.example/v1 ANTHROPIC_AUTH_TOKEN=<token> \
  scripts/cc-demo.sh
```

Starts the proxy with the extractor on, routes a real `claude -p` task through it via a
settings file (so Claude Code's `env.ANTHROPIC_BASE_URL` precedence is handled), and
prints `/stats` before and after. Detail + measured result:
[integration/claude-code.md](integration/claude-code.md).

## Config-extension guide

`configs/lab-cx.yaml` selects components purely by **name**. Each list folds onto the base
`preset`; an empty/omitted list means "all built-in defaults"; list order sets
priority/run order. To add a component, register it once in its registry, then list its
name in the config — no other core edit needed.

| Component | Register it in | Then list it under |
|---|---|---|
| **Compactor** | `engine.(*Engine).Register` / `builtinCompactors` map (`engine/engine.go`) | `compactors:` |
| **Reducer** | `reduce.RegisterReducer(...)` (its `Reducer.Name`) | `reduce.reducers:` |
| **Encoder** (format re-encoder) | `allEncoders` table in `internal/reduce/actions.go` (unique name + rank) | `reduce.encoders:` (order = tie-break priority) |
| **Extract strategy** | `RunExtraction`'s switch + `rawStrategyOrder` in `internal/extract/extract.go` | `extract.strategies:` |

Every compaction approach (`reduce`, `extract`, `summarize`, `truncate`, `cache`)
implements one `engine.Compactor` interface — given the conversation, return the
transformed conversation. Built-ins: compactors `reduce, extract, summarize, truncate,
cache` (default order `extract, reduce, cache`; summarize/truncate are opt-in); reducers
`collapse, skeleton, format` (`cmdfilter` and `dedup` are separate passes toggled by their
own keys); encoders `json_compact, toon, jsonl, markdown_kv, tsv, csv`; strategies
`code, single, rlm, deterministic`.

Other `reduce` keys: `protect_recent` (keep N most recent turns at full fidelity),
`provable_only` (never drop merely-predicted-unused content), `cmd_filter` (bool).
`extract` and `summarize` share one LLM transport block: `provider`, `model`, `auth`,
`base`, plus credentials (`api_key` inline or `key_env` naming an env var) and `source`
(`config` = use the configured model; `incoming` = reuse the proxied request's own model +
credentials). `--extract-*` / `--summarize-*` flags override it. `extract` keys: `enabled`,
`mode` (`auto|single|rlm|deterministic`), `floor`. `summarize` keys: `enabled`, `level`
(`concise|regular|highly_detailed`), `keep_last`, `trigger_tokens`. `truncate` keys (no
model): `enabled`, `keep_last`, `trigger_tokens`. `cache` keys: `enabled`, `breakpoints`.
