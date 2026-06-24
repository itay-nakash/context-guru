# Results index

Every number here is real and reproducible — links to the source docs that hold the
verbatim harness output. No numbers are invented; this page only indexes them.

## Offline deterministic components

- **Headline:** `cmdfilter` **−94%** on verbose command output, `format` re-encode
  **−35%** best / **−29%** TOON on structured output, `skeleton` **−78%** on a code read,
  full deterministic pipeline **−93%** aggregate across 10 real fixtures — every reduction
  reversible, no model in the loop.
- **Date:** 2026-06-24. **How produced:** `CGO_ENABLED=1 go run ./cmd/labcx-bench` over
  the committed `testdata/fixtures/` corpus, `o200k_base` token counts, reversibility
  verified per row.
- **Full table + analysis:** [RESULTS-offline.md](RESULTS-offline.md).

## Cheap-model extractor (online)

- **Headline:** `claude-haiku-4-5` extractor reduced **4 of 6** structured fixtures
  **−56%…−80%** (via `code`/`single`/`deterministic` strategies), with **2 honest
  declines** shown as `strategy=none`; every spliced result is `contained=yes` (lossless +
  reversible).
- **Date:** 2026-06-24. **How produced:** `CGO_ENABLED=1 go run ./cmd/labcx-bench
  --extract` against a live Anthropic-compatible gateway (`LLMCompactFloor=200` for the
  small fixtures), real network round-trips, no mocks.
- **Full table + honest analysis:** [RESULTS-extract.md](RESULTS-extract.md).

## Claude Code integration (real run)

- **Headline:** task completed correctly through the proxy; `cache_injected: 0` /
  `tokens_saved: 0` — **do-no-harm on a self-caching client, as designed** (lab-cx stands
  down rather than fight Claude Code's own `cache_control`).
- **Date:** 2026-06-24. **How produced:** `claude -p` routed through `lab-cx proxy` via a
  settings file, reading `/stats` before/after (`scripts/cc-demo.sh`).
- **Detail:** [integration/claude-code.md](integration/claude-code.md).

## SWE-bench / eval-containers integration

- **Headline:** wiring **validated** — the merged compose renders `winnow` before the
  gateway and the lab-cx image builds (CGO, distroless/base). The **full SWE-bench run was
  NOT executed** this session (multi-GB pulls + real model spend); the doc is the runbook
  to execute, with no fabricated benchmark numbers.
- **Date:** 2026-06-24. **How produced:** structural `docker compose ... config` merge
  validation (no image pull).
- **Detail + runbook:** [integration/swe-bench.md](integration/swe-bench.md).
