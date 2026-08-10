# Results — observe mode

Live through the harness, `claude-code` agent on `aws/claude-sonnet-5`, `codesmart`
pipeline, cache-aware billed cost (fresh $2/M · cache-read $0.20/M · cache-write $2.50/M ·
output $10/M) recomputed from each trial's token tiers. See [REPRODUCE.md](REPRODUCE.md).

**Scale caveat, stated up front.** 2 SWE-bench tasks and 2 Terminal-Bench tasks per mode at
n=1, plus one real Claude Code session per mode. Enough to answer the two questions observe
mode has to answer — does it add latency, and do its projections mean anything — and
nowhere near enough for a cost or solve-rate claim. The 50-task arms in the other results
pages are the ones to cite for savings.

## Does observe add latency to the enforced path?

**No.**

| | SWE-bench | Terminal-Bench | Live Claude Code session |
|---|---|---|---|
| `sync` added latency / req | 1,599.4 ms | 26.9 ms | 28.964 ms |
| **`observe` added latency / req** | **0.062 ms** | **0.076 ms** | **0.209 ms** |

Four orders of magnitude on SWE-bench, and it is structural rather than tuned: the request
path never runs the pipeline, so the only cost is copying the body and an enqueue.

Observe is not *free* in other respects — it moved 75.0 s of compaction off-path on
SWE-bench and spent $0.0779 of cheap-model tokens doing the measuring. It costs money and
CPU, just not request latency, and `observe_llm_notice` labels that spend for what it is.

## Do observe's projections match what sync actually achieved?

This is the question that validates the mode. Three independent lines of evidence, in
descending order of strength:

### 1. Controlled same-traffic comparison — exact agreement

The same five turns driven through the real handler under each mode:

```
sync:    before=43445  saved=10020  (23.06%)
observe: baseline=43445 potential=10020 (23.06%)
```

Identical. A test pins this, and it fails at ratio 0.33 if observe's own store is removed.

### 2. Terminal-Bench — correct agreement near zero (negative control)

| | `sync` | `observe` |
|---|---|---|
| content savings (enforced) | 1.02% | — (0 by construction) |
| projected savings | — | **0%** |
| added latency / req | 26.9 ms | 0.076 ms |
| enforced requests | 60 | **0** |

On traffic where sync achieves almost nothing, observe correctly projects almost nothing
rather than inventing a headline. This is the more convincing shape of the evidence: a mode
that only ever agreed on high-savings traffic would be much weaker proof that its
projections mean anything. It also correctly reported the overhead sync *would* have added
as 9.1 ms/req — small here because the pipeline made no model calls on these tasks.

### 3. SWE-bench arms — consistent, but too noisy to confirm independently

6.40% projected against 0.82% enforced. The gap is **not** explained away:

- the arms are different agent trajectories (22.5 vs 15.5 mean steps) — observe saw 46
  requests and 492,652 baseline tokens, sync saw 35 and 244,319. These are not the same
  conversations.
- observe's projection never pays a bounce. Nothing is offloaded, so no `expand` round trip
  can claw savings back and `wasted_tokens` is structurally 0. Under `sync` some savings do
  come back. Observe's projection is an **upper bound** on content savings, documented as
  one.
- 2 tasks at n=1 cannot separate a real bias from trajectory noise.

A 50-task paired run is the honest next step for this line specifically.

### Two bugs this question found

Answering it honestly was the most valuable thing the benchmark did, because the first
comparison was wrong twice, in opposite directions:

1. **11x overstatement.** The observe job ran without the session tracker, so its
   cached-prefix boundary was unknown, the tail gate never fired, and 50 `extract_llm`
   candidates passed where sync allowed 5 — 9.53% projected against 0.82% enforced. A
   projection that ignores cache-awareness projects what a *cache-blind* proxy would do and
   overstates by exactly what cache-awareness costs.
2. **3x understatement.** Fixing that exposed the opposite error: observe ran against a
   discarded buffer and so lost the frozen decisions offloaders replay on every later turn
   — where most of the sustained saving lives.

Both are fixed, both have a test that fails without its fix, and the exact agreement in §1
is the result.

## Namespace separation, verified in production

From the SWE-bench observe arm's live `/stats`:

- **enforced:** `requests: 0`, `saved_tokens: 0`, `sync_enforced: 0`, `components: {}` —
  all zero, all empty;
- **hypothetical:** `observe_hypothetical_requests: 46`,
  `actual_baseline_tokens: 492652`, `projected_optimized_tokens: 461112`,
  `potential_saved_tokens: 31540`, `potential_components: {…}` — fully populated.

No aggregate over the enforced savings rollups can reach a hypothetical, because they are
different accumulators with disjoint serialized names.

## Live Claude Code sessions

Same prompt and workspace through each mode against the live gateway:

| | `sync` | `observe` |
|---|---|---|
| requests (enforced) | 4 | **0** |
| `sync_enforced` | 4 | 0 |
| added latency / req | 28.964 ms | **0.209 ms** |
| baseline tokens | 6,025 | 6,025 *(as `actual_baseline_tokens`)* |
| task answered correctly | yes | yes |

Observe's `actual_baseline_tokens` = 6,025 is *exactly* sync's `tokens_before` = 6,025 on
the same prompt — the hypothetical namespace accounts for identical traffic identically,
measured independently. And `requests: 0` with every enforced savings aggregate at zero is
the machine-readable form of "context-guru did not modify anything".

## What is not established here

- Any cost or solve-rate claim per mode. 2 tasks at n=1; the billed-cost figures track
  trajectory length far more than they track mode.
- Whether observe's projection matches sync on *large-savings* traffic at scale. §1 shows
  exact agreement on controlled traffic and §2 correct agreement near zero; the SWE-bench
  arms are too small and too differently-shaped to confirm the middle of that range.
- The off-path queue under pressure: `dropped` was 0 on every arm, so that path is
  exercised only by tests, never yet by production load.
