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

## Dashboard routes (`--dashboard`)

Present only when the [dashboard](../dashboard.md) is enabled. Without the flag the route
table above is unchanged and every path below returns 404.

| Route | Purpose |
|---|---|
| `GET /dashboard/` | The embedded single-page UI (HTML + CSS + JS from `go:embed`; no CDN, no build step). `/dashboard` redirects here. |
| `GET /api/stats` | Overview aggregates: token tiers, costs, the four labelled savings denominators, the honest-savings waterfall, safety-mechanism costs, and the accounting / cache-miss / uncompressed-reason distributions. Accepts every filter parameter below. |
| `GET /api/series?bucket=<ms>` | Time series, bucketed **at query time** (no rollup tables). One object per bucket with tokens, the four billed tiers, costs, mean latencies, restorations and cache misses. |
| `GET /api/requests` | Paginated request list. Server-side filters + **keyset** pagination (`before=<id>`, not an offset). Returns `{requests, next_cursor, total}`. |
| `GET /api/requests/{id}` | One request with its per-component rows and, for a permitted caller, the before/after content the diff view renders. |
| `GET /api/sessions` | Session list with per-session rollups (`limit` / `offset`). |
| `GET /api/components` | Per-component economics: runs, acted, reverted, unique/gross savings, `overcount_ratio`, total and mean own-latency, errors. |
| `GET /api/facets` | The distinct values present for each filter dimension, so a UI shows only what the data contains. |
| `GET /api/config` | The **effective** (resolved, key-allowlisted) configuration. Access-gated. |
| `GET /api/benchmarks` | Ingested harness runs with per-arm aggregates. `?refresh=1` re-scans the configured run directories. |
| `GET /api/benchmarks/{id}/tasks` | Per-task rows for a run (`?arm=<name>` to restrict). |
| `GET /api/capture` | The capture pipeline's own health, **including its drop count**. |
| `GET /api/events` | SSE stream of captured requests (summary rows only — never content). Honors `Last-Event-ID` (or `?last_event_id=`) so a reconnect backfills the gap. |

### Filter parameters

Accepted by `/api/stats`, `/api/series`, `/api/requests`, `/api/sessions`,
`/api/components` and `/api/facets`. All filtering happens in SQL, server-side.

| Parameter | Matches |
|---|---|
| `since` / `until` | Epoch-**millisecond** bounds (`since` inclusive, `until` exclusive). |
| `session` · `model` · `provider` · `agent` · `preset` · `mode` | Exact match. |
| `component` | Requests on which that component ran. |
| `reason` | The uncompressed-reason bucket (`bypassed`, `below_trigger`, `cache_frozen`, `found_nothing`, `reverted`, `no_messages`), or `compacted` for requests we did compact. |
| `accounting` | `complete` \| `partial` \| `missing`. |
| `q` | Free-text match against session id, model and agent. |
| `limit` · `before` · `offset` | Page size; keyset cursor (`/api/requests`); offset (`/api/sessions`). |

### Access gating

| Surface | Who can see it |
|---|---|
| Aggregates, series, session/component rollups, per-request **metrics** | anyone who can reach the port |
| Per-request **content** (`/api/requests/{id}` content, the diff view) | loopback, or a `--dashboard-trusted-cidrs` entry |
| Effective **configuration** (`/api/config`) | loopback, or a trusted CIDR |

Aggregates stay open on purpose: a proxy bound to `0.0.0.0` should still report its own
numbers. Content is gated because a transcript can carry a user's source code. An
untrusted caller still gets the metrics row, plus `content_visible: false` so the UI can
say *why* the panel is empty rather than implying nothing changed.

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
