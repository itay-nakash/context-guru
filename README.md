# lab-context-engineering

Context engineering for LLM agents — reduce token cost without changing the agent or
hurting the result.

`lab-context-engineering` is a [Kagenti](https://github.com/kagenti/kagenti) platform
component: a single Go core that sits between an LLM coding agent and the model API,
reduces the context the request carries (caching, lossless reduction, and
verified-lossless extraction), and forwards a cheaper request upstream. It runs from one
codebase as a **standalone proxy** (`lab-cx proxy`), an **importable Go library**
(`engine`, `surfaces`, `config`), or **inside eval-containers**.

It is **fail-open** (any error in any stage forwards the original request untouched) and
every reduction is **reversible** (a namespaced marker plus a content-addressed rewind
store, so a downstream consumer or the agent can always recover the original bytes).

## Install

Prerequisites:

- **Go 1.25**
- **A C toolchain** (`gcc`/`clang`) — the code skeletonizer uses tree-sitter via cgo, so
  `CGO_ENABLED=1` is required to build, test, and lint. The Makefile exports it for you.

```sh
make build           # builds ./bin/lab-cx (CGO_ENABLED=1)
./bin/lab-cx version  # prints: lab-cx <version> (<commit>)
```

## Run

Point any agent's base URL at the proxy — no agent changes:

```sh
./bin/lab-cx proxy --preset balanced
# then, for any agent:
#   ANTHROPIC_BASE_URL=http://localhost:8080
#   OPENAI_BASE_URL=http://localhost:8080/v1
```

Works with Claude Code, Bob/OpenClaw, Codex, Cursor, Aider. With a config file and the
cheap-model extractor enabled, forwarding all traffic to an upstream gateway:

```sh
./bin/lab-cx proxy --config configs/lab-cx.yaml \
  --upstream https://gateway.example/v1 \
  --extract-model claude-haiku-4-5 --extract-provider anthropic --extract-auth bearer \
  --extract-base https://gateway.example/v1
```

`proxy` flags: `--addr` (default `:8080`), `--preset`
(`safe|balanced|aggressive|cache|coding|mcp`), `--config` (a YAML file that names which
components run, overrides `--preset`), `--upstream` (forward ALL requests here; default
routes by provider), `--extract-model/-provider/-auth/-base` and
`--summarize-model/-provider/-auth/-base` (enable + configure the cheap-model extractor /
summarizer; these override the config's transport block), `--max-body-bytes`
(default 32 MiB; `0` = no cap), `--upstream-timeout` (default `0s` = none; a non-zero
value caps the whole request and can truncate long SSE streams).

Read the live aggregate metrics:

```sh
./bin/lab-cx stats                       # GETs /stats from a running proxy + prints a summary
./bin/lab-cx stats --addr http://localhost:8080
```

Endpoints the proxy serves:

- `GET /stats` — process-wide reduction snapshot as JSON (tokens before/after/saved,
  cache injected, extracted, stage errors, added-latency p50/p95).
- `GET /labcx/expand?id=<marker-id>` — returns the original bytes behind a reversibility
  marker.
- `GET /health`, `GET /ready` — liveness/readiness.

Bypass / disable:

- `LABCX_DISABLE=1` env var — run the proxy as a transparent passthrough.
- `x-labcx-bypass: true` request header — skip reduction for that single request.

## Components

- **Cache** — injects Anthropic `cache_control` breakpoints on the stable prefix
  (lossless). Stands down when the client already self-caches (e.g. Claude Code).
- **Reduce** (deterministic, no model) — `relevance` scoring + `collapse` of
  stale/duplicate/empty content, `dedup`, `cmdfilter` (trims noisy shell-command output),
  `skeleton` (drops function bodies, keeps signatures, via tree-sitter), and `format`
  re-encoders that re-express bulky structured output in cheaper lossless forms
  (`json_compact`, `toon`, `jsonl`, `markdown_kv`, `tsv`, `csv`).
- **Extract** (cheap model) — a cheap model proposes a structured projection of a huge
  tool/MCP output; accepted only if **structurally contained** in the original. Strategies
  `code` (model writes a filter run in a **Starlark sandbox** over the full body), `single`
  (one-shot JSON-return filter), `rlm` (chunked), and a `deterministic` fallback.
- **Summarize** (cheap model, opt-in) — replaces older turns with one factual
  `<summary>` (ReSum-style prompt), keeping the last `keep_last` messages verbatim; the
  dropped span is stored and recoverable.
- **Truncate** (no model, opt-in) — the naive baseline: keep the last `keep_last`
  messages, drop the rest behind one recoverable note. The control to measure smarter
  compactors against.
- **Tokenizer** — real BPE token counts via tiktoken `o200k_base` (`internal/tokens`); the
  same counter gates every reduction (a component never inflates an output).
- **Reversibility** — namespaced markers + a content-addressed rewind store; recover via
  `engine.FindMarkers` + `engine.Expand`, or the `/labcx/expand` endpoint.
- **Observability** — OpenTelemetry GenAI semantic conventions (`gen_ai.*`) plus the live
  `/stats` aggregate.

## Config

Every compaction approach — `reduce`, `extract`, `summarize`, `truncate`, `cache` —
implements one interface, `engine.Compactor` (given the conversation, return the
transformed conversation). The `compactors:` list selects which run and in what order.

The example config is [`configs/lab-cx.yaml`](configs/lab-cx.yaml). It folds onto a base
`preset`, then selects which compactors/reducers/encoders/extract-strategies run, **purely
by name**. Extension path — no core edit beyond one registration:

1. **Add** a new encoder/reducer/extract-strategy/compactor, and **register by name** in
   its registry (encoders in `internal/reduce/actions.go`, reducers via
   `reduce.RegisterReducer`, strategies in `internal/extract` `RunExtraction`, compactors
   via `engine.(*Engine).Register`).
2. **List it by name** in the matching list in `configs/lab-cx.yaml` (an empty/omitted list
   means "all built-in defaults"; list order sets priority/run order).

Full detail in [docs/RUNNING.md](docs/RUNNING.md).

## Integrations

- [docs/integration/claude-code.md](docs/integration/claude-code.md) — base-URL swap;
  real runs measured (**−50% tokens** on a large-file task with correctness preserved;
  do-no-harm on a trivial task).
- [docs/integration/bob.md](docs/integration/bob.md) — IBM Bob Shell (OpenAI-compatible
  CLI); real run, **5/5 tasks correct**, cache-injection lever fires on every request.
- [docs/integration/swe-bench.md](docs/integration/swe-bench.md) — proxy before the
  eval-containers gateway; wiring validated, full run is a documented runbook.
- [docs/integration/authbridge.md](docs/integration/authbridge.md) — import the engine
  in-process from a Kagenti AuthBridge plugin (the plugin lives in `kagenti-extensions`).

## Results

Real measured numbers (2026-06-24). See [docs/RESULTS.md](docs/RESULTS.md) for the index.

| Measurement | Result | Source |
|---|---|---|
| `cmdfilter` on verbose command output | **−94%** tokens (reversible) | [RESULTS-offline.md](docs/RESULTS-offline.md) |
| `format` re-encode on structured output | **−35%** best / **−29%** TOON | [RESULTS-offline.md](docs/RESULTS-offline.md) |
| `skeleton` on a code read | **−78%** tokens | [RESULTS-offline.md](docs/RESULTS-offline.md) |
| Full deterministic pipeline, 10 real fixtures | **−93%** aggregate (all reversible) | [RESULTS-offline.md](docs/RESULTS-offline.md) |
| Cheap-model extractor (`claude-haiku-4-5`) | **−56%…−80%** on 4/6 structured fixtures; 2 honest declines; all contained | [RESULTS-extract.md](docs/RESULTS-extract.md) |
| **Claude Code, large-file task (haiku)** | **−50.2%** tokens (34,954 saved), answer correct (42 funcs) | [claude-code.md](docs/integration/claude-code.md) |
| **Bob (haiku), 5-task suite** | **5/5 correct** incl. a file edit; cache injected 6/6; 0 errors | [bob.md](docs/integration/bob.md) |

These are real measured numbers on real tool-output fixtures and live agent runs, with a
real BPE tokenizer. **Claude Code** (self-caching): −50% on a large-file task with
`reduce_cached_prefix`, answer unchanged — and do-no-harm on a trivial task. **Bob**
(non-self-caching): 5/5 tasks correct, the cache-injection lever fires every request. The
end-to-end **SWE-bench** run is a **documented runbook, not yet executed**
([swe-bench.md](docs/integration/swe-bench.md)).

## Repository layout

```
cmd/proxy/        the lab-cx binary (proxy | stats | version)
cmd/labcx-bench/  offline + --extract measurement harness
engine/           Engine: Transform / Expand, Compactor pipeline (builtinCompactors)
surfaces/         anthropic | openai | gemini  wire <-> canonical request
config/           Settings + presets, YAML config loader
canon/            canonical request type
observability/    OpenTelemetry GenAI emitter + /stats aggregator
internal/         tokens, reduce, extract, cheapmodel, cache, markers, store, proxyhttp, treesitter
configs/          example config (lab-cx.yaml)
docs/             RUNNING, RESULTS, integration guides
deploy/           eval-containers wiring
```

## License

Apache-2.0. See [LICENSE](LICENSE).
