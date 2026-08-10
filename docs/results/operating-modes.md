# Results — operating modes (sync vs async vs observe)

Live through the harness, `claude-code` agent on `aws/claude-sonnet-5`, `codesmart`
pipeline, cache-aware billed cost (fresh $2/M · cache-read $0.20/M · cache-write
$2.50/M · output $10/M) recomputed from each trial's token tiers. See
[REPRODUCE.md](REPRODUCE.md).

**Scale caveat, stated up front.** These are 2-task arms (n=1) — enough to validate
the *mechanism* and to answer the latency and cache questions, nowhere near enough for
a cost or solve-rate claim. The 50-task arms in the other results pages are the ones to
cite for savings. What is measured here is whether each mode does what it says.

## SWE-bench Verified — 2 tasks, n=1

| | `sync` | `async` | `observe` |
|---|---|---|---|
| solved | 2/2 | 2/2 | 2/2 |
| mean steps | 15.5 | 21.5 | 22.5 |
| **added latency / req** | **1,599.4 ms** | **25.3 ms** | **0.062 ms** |
| content savings (enforced) | 0.82% | 4.17% | — (0 by construction) |
| projected savings | — | — | 6.40% |
| cache-read | 1,464,729 | 2,186,100 | 2,300,699 |
| **cache-write** | **52,287** | **42,980** | 127,589 |
| cache-write per 1M cache-read | 35,697 | **19,661** | 55,458 (not enforcing) |
| cache-hit rate | 96.55% | **98.07%** | 94.74% |
| fresh input | 54 | 78 | 86 |
| output | 10,249 | 10,711 | 11,522 |
| billed cost | $0.5263 | $0.6519 | $0.8945 |
| context-guru's own LLM cost | $0.0122 (1 call) | $0.0435 (4 calls) | $0.0779 (7 calls) |
| off-path compaction time | — | 80.8 s | 75.0 s |
| deferred compactions committed | — | 1 | 0 (never commits) |
| `async_realized_saved_tokens` | — | 15,962 | — |
| queue `{dropped, stale_discarded}` | — | `{0, 0}` | `{0, 0}` |

## The four questions

### 1. Does async reduce added latency without increasing cache-write?

**Latency: yes, decisively.** 1,599.4 ms → 25.3 ms per request, a **63x reduction**.
The mechanism is visible per component: `extract_llm` costs 15,014 ms on sync's request
path and 71.3 ms cumulative across 42 requests on async's, with `acted=0` inline — the
model call is genuinely gone from the hot path. 80.8 s of compaction ran off-path,
charged to nobody's request.

**Cache-write: it went DOWN, which is the result the policy was designed for.** Absolute
cache-write 52,287 → 42,980 (**−17.8%**) on ~49% *more* cache-read traffic. Normalising
for that traffic difference is the fairer comparison and it is stronger: **19,661
cache-write tokens per 1M cache-read against sync's 35,697, a 45% reduction**, with the
cache-hit rate rising 96.55% → 98.07%.

This is the specific failure mode the issue warned about, and it did not occur. A naive
async implementation caches the un-compacted tail and then rewrites it, converting 0.1x
reads into 1.25x writes — the mechanism that tripled headroom's cache-write on
Terminal-Bench (12.37M vs a 4.01M baseline). Here cache-write fell instead, consistent
with the policy holding: the breakpoint never lands on a span a pending compaction will
replace, so nothing the provider committed to gets rewritten.

Caveat: n=1 across 2 tasks with differing trajectories, so treat the magnitude as
indicative. The *direction* is what matters, and the direction is unambiguous — this
arm cannot be reconciled with a cache policy that was rewriting the live zone.

### 2. Does async reach the same steady-state savings as sync, just later?

**On this evidence it reached more, not less** — 4.17% enforced against sync's 0.82% —
but do not read that as async being better at compaction. Both numbers are small and
noisy at n=1, and the arms took different trajectories (different step counts, so
different traffic). The load-bearing observation is narrower and does hold:
`async_realized_saved_tokens` = 15,962 = the entire enforced saving, from 1 committed
deferred compaction. Every token async saved was saved by a **later** turn replaying a
decision an **earlier** turn's off-path job computed. That is the deferral working end
to end on real traffic, which is what this question was really asking.

Note also what async spent to get there: 80.8 s of compaction ran off-path against 25.3
ms/req on-path. The work did not disappear, it moved.

### 3. Does observe add measurable latency to the enforced path?

**No — 0.062 ms/req**, against sync's 1,599.4 ms on the same benchmark. Four orders of
magnitude, and it is structural rather than tuned: the request path does not run the
pipeline at all, so the only cost is copying the body and an enqueue. Confirmed
independently in a real Claude Code session: 0.209 ms in observe against 28.964 ms in
sync.

Observe is not *free* — it moved 75.0 s of compaction off-path and spent $0.0779 of
cheap-model tokens to do the measuring. It costs money and CPU, just not request
latency.

### 4. Do observe's projections match what sync actually achieved?

This is the question that validates the mode, and answering it honestly found **two
real bugs** — the most valuable thing the benchmark did.

The first comparison read **9.53% projected against 0.82% enforced**, an 11x
overstatement. Cause: the observe job ran without the session tracker, so its
cached-prefix boundary was unknown, the tail gate never fired, and 50 `extract_llm`
candidates passed where sync allowed 5. A projection that ignores cache-awareness
projects what a *cache-blind* proxy would do and overstates by exactly what
cache-awareness costs. Fixed by sharing the tracker.

That exposed a second error in the opposite direction: observe then **under**-projected
by ~3x, because it ran against a discarded buffer and so lost the frozen decisions
offloaders replay on every later turn — where most of the sustained saving lives. Fixed
by giving observe its own persistent-but-disjoint store.

After both fixes, on identical traffic through the real handler, projection and actual
agree **exactly**: 10,020 tokens / 23.06% each. A test pins it, and that test fails at
ratio 0.33 if the shadow store is removed.

On the SWE-bench arms the remaining gap is **6.40% projected vs 0.82% enforced**, and
that gap is *not* explained away — it is the honest discrepancy this section owes:

- The two arms are different agent trajectories (22.5 vs 15.5 mean steps), so the
  traffic differs. Observe saw 46 requests and 492,652 baseline tokens; sync saw 35 and
  244,319. These are not the same conversations.
- Observe's projection never pays a bounce. Nothing is offloaded, so no `expand` round
  trip can claw savings back, and `wasted_tokens` is structurally 0. Under `sync`, some
  savings do come back. Observe's projection is an **upper bound** on content savings,
  and is documented as one.
- 2 tasks at n=1 cannot separate a real bias from trajectory noise.

The controlled same-traffic test is the strong evidence for agreement; the benchmark
arms are consistent with it but too small to confirm it independently. A 50-task paired
run is the honest next step.

## Real Claude Code sessions (one per mode)

Same prompt and workspace through each mode, live gateway:

| | `sync` | `async` | `observe` |
|---|---|---|---|
| requests (enforced) | 4 | 5 | **0** |
| `sync_enforced` / `async_enforced` | 4 / 0 | 0 / 5 | 0 / 0 |
| added latency / req | 28.964 ms | 17.797 ms | **0.209 ms** |
| baseline tokens | 6,025 | 8,124 | 6,025 *(as `actual_baseline_tokens`)* |
| queue `{dropped, stale_discarded}` | — | `{0, 0}` | `{0, 0}` |
| task completed correctly | yes | yes | yes |

All three produced the correct answer, so no mode broke the agent.

Two details worth noting. Observe's `actual_baseline_tokens` = 6,025 is *exactly*
sync's `tokens_before` = 6,025 on the same prompt — the hypothetical namespace accounts
for identical traffic identically, measured independently. And observe reports
`requests: 0` with every enforced aggregate at zero, which is the machine-readable form
of "context-guru did not modify anything".

## Metric namespace separation, verified in production

From the SWE-bench observe arm's `/stats`:

- enforced: `requests: 0`, `saved_tokens: 0`, `sync_enforced: 0`, `async_enforced: 0`,
  `components: {}` — all zero, all empty;
- hypothetical: `observe_hypothetical_requests: 46`, `actual_baseline_tokens: 492652`,
  `projected_optimized_tokens: 461112`, `potential_saved_tokens: 31540`,
  `potential_components: {…}` — fully populated.

No aggregate over the enforced rollups can reach a hypothetical, because they are
different accumulators with disjoint serialized names.

## Bugs the benchmark found that the tests did not

Recorded because the tests passed while all four were live:

1. **Deferred runs double-counted.** Off-path reports were stamped `async` and entered
   the enforced rollups even though nothing was forwarded — then entered again when a
   later turn replayed the decision on-path.
2. **`cacheinject` corrupted turn state off-path.** Its per-message divergence digests
   are turn state; a deferred job commits several turns later, so committing them
   replayed turn N's digests over turn N+2's.
3. **Observe overstated by 11x** (missing cache boundary).
4. **Observe then understated by 3x** (discarded frozen state).

Each is now covered by a test that fails without its fix.

## What is not established here

- Any cost or solve-rate claim per mode. 2 tasks, n=1. The billed-cost column tracks
  trajectory length more than it tracks mode.
- The cache-write result's *magnitude*. Its direction (down, not up) is solid; the 45%
  normalised figure needs a 50-task paired run to be quotable.
- Async under concurrency pressure: `dropped` and `stale_discarded` were 0 on every
  arm, so the drop and stale-discard paths are exercised only by tests, never yet by
  production load.
