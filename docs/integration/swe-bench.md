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
# -> otelcol, gateway, lab-cx, runner   (lab-cx inserted before the gateway)
```

The override adds a `lab-cx` service on the `internal` network, repoints the runner's
`ANTHROPIC_BASE_URL` (`http://gateway:4000/anthropic`) to `http://lab-cx:8080/anthropic`,
and sets `lab-cx --upstream http://gateway:4000`. The agent still holds only `sk-proxy`;
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
   `lab-cx --upstream` directly to it (bypassing the eval gateway) — lab-cx forwards the
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
   - **lab-cx**: `curl http://lab-cx:8080/stats` (tokens_before/after/saved, cache,
     extracted, added latency) — run from inside the compose network, or expose the port.
   - Compare with-vs-without lab-cx on the same `EVAL_TASK_ID` for the token/cost delta;
     `result.json` resolve status confirms accuracy is preserved.

## Results (10 real tasks, 2026-06-25)

Ten SWE-bench Verified tasks run Claude Code (`claude-sonnet-4-6`) → `lab-cx` → gateway,
with the **deterministic reducers + extractor on the cached prefix** (`--reduce-cached-prefix`)
and the extractor pinned to `claude-haiku-4-5`. Per-task input-token reduction from
`lab-cx`'s `/stats`, resolve from `result.json`:

| task | reqs | tokens before | tokens after | saved | %    | blocks | resolve |
|------|-----:|--------------:|-------------:|------:|-----:|-------:|---------|
| astropy-12907 |  16 |   210,638 |   101,472 |   109,166 | 51.8 |    — | passed |
| astropy-13033 |  29 |   659,220 |   573,936 |    85,284 | 12.9 |  133 | —      |
| astropy-13236 |  45 |   416,300 |   237,675 |   178,625 | 42.9 |  350 | not resolved |
| astropy-13398 |  28 |   792,753 |   401,839 |   390,914 | 49.3 |  176 | not resolved |
| astropy-13453 |  41 | 1,129,296 |   567,732 |   561,564 | 49.7 |  425 | passed |
| astropy-13579 |   9 |    52,283 |    26,409 |    25,874 | 49.5 |    7 | not resolved |
| astropy-13977 |  27 |   354,322 |   282,680 |    71,642 | 20.2 |  154 | not resolved |
| astropy-14096 |  19 |   259,925 |   223,951 |    35,974 | 13.8 |   58 | passed |
| astropy-14182 |  36 |   452,919 |   354,583 |    98,336 | 21.7 |  286 | passed |
| astropy-14309 |  10 |    78,165 |    70,019 |     8,146 | 10.4 |    9 | passed |
| **TOTAL** | **263** | **4,405,821** | **2,840,296** | **1,565,525** | **35.5** | **1,598** | |

- **Reduction is real and substantial: −35.5% aggregate** (−10.4%…−51.8% per task), all
  from the deterministic reducers (1,598 blocks). 0 stage errors; p95 added latency
  ~130–250 ms.
- `extract_candidates=0` on every task: these astropy tasks emit no large *structured*
  tool outputs, so the LLM extractor had nothing to claim — its value is the separate
  −56%…−80% on structured fixtures ([../RESULTS-extract.md](../RESULTS-extract.md)).
- **Honest caveats:** runs used the **arm64** Epoch bases for native Apple-Silicon
  execution; Epoch marks arm64 grading best-effort/untested, so `not resolved` rows may
  understate accuracy (token reduction is architecture-independent and valid). 12907/13033
  predate per-task `result.json` capture. `--reduce-cached-prefix` trades the client's
  prompt-cache hits for these reductions.

### How these were produced (reproduce)

Most of the 500 `tasks.txt` instances have **no prebuilt** `evals/<task>--claude-code`
image; only a demo (12907) is published. Build per-task images locally from the **public**
Epoch base, then stitch with the agent:

```sh
# 1. benchmark image (public Epoch base; arm64 for native Apple Silicon, x86_64 otherwise)
docker build --platform linux/arm64 -f containers/benchmarks/swe-bench/Dockerfile \
  --build-arg EVAL_TASK_ID=<task> --build-arg EVAL_BASE_ARCH=arm64 \
  --build-context "ghcr.io/exgentic/core/entrypoint=docker-image://ghcr.io/exgentic/core/entrypoint:latest" \
  -t localhost:5001/swe-bench-<task>:latest containers/benchmarks/swe-bench
docker push localhost:5001/swe-bench-<task>:latest   # local registry: BuildKit resolves FROM via a registry
# 2. stitch benchmark + agent + gosu into the runner image
docker build --platform linux/arm64 -f containers/core/combination.Dockerfile \
  --build-arg BENCHMARK_IMAGE=localhost:5001/swe-bench-<task>:latest \
  --build-arg AGENT_IMAGE=ghcr.io/exgentic/agents/claude-code:latest \
  --build-arg GOSU_IMAGE=ghcr.io/exgentic/core/gosu:latest \
  -t ghcr.io/exgentic/evals/swe-bench-<task>--claude-code:latest containers/core
```

Gotchas learned: (a) build for the host arch — an amd64-only image won't run `node`
without emulation on arm64; (b) Claude Code 2.1.x sends adaptive **thinking** that some
gateways reject for haiku — run the agent on a thinking-capable model
(`ANTHROPIC_MODEL=claude-sonnet-4-6`) or cap `ANTHROPIC_MODEL_SUPPORTED_CAPABILITIES=effort`;
(c) the eval gateway force-maps to `EVAL_MODEL`, so to keep the extractor on haiku point
`--extract-base` at the model endpoint directly (the override does this).

## Note for self-caching agents

Claude Code self-caches, so by default lab-cx defers to its `cache_control` (cache stage
stands down) and reduces little. To run the **Reduce** pre-passes
(cmdfilter/dedup/skeleton/format) and the **extractor** on the cached prefix anyway — the
configuration that produced the results above — pass `--reduce-cached-prefix` (or
`reduce.reduce_cached_prefix: true`). This re-reduces (and so invalidates) the client's
cached prefix: you trade its cheap cached-token reads for the reductions, which is the
right call when measuring the components or when reduction matters more than the client
cache.
