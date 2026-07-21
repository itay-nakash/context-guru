# Routes & headers

The proxy serves both provider dialects on one port (default `:4000`).

## Routes

| Route | Purpose |
|---|---|
| `POST /openai/v1/chat/completions` | OpenAI chat dialect — runs the pipeline, forwards to the OpenAI upstream. |
| `POST /anthropic/v1/messages` | Anthropic Messages dialect — runs the pipeline, forwards to the Anthropic upstream. |
| `GET /healthz` | Liveness check. |
| `GET /stats` | Savings rollups (token-weighted `Σ saved / Σ before`, plus `wasted_tokens`/`bounces` and per-component breakdown). |
| `GET /expand?id=` | Recover an offloaded original by its `<<cg:HASH>>` id. |

!!! note "`POST /compact` (compaction-service mode)"
    The [llm-d compaction service example](../examples/llm-d-service.md) adds a
    stateless `POST /compact` route: it runs the pipeline and returns the
    rewritten body directly (`200` + JSON) with no upstream call, no store, and
    no markers. See [Quickstart: Compaction service](../get-started/quickstart-compaction.md).

## Per-request headers

| Header | Effect |
|---|---|
| `x-context-guru-session: <id>` | Sets the session key explicitly. Otherwise a stable content hash (`sha256(system + firstUser)`) keys the session. |
| `x-context-guru-bypass: true` | Skips the pipeline entirely for this request (tokens unchanged). |
| `x-context-guru-pipeline: <a,b,c>` | Runs exactly these components, in order, for this request. |

See [Config & environment](config.md) for flags and env vars, and
[Presets](presets.md) for the built-in pipelines.
