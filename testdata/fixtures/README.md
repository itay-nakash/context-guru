# Measurement fixtures

Real-world tool-output fixtures used by `cmd/labcx-bench` to measure, on REAL data,
how much each deterministic reduction component reduces tokens/bytes. Every fixture is
sourced from a real project's command output or test corpus; provenance is listed below.

The harness wraps each fixture as a canonical Anthropic-shaped request (a `tool_result`
block, paired with the matching `tool_use` so command-aware passes can see the command),
runs the engine with one component enabled at a time, and reports token/byte reduction
plus reversibility. See `docs/RESULTS-offline.md` for the measured numbers.

## `cmd_outputs/` — exercises the command-output filter (`internal/reduce/cmdfilter.go`)

These are real CLI command outputs. The cmdfilter keeps the signal (failures, errors,
test-result summary) and drops routine noise (compile chatter, passing-line spam),
storing the original so the reduction is reversible.

| file | source | what it exercises |
| --- | --- | --- |
| `pytest_failures.txt` | rtk `src/cmds/python/pytest_cmd.rs` (inline `-q`-mode test fixture, `test_filter_pytest_quiet_mode_failures`) — https://github.com/rtk-ai/rtk | `pytest` rule: keep FAILURES + summary, verbatim |
| `cargo_test_failure.txt` | rtk `src/cmds/rust/cargo_cmd.rs` (inline `filter_cargo_test` fixture) — https://github.com/rtk-ai/rtk | `cargo test` rule: drop passing `... ok` lines, keep failures + `test result:` |
| `cargo_build.txt` | reconstructed from rtk's real `Cargo.lock` (one `Compiling <crate> v<version>` line per locked dependency, 203 crates, + a `Finished` line) — https://github.com/rtk-ai/rtk | `cargo build` rule: drop `Compiling` noise, keep `Finished` |

Note on `cargo_build.txt`: this is the genuine output shape `cargo build --release` emits
for rtk — every crate name and version is taken verbatim from rtk's committed
`Cargo.lock` (`awk '/^name = /{n=$3} /^version = /{print n,$3}' Cargo.lock`). It is a
faithful reconstruction of a real build's stdout, used because rtk's inline unit-test
fixture (3 crates) sits below the 8-line cmdfilter floor. The other two cmd fixtures are
verbatim from rtk's Rust unit tests.

## `structured_json/` — exercises the format re-encoder (`toon`, `tsv`/`csv`, `json_compact`)

Large structured tool/MCP outputs. The format reducer re-encodes them in the smallest
faithful representation (Token-Oriented Object Notation, delimited tables, or compact
JSON), keeping the data identical (the original is stored for exact recovery).

| file | source | what it exercises |
| --- | --- | --- |
| `flights_search.json` | winnow `benchmarks/data/tool_outputs.jsonl` record id-7 `context` (flight-search tool output) — `../winnow` | flat array of uniform scalar rows → `tsv`/`csv`/`toon` |
| `users_directory.json` | winnow `benchmarks/data/tool_outputs.jsonl` (user-directory lookup `context`) — `../winnow` | flat array of uniform scalar rows → `tsv`/`csv`/`toon` |
| `products_inventory.json` | winnow `benchmarks/data/tool_outputs.jsonl` (product-inventory `context`) — `../winnow` | flat array of uniform scalar rows → `tsv`/`csv`/`toon` |
| `oc_pods_slice.json` | rtk `tests/fixtures/oc_pods.json` (`oc get pods -o json`, representative 6-item slice of `items[]`) — https://github.com/rtk-ai/rtk | nested k8s object → `toon`/`json_compact` |

The winnow JSON values were stored in the JSONL as already-parsed JSON arrays; they are
written here as canonical pretty-printed JSON (no content changed).

## `search_results/` — exercises the format re-encoder on nested record arrays

Real list-style command results (arrays of objects with nested fields).

| file | source | what it exercises |
| --- | --- | --- |
| `glab_issue_list.json` | rtk `tests/fixtures/glab_issue_list_raw.json` (`glab issue list -F json`) — https://github.com/rtk-ai/rtk | nested array of records → `toon`/`json_compact` |
| `glab_mr_list.json` | rtk `tests/fixtures/glab_mr_list_raw.json` (`glab mr list -F json`) — https://github.com/rtk-ai/rtk | nested array of records → `toon`/`json_compact` |

## `file_reads/` — exercises the code skeletonizer (`internal/reduce/actions.go`)

A real source file read into context. The skeleton reducer keeps function/method
signatures and drops their bodies (tree-sitter, language-agnostic), reversibly.

| file | source | what it exercises |
| --- | --- | --- |
| `runner.py` | rtk `scripts/benchmark-sessions/lib/runner.py` (verbatim) — https://github.com/rtk-ai/rtk | Python function bodies dropped to `{ ... }` |

## A note on "headroom" fixtures

The task referenced a possible "headroom" fixture source (see winnow's
`benchmarks/bench_headroom.py` / `compare_real_headroom.py`). Inspecting those, headroom
is the *real `headroom-ai` CLI* run over the same trace requests for a head-to-head
comparison — it is not a separate fixture corpus; the inputs it compresses are the same
structured tool outputs winnow targets. Those structured tool outputs are exactly what
`structured_json/` already represents (from winnow's `tool_outputs.jsonl`). So the
"headroom-style" workload (large structured outputs scored by token reduction with
lossless recovery) is represented here via the winnow data, and no separate headroom
package was vendored. This is the honest provenance.

## Reproducing the fixture collection

- rtk: `git clone --depth 1 https://github.com/rtk-ai/rtk /tmp/rtk` then copy the paths
  listed above. The two cmd fixtures verbatim-from-source live inside rtk's Rust unit
  tests (`r#"..."#` literals); they were lifted unchanged.
- winnow: `../winnow/benchmarks/data/tool_outputs.jsonl` — the `context` field of the
  named records, written out as canonical JSON.
