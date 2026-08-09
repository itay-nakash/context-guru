# cacheinject

!!! info "Reformat — lossless"
    Places Anthropic `cache_control` breakpoints at the positions that minimise billed
    input cost, so the provider KV cache is read rather than re-processed.

## How it works

Placement is the solution to a cost minimisation, not a heuristic. With `R = 0.1`
(cache read), `W = 1.25` (5m cache write) and `1.0` (plain input) as multipliers on
the base input price, four facts fix the policy:

1. **Write anything resent even once.** A token resent `k` more times costs `W + kR`
   written and `1 + k` unwritten, so writing wins for `k > (W−1)/(1−R) = 0.28`.
   Hence a breakpoint on the **last block**, every turn.
2. **Writes bill as a span,** `(highest breakpoint − read point)`, not per
   breakpoint. An extra breakpoint *below* the top therefore costs **exactly zero**
   and only adds a position a later turn's backward read-walk can land on. So none
   of the four slots is left idle.
3. **Divergence is the large lever.** If a turn differs from the previous one at
   block `d`, every block above `d` is unmatchable: a trailing-only breakpoint finds
   nothing and the whole prefix bills at 1.0x instead of 0.1x. A breakpoint at `d−1`
   recovers it. Worth 12.5x more than any positional tuning, because it restores
   read→read rather than read→write.
4. **Anchors must be turn-stable.** A position is readable on turn `t+1` only if it
   was written on turn `t`, so an anchor counted back from the end lands on a
   different block each turn and is never pre-warmed. Anchors are counted up from
   the start.

The component tracks a per-message digest per session to find `d`, and **keeps every
breakpoint the caller already set** — an agent that places its own is usually
already at the optimum, and the provider's 4-breakpoint cap means a fifth is a 400.

`5m` TTL by default. A `1h` write costs 2.0x instead of 1.25x and only pays when
`p = P(the entry lapses before its next reuse)` exceeds `(2.0−1.25)/(1−0.1) = 83.3%`.
Two things keep `p` near zero here: agent turns are seconds apart (measured median
7.6 s over 1,905 real turns), and every **read refreshes the TTL for free**, so a
shared prefix touched by any session within 5 minutes never lapses — on the benchmark
sweep a new task started every ~2.3 minutes. Set `ttl: 1h` only when reuse is
genuinely sparse (low-concurrency sweeps with task starts more than 5 minutes apart,
or a deployed agent handling a few sessions per hour). That is a property of the
traffic, not of the code.

## Before → After

```
before:  [ tools ][ system ]…[ msg d−1 ][ mutated msg d ]…[ newest turn ]

after:   [ tools ][ system ]…[ msg d−1 {cache_control} ]…[ newest turn {cache_control} ]
                              ^ rescues the stable head    ^ writes this turn's growth
```

## What it is worth — measured, not asserted

Simulated on a captured 91-request SWE-bench stream, calibrated to within 2.10 pp of
the cache-hit rate a live 50-task run actually billed:

| policy | cost | hit | vs the agent's own placement |
|---|--:|--:|--:|
| claude-code's own breakpoints | $0.9270 | 96.04% | — |
| **v1** (breakpoint before the newest turn) | $0.9780 | 95.23% | **+5.50%** |
| **v2** (this policy) | $0.9315 | 96.04% | **+0.49%** |

**Against an agent that already caches well, expect exactly 0%.** Measured on real
traffic: claude-code marks its own final message on **466 of 472 requests (98.7%)**,
so rule 1 is already satisfied and every candidate position this component would
choose collides with an existing breakpoint. Instrumented over a live 12-task run it
placed **zero** extra breakpoints (`runs 432, acted 0`) and was byte-identical to
baseline on the wire.

So this component's value is precisely two things, neither of which is a saving
against claude-code:

1. It **stops v1's +5.5% regression** (see the warning below).
2. It places breakpoints for agents that do *not* mark their own tail, and it anchors
   below a divergence when a prefix mutates mid-conversation — neither of which
   claude-code needs.

The measurable cost saving on claude-code traffic comes from the **cross-session
prefix repairs** in `apply/prefixorder.go`, which are gated on this component being
configured but are a different mechanism. See below.

!!! warning "v1 was a regression"
    v1 placed a single breakpoint on the message *before* the newest turn, which by
    construction shortens the cached prefix on every turn: **+5.50%** standalone and
    **+9.21%** layered on an agent's own breakpoints. Its documentation described
    the savings as "invisible to `/stats`", which made a negative effect
    unfalsifiable. If you are pinning an older release, this component cost money.

## Lossiness

None. It attaches cache directives only; model-visible content is unchanged, and
this is asserted in the verification (`bob-experiments/cacheopt/verify_proxy.py`)
by comparing model-visible text byte-for-byte across all 91 captured requests.

Messages whose content is a bare string cannot carry a block-level directive; they
are skipped rather than restructured, and the breakpoint falls back to the nearest
markable block below so the prefix is still written.

## Configuration

```yaml
components:
  cacheinject:
    ttl: 5m        # or 1h — see the TTL rule above. Anything else is rejected at load.
```

`K=4` breakpoints and `L=20` lookback blocks are provider facts, not tunables.

## When it shines

Agents that do **not** set their own `cache_control`, and any conversation whose
prefix mutates below the cache boundary (an agent rewriting a running summary or
scratchpad, a re-rendered header, an injected timestamp).

## When it's inert

On OpenAI- and Gemini-shaped wires, where `cache_control` does not exist at all —
placement has nothing to express and the split has nothing to gain (see below). Also
inert when four breakpoints are already present, and on string-content messages.

## The volatile-tail split

Enabling `cacheinject` also switches on a body-level repair in `apply/prefixsplit.go`
that no breakpoint placement can achieve, because a cache entry hashes **everything
before** its breakpoint and no position can exclude part of a single block.

Claude Code appends a live environment snapshot to the **end** of its main system
block:

```
Current branch: main
...
Recent commits:
0898367954 SWE-bench
```

Measured across 50 SWE-bench tasks, that block is ~7,017 tokens of which the first
**6,921 (98.4%)** are byte-identical across sessions — but it is one cacheable unit
with its breakpoint at the end, so the hash covers the churning tail and the shared
98.4% is re-written every session.

The tail is real content: it cannot be moved or dropped without lying to the model
about the repo state. It can be **split** — `[stable][volatile]` as two text blocks
with the same concatenated text, breakpoint on the first. Adjacent text blocks
concatenate, so the model sees a **byte-identical** prompt while the provider gains a
hash boundary that excludes the churn. Asserted in `TestSplitIsConcatenationIdentical`.

**Explicit-breakpoint providers only** (Anthropic family). Under an implicit
longest-prefix cache (OpenAI, Gemini) the match already ends at the divergence, so a
block boundary buys nothing.

### What it is worth — measured four ways

**1. Structural — is there a target?** Across 50 real Claude Code sessions, the
stable half of that block takes **1** distinct value while the volatile tail takes
**50**. So without the split the breakpoint hashes stable+volatile, a value unique to
each session, and the 6,877-token stable half can never be read from another
session's cache.

**2. Mechanical — does the transform do what it claims?** Through the live proxy on a
real captured body: system blocks `3 → 4`, the concatenation byte-identical, and
breakpoints `2 → 2` (no slot consumed).

**3. Isolated live A/B — does the provider actually cache it?** Two sessions differing
*only* in the git snapshot, same breakpoints, same order, judged by
`cache_read_input_tokens` from the API. Three runs, byte-identical results:

| arm | session-2 read | session-2 write | hit |
|---|--:|--:|--:|
| without split | **0** | 8,882 | **0.0%** |
| **with split** | **8,597** | **0** | **96.7%** |

At Sonnet 5 rates ($2.50/MTok write, $0.20/MTok read) that first request of a warm
session costs **$0.022205 → $0.001719**, i.e. **$0.0205 saved per session (−92.3%)**.
Per model, per session: Sonnet 5 **$0.0205** (**$0.0307** from 2026-09-01),
Opus 5 **$0.0512**, Haiku 4.5 **$0.0102**. At 100k sessions on Sonnet 5 that is
**$2,048**.

The saving is **once per session, not once per turn**: within a session, turns 2…n
already read the prefix turn 1 wrote in *both* arms. What the split changes is the
first request of each warm session, which would otherwise re-*write* the system
prompt. A cold first session pays the same either way.

**4. End-to-end — what survives in a real agent run?** Terminal-Bench,
`fix-code-vulnerability`, 3 trials, sequential:

| trial | without | with split | saved |
|---|--:|--:|--:|
| 1 | $0.2003 | $0.1368 | $0.0636 (31.7%) |
| 2 | $0.1828 | $0.1198 | $0.0630 (34.5%) |
| 3 | $0.2003 | $0.1280 | $0.0723 (36.1%) |
| **mean** | **$0.1945** | **$0.1282** | **$0.0663 (34.1%)** |

Cache-write median **−58.8%**; cost median **−34.5%**, spread **4.4 pp** across
trials.

Note the end-to-end saving ($0.0663) is **3.2× the isolated per-session figure**
($0.0205): trial 1 recovered 29,882 cache-write tokens, 3.4× the 8,882-token system
prompt. Claude Code issues sub-agent and side requests that each re-send the same
system prompt, and each was independently re-writing it. So the benefit accrues per
*request carrying the system prompt*, not per session.

!!! note "What is NOT claimed"
    A second Terminal-Bench task (`git-leak-recovery`) was **within noise** — its
    step count moved 10→15, 12→13, 10→16 between arms, giving a 95 pp spread on
    cache-write. Its numbers are not quoted, and neither is the pooled two-task total
    (−16.1%), which would inherit a delta that task did not earn. All figures are
    Sonnet 5 rates on one benchmark; treat 34.1% as one task measured three times,
    not a fleet average.

    **Placement contributes $0 of this.** `acted=0` across 69 runs — every dollar
    above is the split.

!!! warning "A withdrawn claim, kept visible on purpose"
    An earlier version of this work also reordered the first system block, on the
    belief that Claude Code's `x-anthropic-billing-header: cc_version=2.1.222.<hex>;`
    was a **per-session nonce** poisoning a 28,297-token shared prefix.

    That was wrong. Across 74 captured conversations the suffix takes only **49
    distinct values**, and one value (`38c`) appears in **24 different
    conversations** — a per-session nonce cannot repeat across sessions. It tracks
    the claude-code **build**, not the session. The original conclusion came from a
    13-task sample where every value happened to differ.

    A sequential Terminal-Bench run then confirmed the consequence directly: with
    the header present, session 2 read **38,855 tokens (92.8% hit)** that session 1
    had written. **Cross-session reuse already works.** There was no poisoning to
    repair, so the reorder and its provider-generalisation were removed.

    A live Azure OpenAI experiment did show 0% → 99.0% cache hit from moving a
    leading volatile block — but the nonce in that experiment was **synthetic**,
    constructed to demonstrate the mechanism. It proved that a volatile leading block
    breaks an implicit prefix cache; it did not show that any real agent sends one.

Full derivation, retractions, and reproduction steps:
`bob-experiments/docs/cache-optimization.md`.

See also: [Components overview](../components.md) ·
[Choose a preset](../how-to/choose-a-preset.md)
