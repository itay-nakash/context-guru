# Offline measurement results (deterministic, no model)

- **Date measured:** 2026-06-24
- **Mode:** OFFLINE / deterministic only. The cheap-model Extract stage is **OFF** for
  every row here; nothing in this document calls a model. Token reduction comes entirely
  from the deterministic components (command-output filter, format re-encoder, code
  skeletonizer, reversible collapse) and the full deterministic pipeline.
- **Tokenizer:** `o200k_base` (the modern GPT-family BPE), via `internal/tokens` — the
  same counter the engine uses to gate reductions. Counts are real BPE token counts, not
  a chars/4 estimate.
- **Fixtures:** 10 real tool outputs committed under `testdata/fixtures/` (rtk command
  outputs + reference-prototype structured tool outputs). Provenance for every file is in
  `testdata/fixtures/README.md`.

## Reproduce

```sh
# from the repo root
CGO_ENABLED=1 go run ./cmd/labcx-bench            # the table below
CGO_ENABLED=1 go run ./cmd/labcx-bench --json     # machine-readable rows
```

The harness wraps each fixture as a canonical Anthropic-shaped request (a `tool_result`
block, paired with the `tool_use` that produced it so command/file-aware passes can see
the command or path), places it outside the protect-recent window, and runs the engine
with **one component enabled at a time** plus the **full deterministic pipeline**. For
every row it verifies the reduction is **reversible** — either it is a no-op, or the
original is recoverable from the rewind store via `engine.FindMarkers` + `engine.Expand`.

## Measured table (verbatim harness output)

The numbers below are pasted directly from `go run ./cmd/labcx-bench`. Rows tagged
`(no-op)` are components that did not apply to that fixture (e.g. the skeletonizer on a
JSON array); they are kept in the table to show the fail-safe — a component never inflates
a fixture it does not understand.

| fixture | component | tokens_before | tokens_after | %saved | bytes_before | bytes_after | reversible | added_latency_ms |
| --- | --- | ---: | ---: | ---: | ---: | ---: | :---: | ---: |
| pytest_failures | cmdfilter (no-op) | 94 | 94 | 0.00 | 324 | 324 | yes | 13.065 |
| pytest_failures | format_toon (no-op) | 94 | 94 | 0.00 | 324 | 324 | yes | 0.625 |
| pytest_failures | format_best (no-op) | 94 | 94 | 0.00 | 324 | 324 | yes | 0.615 |
| pytest_failures | skeleton (no-op) | 94 | 94 | 0.00 | 324 | 324 | yes | 0.521 |
| pytest_failures | collapse (no-op) | 94 | 94 | 0.00 | 324 | 324 | yes | 0.520 |
| pytest_failures | pipeline_full (no-op) | 94 | 94 | 0.00 | 324 | 324 | yes | 0.979 |
| cargo_test_failure | cmdfilter (no-op) | 95 | 95 | 0.00 | 292 | 292 | yes | 0.983 |
| cargo_test_failure | format_toon (no-op) | 95 | 95 | 0.00 | 292 | 292 | yes | 0.569 |
| cargo_test_failure | format_best (no-op) | 95 | 95 | 0.00 | 292 | 292 | yes | 0.568 |
| cargo_test_failure | skeleton (no-op) | 95 | 95 | 0.00 | 292 | 292 | yes | 0.500 |
| cargo_test_failure | collapse (no-op) | 95 | 95 | 0.00 | 292 | 292 | yes | 0.484 |
| cargo_test_failure | pipeline_full (no-op) | 95 | 95 | 0.00 | 292 | 292 | yes | 0.949 |
| cargo_build | cmdfilter | 2687 | 155 | 94.23 | 6743 | 409 | yes | 11.652 |
| cargo_build | format_toon (no-op) | 2687 | 2687 | 0.00 | 6743 | 6743 | yes | 7.597 |
| cargo_build | format_best (no-op) | 2687 | 2687 | 0.00 | 6743 | 6743 | yes | 7.866 |
| cargo_build | skeleton (no-op) | 2687 | 2687 | 0.00 | 6743 | 6743 | yes | 7.736 |
| cargo_build | collapse | 2687 | 54 | 97.99 | 6743 | 133 | yes | 7.667 |
| cargo_build | pipeline_full | 2687 | 155 | 94.23 | 6743 | 409 | yes | 10.966 |
| flights_search | cmdfilter (no-op) | 607 | 607 | 0.00 | 1356 | 1356 | yes | 2.349 |
| flights_search | format_toon | 607 | 291 | 52.06 | 1356 | 535 | yes | 3.353 |
| flights_search | format_best | 607 | 263 | 56.67 | 1356 | 504 | yes | 4.178 |
| flights_search | skeleton (no-op) | 607 | 607 | 0.00 | 1356 | 1356 | yes | 2.588 |
| flights_search | collapse | 607 | 55 | 90.94 | 1356 | 133 | yes | 2.282 |
| flights_search | pipeline_full | 607 | 55 | 90.94 | 1356 | 133 | yes | 2.285 |
| users_directory | cmdfilter (no-op) | 194 | 194 | 0.00 | 533 | 533 | yes | 0.855 |
| users_directory | format_toon | 194 | 140 | 27.84 | 533 | 382 | yes | 1.228 |
| users_directory | format_best | 194 | 124 | 36.08 | 533 | 362 | yes | 1.577 |
| users_directory | skeleton (no-op) | 194 | 194 | 0.00 | 533 | 533 | yes | 0.860 |
| users_directory | collapse (no-op) | 194 | 194 | 0.00 | 533 | 533 | yes | 0.831 |
| users_directory | pipeline_full | 194 | 124 | 36.08 | 533 | 362 | yes | 2.756 |
| products_inventory | cmdfilter (no-op) | 254 | 254 | 0.00 | 643 | 643 | yes | 1.140 |
| products_inventory | format_toon | 254 | 147 | 42.13 | 643 | 345 | yes | 1.677 |
| products_inventory | format_best | 254 | 136 | 46.46 | 643 | 323 | yes | 1.998 |
| products_inventory | skeleton (no-op) | 254 | 254 | 0.00 | 643 | 643 | yes | 1.097 |
| products_inventory | collapse | 254 | 50 | 80.31 | 643 | 133 | yes | 1.124 |
| products_inventory | pipeline_full | 254 | 50 | 80.31 | 643 | 133 | yes | 1.150 |
| oc_pods_slice | cmdfilter (no-op) | 811 | 811 | 0.00 | 3216 | 3216 | yes | 3.836 |
| oc_pods_slice | format_toon | 811 | 511 | 36.99 | 3216 | 1791 | yes | 5.226 |
| oc_pods_slice | format_best | 811 | 511 | 36.99 | 3216 | 1791 | yes | 5.721 |
| oc_pods_slice | skeleton (no-op) | 811 | 811 | 0.00 | 3216 | 3216 | yes | 3.837 |
| oc_pods_slice | collapse | 811 | 50 | 93.83 | 3216 | 133 | yes | 3.208 |
| oc_pods_slice | pipeline_full | 811 | 50 | 93.83 | 3216 | 133 | yes | 3.556 |
| glab_issue_list | cmdfilter (no-op) | 2106 | 2106 | 0.00 | 6748 | 6748 | yes | 8.076 |
| glab_issue_list | format_toon | 2106 | 1992 | 5.41 | 6748 | 6482 | yes | 14.328 |
| glab_issue_list | format_best | 2106 | 1799 | 14.58 | 6748 | 6180 | yes | 17.478 |
| glab_issue_list | skeleton (no-op) | 2106 | 2106 | 0.00 | 6748 | 6748 | yes | 7.020 |
| glab_issue_list | collapse | 2106 | 54 | 97.44 | 6748 | 133 | yes | 6.747 |
| glab_issue_list | pipeline_full | 2106 | 54 | 97.44 | 6748 | 133 | yes | 7.337 |
| glab_mr_list | cmdfilter (no-op) | 2934 | 2934 | 0.00 | 9463 | 9463 | yes | 10.454 |
| glab_mr_list | format_toon | 2934 | 2713 | 7.53 | 9463 | 9004 | yes | 19.472 |
| glab_mr_list | format_best | 2934 | 2417 | 17.62 | 9463 | 8483 | yes | 22.569 |
| glab_mr_list | skeleton (no-op) | 2934 | 2934 | 0.00 | 9463 | 9463 | yes | 9.973 |
| glab_mr_list | collapse | 2934 | 50 | 98.30 | 9463 | 133 | yes | 9.687 |
| glab_mr_list | pipeline_full | 2934 | 50 | 98.30 | 9463 | 133 | yes | 9.715 |
| runner_py | cmdfilter (no-op) | 1401 | 1401 | 0.00 | 5399 | 5399 | yes | 7.418 |
| runner_py | format_toon (no-op) | 1401 | 1401 | 0.00 | 5399 | 5399 | yes | 6.184 |
| runner_py | format_best (no-op) | 1401 | 1401 | 0.00 | 5399 | 5399 | yes | 6.681 |
| runner_py | skeleton | 1401 | 303 | 78.37 | 5399 | 1099 | yes | 8.803 |
| runner_py | collapse | 1401 | 59 | 95.79 | 5399 | 162 | yes | 6.218 |
| runner_py | pipeline_full | 1401 | 59 | 95.79 | 5399 | 162 | yes | 6.271 |

(The ~13 ms on the first row is the one-time lazy initialization of the BPE tokenizer
amortized into that measurement; steady-state per-fixture latency is sub-millisecond to a
few milliseconds, scaling with input size.)

## What this shows

Each deterministic component reduces tokens on exactly the fixture types it targets, and
is a safe no-op everywhere else:

- **Command-output filter (`cmdfilter`)** is the biggest single-component win on verbose
  CLI noise: on a real `cargo build` of rtk's 203-crate dependency graph it cut **94.2%**
  of tokens (2687 → 155) by dropping the `Compiling …` chatter while keeping the
  `Finished` line — reversibly. It is a deliberate **no-op** on the two small pytest/cargo
  fixtures: those are mostly failure/summary signal already, and below the size where the
  recovery-marker overhead is worth paying, so the filter declines to touch them (it never
  inflates an output).

- **Format re-encoder** is the right tool for structured tool/MCP outputs. On flat
  uniform record arrays (`flights_search`, `users_directory`, `products_inventory`) the
  best-encoding mode saved **36–57%** (TOON alone **28–52%**) by switching JSON to a
  delimited/TOON table — fully lossless, the data is identical. On nested record arrays
  (`glab_issue_list`, `glab_mr_list`, `oc_pods_slice`) the savings are smaller
  (**15–37%**) because the nesting limits the table encoders, but it still helps and never
  hurts.

- **Code skeletonizer** targets file reads: on the real Python source `runner.py` it
  dropped function bodies for **78.4%** token savings, keeping every signature.

- **Reversible collapse** is the universal fallback for an *unused* tool output (one whose
  content is not referenced later): it replaces the body with a recovery marker for
  **80–98%** savings. It is the strongest per-fixture reducer but the most aggressive — it
  hides content behind a marker rather than re-expressing it — so the pipeline prefers a
  signal-preserving reducer when one applies (see below).

- **Full deterministic pipeline** combines them in the engine's real order. Across all 10
  fixtures it took **11,183 → 786 tokens, a 93.0% reduction**, all reversible. Note the
  pipeline does **not** simply take the max of the columns: on `cargo_build` it reports
  94.2% (the cmdfilter result, 155 tokens) rather than collapse's 98.0% (54 tokens),
  because cmdfilter runs first as a pre-pass and keeps the failure/summary signal a model
  would actually need — the engine intentionally trades a few tokens for fidelity.

The headline: **cmdfilter −94% on verbose command output, format re-encode −35% (best) /
−29% (TOON) on structured outputs, skeleton −78% on a code read, and the full
deterministic pipeline −93% aggregate across 10 real fixtures — every reduction
reversible, with no model in the loop.**
