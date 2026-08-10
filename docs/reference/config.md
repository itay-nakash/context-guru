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

## Dashboard

The [dashboard](../dashboard.md) is **off by default**. Enabling it adds `/dashboard/` and
`/api/*`; nothing else about the proxy changes.

| Flag / env | Default | Purpose |
|---|---|---|
| `--dashboard` / `DASHBOARD` | off | Enable the persistent dashboard (embedded UI + JSON/SSE API). |
| `--dashboard-db` / `DASHBOARD_DB` | `./context-guru-dashboard.db` | SQLite path. `:memory:` keeps history in RAM only (the no-persistence mode). An unwritable path falls back to in-memory with a warning rather than failing to start. |
| `--dashboard-retention` / `DASHBOARD_RETENTION` | `168h` (7 days) | Drop rows older than this. `0` disables the age rule. |
| `--dashboard-max-bytes` / `DASHBOARD_MAX_BYTES` | `536870912` (512 MiB) | Cap the database size, dropping the oldest requests first. `0` disables the size rule. |
| `--dashboard-content` / `DASHBOARD_CONTENT` | `true` | Capture before/after message text for the diff view. Redacted and size-capped **before** storage. |
| `--dashboard-content-cap` / `DASHBOARD_CONTENT_CAP` | `16384` | Maximum bytes stored per captured before/after blob. |
| `--dashboard-queue` / `DASHBOARD_QUEUE` | `4096` | Capture-channel depth. A full channel **drops** events (counted, and shown in the UI) rather than delaying a request. |
| `--dashboard-trusted-cidrs` / `DASHBOARD_TRUSTED_CIDRS` | — | Comma-separated CIDRs allowed to view per-request **content** and the effective config. Loopback always is; aggregates are open to everyone. |
| `--dashboard-bench-dirs` / `DASHBOARD_BENCH_DIRS` | — | Comma-separated directories of benchmark runs (each with `summary.json` + `rows-*.json`) to ingest at startup. Re-ingesting replaces a run rather than duplicating it. |

!!! note "Retention is bounded by age AND size"
    Age alone cannot bound a burst of traffic; size alone silently erases a quiet week.
    The age rule runs first, then the size rule drops the oldest remaining requests until
    the file fits.

!!! warning "There is deliberately no 'disable observability in production' switch"
    For a tool whose value *is* observability, that would be backwards. What is gated is
    per-request **content** and the effective **configuration** — not the metrics.

### Example (container)

```sh
DASHBOARD=true \
DASHBOARD_DB=/var/lib/context-guru/dashboard.db \
DASHBOARD_RETENTION=720h \
DASHBOARD_MAX_BYTES=2147483648 \
DASHBOARD_TRUSTED_CIDRS=10.0.0.0/8,192.168.0.0/16 \
context-guru-proxy --preset codesmart
```

## Diagnostics

| Env | Effect |
|---|---|
| `CONTEXT_GURU_DEBUG=1` | Logs each tool output's token count + first line. |
| `CONTEXT_GURU_DUMP=<file>` | Appends a before → after JSON record per rewritten message. The [dashboard](../dashboard.md) captures the same material into a queryable store with a diff view. |
| `CONTEXT_GURU_CAPTURE=<file>` | Appends each pristine inbound request as one JSONL record, for offline replay through `/compact`. |
