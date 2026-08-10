# Measure savings

context-guru reports what it actually saved through the proxy's `GET /stats` endpoint, backed by
an in-process metrics aggregator. Savings are measured on **message content text** — what the model
reads — not the JSON envelope, so a control directive like a `cache_control` breakpoint never looks
"worse".

## `GET /stats`

The proxy exposes `GET /stats` with in-process savings rollups. Savings are **token-weighted**
(Σ saved / Σ before) — the honest aggregate, not a mean of per-request percentages. It also reports:

| Field | Meaning |
|---|---|
| token-weighted savings | Σ saved / Σ before across all requests |
| `wasted_tokens` | content offloaded then re-served via expand (a premature offload) |
| `bounces` | how many offloads were re-served (the count behind `wasted_tokens`) |
| `adjusted_saved` | `saved − wasted` — bounce-adjusted, may be negative |
| `top_passthrough` | components that ran but never changed a request: dead weight to drop |
| `top_discarded` | components whose changes the **writeback layer threw away** — they mutated but never reached the wire. Always worth investigating. |
| `saved_tokens_unique` / `overcount_ratio` | distinct compactions, and how many times each was re-counted. Prefer the unique figure: the agent re-sends history verbatim every turn, so the cumulative `saved_tokens` is inflated. |
| `mode` | the operating mode these numbers came from: `sync` \| `observe` |
| `sync_enforced` | requests whose forwarded body context-guru actually shaped. **0 in observe mode by construction.** |

!!! tip "Reading top_passthrough"
    A component in `top_passthrough` isn't necessarily broken. `cachesplit` always lands there —
    its saving is a provider-side KV-cache hit, invisible to content-token counts, and the
    component itself deliberately always skips (the rewrite is body-level). But a
    content-offloader that never fires is a candidate to drop from your pipeline.

!!! warning "`top_discarded` is never expected"
    An entry in `top_discarded` means a component ran, mutated the request, and the writeback
    layer threw the change away before it reached the wire. Unlike `top_passthrough` this is
    always worth investigating: it is exactly the signature that hid the `cacheinject` bug for
    two whole benchmark studies, because a mutated-then-discarded component looks byte-identical
    to a working Reformat. Check the per-component `discarded_changes` count.

!!! warning "Enforced vs hypothetical"
    Everything above is what context-guru **actually did**. In
    [observe mode](operating-modes.md#observe-measure-without-enforcing) nothing is applied,
    so every savings field above reads zero and the numbers appear instead under
    `potential_*` / `projected_*`, alongside an `observe_notice` banner. The two
    vocabularies never share a key: a hypothetical cannot be summed into a real saving even
    by accident. Two enforced keys stay deliberately real there — `cg_added_ms_avg` (the
    actual enforced-path latency, ~0, which is the point) and context-guru's own model
    spend, labelled by `observe_llm_notice` as the cost of measuring rather than enforcing.

## The Emitter interface

The pipeline depends only on the `Emitter` interface (`Component(Report)` + `Run(RunReport)`), so it
carries no telemetry-backend dependency. Swap implementations to route metrics where you need:

| Emitter | Role |
|---|---|
| `Slog` | logs in the `context_engineering.*` vocabulary |
| `Aggregator` | in-process rollups behind `/stats` |
| `Tee` | fan-out to several emitters |
| `NopEmitter` | discard |

## A real session: `scripts/cc-demo.sh`

`scripts/cc-demo.sh` routes a real `claude` CLI session through the proxy and reads `/stats` before
and after. It builds a tiny repo, starts the proxy with `--preset balanced`, points Claude Code's
`ANTHROPIC_BASE_URL` at it, runs one `claude -p` task, and prints the stats delta:

```sh
export ANTHROPIC_BASE_URL=...            # upstream Anthropic-compatible endpoint
export ANTHROPIC_AUTH_TOKEN=...
scripts/cc-demo.sh
# == stats before ==  {...}
# (claude reads main.go + README.md through the proxy)
# == stats after ==   {...}   ← token-weighted savings for the session
```

It's the shortest way to see real savings on your own model without a full benchmark harness.

## Benchmarks

For the full per-component SWE-bench evaluation — where `mask` delivers ~27% content-token savings
with no reward loss, and how the `/stats` within-run metric is derived — see
[Benchmarks](../RESULTS.md).
