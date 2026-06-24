# Validate, Configure, Measure & Document Implementation Plan

> **For agentic workers:** Executed via superpowers:subagent-driven-development — fresh subagent per task, review between tasks. Real model runs use the IBM LiteLLM gateway (claude-haiku-4-5) already in the session env.

**Goal:** Prove every component works (unit + real runs), add a generic/extendable config file, measure & expose real token/cost/latency reduction on real fixtures, integrate for real with Claude Code (haiku) and eval-containers/SWE-bench, and document install/run/results.

**Architecture:** Keep public API stable. Add a config-file loader → `config.Settings` + component registry; a measurement harness reusing winnow/headroom/rtk fixtures; a `/stats` aggregation on the proxy. Real model calls go through `cheapmodel` (bearer-auth) and through Claude Code pointed at the proxy.

## Global Constraints

- Go 1.25.0, CGO enabled. `make lint test build` green. DCO sign-off (`git commit -s`), author Osher-Elhadad, no `Co-Authored-By`/"Generated with".
- **Honesty:** real numbers only where actually measured; runbooks (not invented numbers) where a run wasn't executed. Every results doc states exactly how it was produced (model, fixtures, command, date).
- Model: gateway base `ANTHROPIC_BASE_URL` + bearer `ANTHROPIC_AUTH_TOKEN` (in session env; also `/tmp/lcx_env.sh`). Haiku id `claude-haiku-4-5`. **Never echo the token** into logs/commits/docs.
- Fail-open preserved everywhere.

---

### Task 1: cheapmodel bearer auth + header config
**Files:** `internal/cheapmodel/anthropic.go`, `openai.go`, `cheapmodel_test.go`; `cmd/proxy/main.go`.
- Add an `AuthScheme` ("x-api-key" default | "bearer") or auto: if the key looks like a gateway token / a `--extract-auth bearer` flag is set, send `Authorization: Bearer <key>` instead of `x-api-key`. OpenAI already uses Bearer.
- Add `--extract-auth` flag (default "x-api-key") + read base/token from env (`ANTHROPIC_AUTH_TOKEN` fallback for the key).
- Test: httptest asserts the bearer header path sends `Authorization: Bearer`.

### Task 2: Generic, extendable config file
**Files:** `config/file.go` (+ test), `cmd/proxy/main.go`, example `configs/lab-cx.yaml`.
- A declarative config (YAML; use `gopkg.in/yaml.v3`) that maps to `Settings` AND a **component registry**: `stages: [reduce, extract, cache]` (order + enable), `reducers:` (collapse/skeleton/format/dedup/cmdfilter on/off), `encoders:` (json_compact/toon/csv/... on/off + rank), `extract: {mode, strategies:[code,single,rlm,deterministic], floor, provider, model}`.
- The registry is keyed by NAME so adding an extractor/encoder/stage tomorrow = a new registered name + a config entry, no core edit. Document the extension point in a doc comment + RUNNING.md.
- `lab-cx proxy --config configs/lab-cx.yaml`. Unknown keys error; missing keys fall back to preset/defaults.
- Test: load a config that disables extract + enables only TOON; assert resulting Settings + active component set.

### Task 3: Metrics — measure & expose
**Files:** `observability/` (+ a cost rate table), `internal/proxyhttp/proxy.go` (`/stats`), `cmd/proxy` (`lab-cx stats`).
- Aggregate per-request `Report`s into a process-wide stat: requests, tokens_before/after/saved, ratio, by-stage savings, est. cost saved (USD via a small per-model rate table, configurable), p50/p95 added latency.
- Expose `GET /stats` (JSON) + a human summary line; `lab-cx stats` prints the latest. Document the metric definitions.
- Test: feed two Events, assert aggregation + /stats JSON shape.

### Task 4: Fetch headroom/rtk + measurement harness
**Files:** fetch `rtk-ai/rtk` and the headroom source into `../` (or vendor fixtures under `testdata/fixtures/`); `cmd/labcx-bench/main.go` (or `scripts/measure.go`); `testdata/fixtures/*`.
- Pull REAL fixtures: rtk command-output samples (pytest/cargo/npm logs), headroom/winnow MCP-JSON + large file-read + search-result fixtures (mine `../winnow/benchmarks/data`).
- Harness runs each component in isolation AND the full pipeline over each fixture, emitting a table: fixture, component, tokens_before, tokens_after, %saved, bytes, lossless? (containment/expand check), latency.
- Run it for real on the deterministic components; commit the fixtures + the generated `docs/RESULTS-offline.md` table (real numbers).
- Test: harness has a unit test on one fixture asserting reduction > 0 and round-trip recoverable.

### Task 5: Real LLM-extractor measurement (haiku)
**Files:** extend the harness with an `--extract` mode; `docs/RESULTS-extract.md`.
- Source the gateway env, run the extractor (code/single/rlm) with claude-haiku-4-5 over the structured/large fixtures; record tokens_before/after, %saved, strategy chosen, containment-verified, latency, and (sampled) that the result is a faithful subset.
- Write real numbers + the exact command to RESULTS-extract.md. Note cost.

### Task 6: Real Claude Code + haiku integration
**Files:** `docs/integration/claude-code.md`, capture script `scripts/cc-demo.sh`.
- Start `lab-cx proxy` (extract on, haiku, bearer) pointing upstream at the gateway. Run a small real `claude -p "<task>"` with `ANTHROPIC_BASE_URL=http://localhost:PORT` against a throwaway repo; capture the proxy `/stats` before/after (tokens saved, cache hits) and confirm the task still completes.
- Document exact env + commands + the measured reduction. Honest: if Claude Code's auth can't traverse the proxy, document that and fall back to a tool-calling agent demo.

### Task 7: eval-containers SWE-bench (wire + runbook + tiny slice)
**Files:** `deploy/eval-containers/` (already has compose override + README); `docs/integration/swe-bench.md`.
- Finalize the before-gateway wiring so `lab-cx` reduces traffic with claude-code as the agent and claude-haiku-4-5 as the model via the IBM gateway. Build the lab-cx image.
- Write the exact runbook (env, compose command, where token/cost land in result.json/trajectory.jsonl).
- If Docker + time + budget allow, run **1–3 SWE-bench tasks** with and without lab-cx and record the real token/cost delta + resolve status; else clearly mark the runbook as not-yet-executed.

### Task 8: Docs — README + RUNNING + RESULTS
**Files:** `README.md`, `docs/RUNNING.md`, `docs/RESULTS.md` (links the per-area results).
- README: what it is, install (incl. CGO/C toolchain, `make build`), every run mode (proxy, wrap, config file, stats), the three integrations, and a results summary with real numbers.
- RUNNING.md: per-component how-to + the config-extension guide. RESULTS.md: consolidated real measurements + how each was produced + caveats.

---

## Out of scope / caveats
- Full SWE-bench (300+ tasks) — too costly; a tiny slice or runbook only.
- True Claude-token billing accuracy — tiktoken o200k is the offline proxy; gateway usage is authoritative where available.
