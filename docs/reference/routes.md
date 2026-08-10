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

### `/stats` freeze-replay fields

Fields are only ever **added** to this payload (harnesses in `deploy/harbor` parse it), so
a consumer that reads by key keeps working.

| Field | Meaning |
|---|---|
| `frozen_hits` | Replay lookups that found a stored decision and re-sent the same bytes. |
| `frozen_misses` | Replay lookups that found nothing. **Dominated by the ordinary "not compacted yet" case** — it is a lookup counter, not an error counter. Read `frozen_dropped` for harm. |
| `frozen_dropped` | Stored decisions the store actually **lost** (TTL expiry or eviction). Each is a chance for an already-cached message to flip representation. Counted per drop *event*. |
| `frozen_repaired` | Dropped decisions later restored, so a replay can land again. |
| `frozen_flips` | `frozen_dropped − frozen_repaired` — outstanding losses, i.e. drops that plausibly cost a suffix cache-write. **Should be 0.** |

A healthy long-horizon run shows `frozen_hits` climbing with turn count and
`frozen_dropped` at 0; a rising `frozen_dropped` means decisions are dying mid-session
(TTL too short for the task, or the entry cap too small for the session's working set).

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
