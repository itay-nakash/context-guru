# Integrating with eval-containers (SWE-bench)

[eval-containers](https://github.com/exgentic/eval-containers) runs `one benchmark + one
agent + one model`, with all agent→model traffic flowing through a **gateway** on port
4000. `lab-cx` inserts as a plain proxy *before* that gateway, so it sees and reduces
exactly what the agent sends — here with **claude-code** as the agent and
**claude-haiku-4-5** as the model.

## Wiring (validated)

The before-gateway placement is [`deploy/eval-containers/compose.override.yaml`](../../deploy/eval-containers/compose.override.yaml).
Merging it with the real swe-bench compose was validated structurally (no image pull):

```sh
cd eval-containers/containers/benchmarks/swe-bench
EVAL_TASK_ID=astropy__astropy-12907 EVAL_AGENT=claude-code EVAL_MODEL=anthropic/claude-haiku-4-5 \
OPENAI_API_BASE=... OPENAI_API_KEY=... \
docker compose -f compose.yaml -f /path/to/lab-context-engineering/deploy/eval-containers/compose.override.yaml \
  config --services
# -> otelcol, gateway, winnow, runner   (winnow inserted before the gateway)
```

The override adds a `winnow` service on the `internal` network, repoints the runner's
`ANTHROPIC_BASE_URL` (`http://gateway:4000/anthropic`) to `http://winnow:8080/anthropic`,
and sets `winnow --upstream http://gateway:4000`. The agent still holds only `sk-proxy`;
the real key stays in the gateway. lab-cx's suffix routing reduces the `/anthropic/...`
prefixed path and forwards the full path on.

## Runbook (full run)

1. Build the lab-cx image (CGO; final image is `distroless/base`):
   ```sh
   cd lab-context-engineering && docker build -t ghcr.io/kagenti/lab-context-engineering:latest .
   ```
2. Point the eval **gateway** at your model provider. For a standard provider set
   `OPENAI_API_BASE`/`OPENAI_API_KEY` + `EVAL_MODEL` per the eval-containers docs. For an
   **Anthropic-compatible gateway** (e.g. the IBM LiteLLM endpoint used in this repo's
   other tests), either configure the eval gateway's provider to that endpoint, or set
   `winnow --upstream` directly to it (bypassing the eval gateway) — winnow forwards the
   agent's `Authorization` header through. Use `--extract-auth bearer` for bearer-token
   gateways.
3. Run a task (or a slice from `tasks.txt`) with and without the override and compare:
   ```sh
   # with lab-cx
   EVAL_TASK_ID=astropy__astropy-12907 EVAL_AGENT=claude-code EVAL_MODEL=anthropic/claude-haiku-4-5 \
   docker compose -f compose.yaml -f .../compose.override.yaml up -y --abort-on-container-exit
   ```
4. Read the metrics:
   - **eval-containers**: `result.json` (resolve status) + `trajectory.jsonl` (per-call
     `total_tokens`, `cost_usd`) in the shared `output` volume.
   - **lab-cx**: `curl http://winnow:8080/stats` (tokens_before/after/saved, cache,
     extracted, added latency) — run from inside the compose network, or expose the port.
   - Compare with-vs-without lab-cx on the same `EVAL_TASK_ID` for the token/cost delta;
     `result.json` resolve status confirms accuracy is preserved.

## Status (honest)

- **Wiring: validated** — the merged compose renders correctly (`winnow` before
  `gateway`) and the lab-cx image builds (CGO, distroless/base).
- **Full SWE-bench run: NOT executed in this session.** It requires multi-GB image
  pulls, configuring the eval gateway against the model provider, and real model spend +
  long agent-loop runtime — out of scope for a single session, and we do not fabricate
  benchmark numbers. The runbook above is what to execute.
- The measured evidence we DO have is real and reproducible: deterministic components
  −93% aggregate on real fixtures ([../RESULTS-offline.md](../RESULTS-offline.md)) and
  the haiku extractor −56%…−80% on structured outputs
  ([../RESULTS-extract.md](../RESULTS-extract.md)), plus a real Claude Code run through
  the proxy ([claude-code.md](claude-code.md)). SWE-bench is the end-to-end validation to
  run next with a provisioned gateway.

## Note for self-caching agents

Claude Code self-caches, so on SWE-bench lab-cx's biggest lever is the **Reduce**
pre-passes (cmdfilter/dedup/skeleton/format) and the **extractor** on large tool
outputs, not cache injection (it defers — see [claude-code.md](claude-code.md)). Enable
`reduce_cached_prefix` in the config to also re-cache a smaller prefix on self-caching
clients.
