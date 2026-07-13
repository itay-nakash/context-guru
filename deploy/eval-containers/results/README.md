# SWE-bench sweep results (committed snapshots)

The live `../sweep-results.csv` is gitignored (scratch, overwritten each run). This
directory holds dated snapshots of interesting runs so the numbers behind
[`docs/RESULTS.md`](../../../docs/RESULTS.md) are reproducible and preserved.

## `swe-bench-sweep-2026-07-13.csv`

Full sweep of every config over the 10-task set, via the eval-containers compose
stack against the IBM litellm upstream (`EVAL_MODEL=anthropic/claude-sonnet-4-6`,
`claude-code` agent). One row per (task, config).

Columns: `task, config, agent, reward, passed, wall_s, gw_requests, gw_before,
gw_after, gw_saved, gw_pct, note`. `gw_*` are the gateway `/stats` rollups
(`tokens_before/after`, `saved_tokens`, `savings_pct`) for that cell.

LLM-based configs use the final tuned triggers (`model.source: incoming`):

- `cg-extract-code` — `extract {strategy: code, trigger: {min_output_tokens: 700, min_request_tokens: 4000}}`
- `cg-summarize` — `summarize {trigger: {min_request_tokens: 6000, min_messages: 10}, resummarize_tokens: 5000}`

Headline (10 tasks): resolved 6/6/6 (baseline / extract-code / summarize) — zero
reward regression. extract-code reduced ~23% of its request tokens overall
(≈193k saved, fired on 5 tasks); summarize ≈4.8% (≈15.6k, fired on the 2 large
reward-0 tasks). Deterministic configs (`cg-mask`, `cg-dedup`, …) are included for
comparison. `pydata__xarray-4629` / `scikit-learn__scikit-learn-25931` runner images
are flaky under arm64 emulation; some cells show `up-failed`.
