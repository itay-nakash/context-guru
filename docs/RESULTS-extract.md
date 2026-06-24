# Real cheap-model extractor results (ONLINE, claude-haiku-4-5)

- **Date measured:** 2026-06-24
- **Mode:** ONLINE. The cheap-model **Extract** stage is **ON** for every row. Each row is
  the result of a REAL call to a live model gateway — there is no mock and no canned
  output anywhere in this measurement.
- **Model:** `claude-haiku-4-5`, served via an Anthropic-compatible LiteLLM gateway
  (`ANTHROPIC_BASE_URL`), bearer-authenticated (`cheapmodel.Anthropic{AuthScheme: "bearer"}`).
  The token is never printed or committed.
- **`LLMCompactFloor` used:** **200** tokens. The production default is 3000. The committed
  structured fixtures are only a few hundred tokens each, so the floor is lowered to 200
  **purely for this measurement** so that extraction actually fires on the corpus. A
  fixture under 200 tokens is below the floor and never becomes an extraction candidate —
  it shows up honestly as `strategy=none`, `0% saved`. This is a measurement-only knob and
  changes no default.
- **`ProtectRecent`:** 1 (the goal turn is the only protected trailing turn; the
  `tool_result` sits outside the protect window so it is eligible).
- **Latency:** `latency_ms` is **wall-clock of the full `engine.Transform`, including the
  real network round-trip to the gateway model** (plus, for the `code` strategy, local
  Starlark execution of the model-written filter). It is therefore dominated by network +
  inference time, not engine overhead.
- **Tokenizer:** `o200k_base` via `internal/tokens` — the same counter the engine uses to
  gate reductions.
- **Fixtures:** the large/structured corpus only — the `structured_json/` and
  `search_results/` record-shaped fixtures under `testdata/fixtures/`. Each is surfaced as
  a `tool_result` paired with a realistic recent goal that names specific records (so the
  keep-set the model filters toward is concrete and realistic). Provenance:
  `testdata/fixtures/README.md`.

## Reproduce

```sh
# from the repo root; loads ANTHROPIC_BASE_URL + ANTHROPIC_AUTH_TOKEN + the haiku model id
source /tmp/lcx_env.sh
CGO_ENABLED=1 go run ./cmd/labcx-bench --extract            # the table below
CGO_ENABLED=1 go run ./cmd/labcx-bench --extract --json     # machine-readable rows
```

`--extract` requires `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN` in the environment;
it fails fast if they are unset. Without `--extract` the harness stays fully offline and
deterministic (see `RESULTS-offline.md`) — no model is called.

## Results

These are the exact rows printed by the command above on 2026-06-24. They are not edited.
Because they come from a live model, exact `%saved`, `strategy`, and `latency_ms` will vary
run to run (the model is non-deterministic); the qualitative outcome — which fixtures shrink
and which decline — is stable.

| fixture | tokens_before | tokens_after | %saved | strategy | contained | latency_ms | model |
| --- | ---: | ---: | ---: | :---: | :---: | ---: | :--- |
| flights_search | 607 | 122 | 79.90 | code | yes | 1921 | claude-haiku-4-5 |
| users_directory | 194 | 194 | 0.00 | none | no | 1 | claude-haiku-4-5 |
| products_inventory | 254 | 93 | 63.39 | code | yes | 2492 | claude-haiku-4-5 |
| oc_pods_slice | 811 | 258 | 68.19 | single | yes | 8533 | claude-haiku-4-5 |
| glab_issue_list | 2106 | 2106 | 0.00 | none | no | 3138 | claude-haiku-4-5 |
| glab_mr_list | 2934 | 1270 | 56.71 | deterministic | yes | 8081 | claude-haiku-4-5 |

## Analysis (honest)

**Shrank (4 of 6).** The extractor reduced four fixtures, each via a different strategy:

- `flights_search` **−79.9%** and `products_inventory` **−63.4%** via the **code** strategy
  — the model wrote a Starlark filter that ran locally over the *full* body and kept only
  the records named in the goal (FL003/FL004; SKU0001/SKU0004).
- `oc_pods_slice` **−68.2%** via the **single** strategy — a one-shot JSON-return filter
  keeping the pod(s) the goal asked about.
- `glab_mr_list` **−56.7%** via the **deterministic** strategy — the model strategies did
  not beat the deterministic projection here, so the ordered fallback (a deterministic,
  keep-set-driven projection) won. This is still a real run: the model strategies were
  attempted first and declined, and the safe deterministic fallback produced the kept set.

**Declined (2 of 6) — both shown honestly as `strategy=none`, `0% saved`:**

- `users_directory` (194 tokens) is **below the 200-token floor**, so it never became an
  extraction candidate. The 1 ms latency reflects that no model call was made. This is the
  floor working as designed, not a model failure.
- `glab_issue_list` (2106 tokens) **did** qualify (above the floor) and the model was
  called, but no strategy produced a result that was both strictly smaller AND passed the
  containment/validation gate, so the engine kept the original untouched. A decline is a
  valid, **safe** outcome: fail-open means the original is forwarded unchanged.

**Containment / reversibility.** Every spliced result is marked `contained=yes`, verified
by the harness via the public `engine.FindMarkers` + `engine.Expand` round-trip: each
spliced block carries a recovery marker whose stored original is a substring of the
pre-extraction body. So every reduction here is **lossless and reversible** — the engine
only splices a result it can fully recover from its rewind store, and only when the result
is strictly smaller (it never inflates). Declined fixtures show `contained=no` simply
because nothing was spliced (the original is intact).
