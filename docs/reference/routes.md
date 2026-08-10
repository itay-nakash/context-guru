# Routes & headers

The proxy serves both provider dialects on one port (default `:4000`).

## Routes

| Route | Purpose |
|---|---|
| `POST /openai/v1/chat/completions` | OpenAI chat dialect — runs the pipeline, forwards to the OpenAI upstream. |
| `POST /anthropic/v1/messages` | Anthropic Messages dialect — runs the pipeline, forwards to the Anthropic upstream. |
| `POST /compact` | Stateless compaction: run the pipeline and return the rewritten body, no upstream call. `?provider=anthropic` switches dialect; `?preset=` / `x-context-guru-pipeline` override the pipeline; `?cache=on\|off\|auto` overrides cache-awareness. |
| `GET /healthz` | Liveness check. |
| `GET /stats` | Savings rollups and health counters — see below. |
| `GET /expand?id=` | Recover an offloaded original by its `<<cg:HASH>>` id. Scoped to the caller's session. |

!!! note "`POST /compact` (compaction-service mode)"
    The [llm-d compaction service example](../examples/llm-d-service.md) uses this route with
    the store disabled and `marker_mode: off`, so the returned body is clean, marker-free and
    directly usable. See [Quickstart: Compaction service](../get-started/quickstart-compaction.md).

## `GET /stats`

Fields are only ever **added** to this payload (harnesses in `deploy/harbor` parse it), so a
consumer that reads by key keeps working. The table below is the `Snapshot` struct in
`metrics/metrics.go`, complete.

### Savings

| Field | Meaning |
|---|---|
| `requests` | Enforced requests aggregated. |
| `tokens_before` / `tokens_after` | Content-token totals before and after the pipeline. |
| `saved_tokens` / `savings_pct` | `before − after`, and the **token-weighted** ratio (`Σ saved / Σ before`), not a mean of per-request percentages. |
| `wasted_tokens` | Content offloaded then re-served via `expand` — a premature offload. |
| `bounces` | How many expand events produced that waste. |
| `adjusted_saved` | `saved − wasted`. May be negative. |
| `components` | Per-component rollup (see below). |
| `top_passthrough` | Components that ran but never *changed* a request — dead weight. A component that mutated without saving content tokens (`cachesplit`, `cacheinject`) is **not** listed here. |
| `top_discarded` | Components whose changes the **writeback layer threw away** at least once. Any entry needs investigating: the component ran, mutated, and had no effect on the wire. |

Per-component (`components.<name>` and `potential_components.<name>`):

| Field | Meaning |
|---|---|
| `runs` | Times the component ran. |
| `acted` | Runs that actually saved tokens. |
| `mutated` | Runs that changed the request at all — may save 0 content tokens. |
| `reverted` | Runs the pipeline rolled back (error, panic, or grew the request). |
| `saved_tokens` | **Cumulative** — re-counted every turn the compaction re-appears. |
| `saved_tokens_unique` | **Unique** — each distinct compaction counted once, deduped by content key. |
| `overcount_ratio` | `saved_tokens / saved_tokens_unique`. ~1.0 is honest; a large value means the cumulative figure is inflated by the agent re-sending history verbatim. |
| `duration_ms` | Cumulative wall time this component spent on the hot path. |
| `discarded_changes` | Changes the writeback layer threw away, attributed back to this component. |

!!! warning "Cumulative is not unique"
    `saved_tokens` counts the same compaction again on every later turn that carries it. A
    figure like "4.8M tokens saved" is a *cumulative* total; the unique figures behind the
    Terminal-Bench and SWE-bench runs are **234,119 tokens behind 103 markers** and
    **15,457 behind 29** — 21× and 8× smaller respectively. Quote
    `saved_tokens_unique`, and check `overcount_ratio` before citing either.

### Context-guru's own cost

| Field | Meaning |
|---|---|
| `llm_calls`, `llm_input_tokens`, `llm_output_tokens` | The cheap-model spend context-guru's *own* components incurred (`extract_llm`, `summarize`). Separate from the agent's spend; priced externally. |
| `cg_added_ms_avg` | Mean ms context-guru added per request (normalize + pipeline + writeback). |
| `upstream_ms_avg` | Mean provider round-trip on the active path. |
| `upstream_ms_avg_bypassed` | Same on `x-context-guru-bypass` requests — the baseline for a with/without latency comparison. |

### SSE streaming health

Buffering a stream is the one thing that stops it being a stream, so it is counted. All four
fields count **once per client request**, not per upstream round: a request that drove several
expand rounds waited for all of them.

| Field | Meaning |
|---|---|
| `sse_streamed` | Streaming responses passed straight through — the fast path. |
| `sse_buffered` | Streaming responses read in full before the client saw a byte, because the request carried a marker that might produce an expand call. |
| `sse_buffered_pct` | `buffered / (streamed + buffered) × 100`. |
| `sse_ttfb_ms_avg` | Real time-to-first-byte, streamed-through requests only. |
| `sse_ttfb_ms_avg_buffered` | Time-to-**last**-byte by construction — a buffered response is read in full before the client is written to, so its first byte cannot precede the buffer completing. Read it as "what buffering cost these requests", **not** as a latency comparable to `sse_ttfb_ms_avg`. |

A high `sse_buffered_pct` on traffic that never expands is the regression to watch: the marker
check used to match the expand tool's own description and so buffered **every** stream.

### Freeze-replay health

The cache-**write** cost line. A frozen decision replayed (`frozen_hits`) keeps an
already-cached message byte-identical. One the store **drops** would flip that message's
representation inside the provider's cached prefix and force the whole suffix to be re-written
at 11.5× the read price — unless it is re-derived.

| Field | Meaning |
|---|---|
| `frozen_hits` | Replay lookups that found a stored decision and re-sent the same bytes. |
| `frozen_misses` | Replay lookups that found nothing. **Dominated by the ordinary "never frozen yet" case** — it is a lookup counter, not an error counter. Read it beside `frozen_dropped`, not instead of it. |
| `frozen_dropped` | Stored decisions the store actually **lost** (TTL expiry or the pin cap). Counted per drop *event*. |
| `frozen_repaired` | Dropped decisions re-derived, so a replay lands again. Only `mask` and `failed_run` qualify — `extract_llm` is deliberately excluded (its replacement is a *sampled* model output, so re-deriving could splice different bytes into the cached prefix). |
| `frozen_flips` | `frozen_dropped − frozen_repaired` — outstanding losses, i.e. drops that plausibly cost a suffix cache-write. **Should be 0.** |

A healthy long-horizon run shows `frozen_hits` climbing with turn count and `frozen_dropped` at
0; a rising `frozen_dropped` means decisions are dying mid-session (TTL too short for the task,
or the entry cap too small for the session's working set).

### cmdfilter attribution

| Field | Meaning |
|---|---|
| `cmdfilter_families` | Per command family (`builds` / `tests` / `iac` / `pkg` / `net` / `other`): `acts`, `saved_tokens`, `saved_tokens_unique`. |
| `cmdfilter_filters` | The same, per individual filter — which filters actually earn their place. |
| `cmdfilter_selector_misses` | Output shapes that matched **no** filter, frequency-ranked. The backlog of filters worth writing. Bounded at 200 distinct selectors, first-seen wins. |

### Operating mode

| Field | Meaning |
|---|---|
| `mode` | The configured operating mode: `sync` \| `observe`. |
| `sync_enforced` | Requests whose forwarded body context-guru actually shaped. **0 in observe mode by construction** — the machine-readable form of "context-guru did not modify requests". |

Observe mode's results are **hypotheticals** and live under keys that never collide with an
enforced metric, so a consumer cannot sum one into a real saving even by accident. All zero
outside observe mode.

| Field | Meaning |
|---|---|
| `observe_notice` | The banner. Present whenever hypotheticals are reported. |
| `observe_hypothetical_requests` | Requests observed off-path. |
| `actual_baseline_tokens` | What the agent really sent. Actual, not hypothetical. |
| `projected_optimized_tokens` | What it would have sent under this pipeline. |
| `potential_saved_tokens` / `potential_savings_pct` | The difference, and its ratio. |
| `potential_components` | Per-component hypothetical contributions (same shape as `components`). |
| `potential_overhead_ms_avg` | What compaction *would* have added per request — measured off-path, so it is what `sync` would cost, not what `observe` costs. |
| `observe_llm_notice` | Warns that `llm_calls` / `llm_*_tokens` in observe mode are the cost of **measuring** off-path, not of enforcing. The spend is real (not hypothetical), so it stays where cost tooling reads it, labelled. |

`cg_added_ms_avg` and the `llm_*` fields deliberately **do** accumulate in observe mode: they
are real measurements and real spend. Zeroing them would hide a true number rather than protect
anyone. See [Operating modes](../how-to/operating-modes.md).

## Per-request headers

| Header | Effect |
|---|---|
| `x-context-guru-session: <id>` | Sets the session key explicitly. Otherwise a stable content hash (`sha256(system + firstUser)`) keys the session. |
| `x-context-guru-bypass: true` | Skips the pipeline entirely for this request (tokens unchanged). |
| `x-context-guru-pipeline: <a,b,c>` | Runs exactly these components, in order, for this request. |

See [Config & environment](config.md) for flags and env vars, and
[Presets](presets.md) for the built-in pipelines.
