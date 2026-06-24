# Integrating with Claude Code (real run)

`lab-cx` integrates with Claude Code by the standard base-URL swap: point Claude Code's
`ANTHROPIC_BASE_URL` at the proxy, and the proxy forwards upstream (a provider or, here,
an Anthropic-compatible gateway). No Claude Code changes; it works unmodified.

## How it was actually run (2026-06-24, claude-haiku-4-5 via an Anthropic-compatible gateway)

1. Start the proxy, forwarding to the model endpoint, extraction on:

```sh
./bin/lab-cx proxy --addr 127.0.0.1:8090 --preset balanced \
  --upstream "$ANTHROPIC_BASE_URL" \
  --extract-model claude-haiku-4-5 --extract-provider anthropic \
  --extract-auth bearer --extract-base "$ANTHROPIC_BASE_URL"
```

2. Run Claude Code routed through the proxy. **Precedence note:** if your
   `~/.claude/settings.json` sets `env.ANTHROPIC_BASE_URL`, Claude Code applies it over
   an inherited shell variable — so an inline `ANTHROPIC_BASE_URL=...` is ignored. Route
   it explicitly with a settings file instead:

```sh
# /tmp/cc-settings.json: {"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8090",
#   "ANTHROPIC_AUTH_TOKEN":"<token>","ANTHROPIC_MODEL":"claude-haiku-4-5",
#   "ANTHROPIC_SMALL_FAST_MODEL":"claude-haiku-4-5"}}
claude -p "Read main.go and README.md and summarize each in one line." \
  --settings /tmp/cc-settings.json --dangerously-skip-permissions
```

3. Read the proxy's `/stats`:

```sh
curl -s http://127.0.0.1:8090/stats
```

## Result (verbatim, honest)

The task completed correctly through the proxy. `/stats` after the run:

```json
{ "requests": 2, "tokens_before": 12246, "tokens_after": 12246,
  "tokens_saved": 0, "cache_injected": 0, "extracted": 0,
  "stage_errors": 0, "added_latency_p50_ms": 41, "added_latency_p95_ms": 65 }
```

**Interpretation (do-no-harm, as designed):**
- **Traffic traversed lab-cx** (`requests: 2`) and the agent's task succeeded — the
  integration works.
- **`cache_injected: 0`** — Claude Code is a *self-caching* client (it sends its own
  `cache_control` breakpoints), so lab-cx's cache stage correctly **stands down** rather
  than fight the client's cache. This matches the winnow finding that on Claude Code the
  proxy is cost-neutral by design.
- **`tokens_saved: 0`** — a tiny 2-file, 2-turn task has no large/stale/duplicate tool
  outputs to reduce and nothing over the extraction floor. lab-cx safely did nothing.
- **`added_latency_p50_ms: 41`** — the reduction pass adds ~40ms even when it changes
  nothing (parse + score + render); acceptable, and dwarfed by model latency.

**Where Claude Code savings actually come from** (not exercised by this toy task):
long sessions that re-read large files / re-run noisy commands / accumulate big tool
outputs — there the Reduce pre-passes (cmdfilter, dedup, skeleton, format) and, with
`reduce_cached_prefix` enabled, prefix re-caching kick in. For per-component evidence on
real large outputs see [../RESULTS-offline.md](../RESULTS-offline.md) (deterministic,
−93% aggregate) and [../RESULTS-extract.md](../RESULTS-extract.md) (haiku extractor,
−56%…−80% on structured outputs). A non-self-caching tool-calling agent additionally
gets the cache-injection lever that Claude Code declines.

## Metric note

`/stats` reports `reduction_ratio` = `tokens_saved / tokens_before` (fraction of input
removed; 0 = no savings, higher = more reduction). On this self-caching do-no-harm run
that value is 0, matching `tokens_saved: 0`. (Earlier builds exposed `ratio` as
`after/before`, which read as 1.0 at zero savings — fixed.)
