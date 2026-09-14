# LOCA — iteration 027

**One sentence.** The economic gate was found to refuse hardest exactly where compaction matters,
its approval term was found to shut the component down after three asks, both were fixed and the
fixes were confirmed to work — and then the adjudicator, finally asked in the regime its own
measurements call sound, was measured to be **worse than dropping everything** on the candidates a
proxy can score, while 46% of the mass turned out to be unscoreable in principle.

| | |
|---|---|
| arms | `cfg-iter027-{A-baseline,B-merged}.yaml` (pipeline = `housellm` + `summarize`, no `collapse`) |
| reward run | **not run.** No configuration measured here beats a free deterministic index. |
| spend | **$43.29**: $10.14 probes and shape verification, $8.11 a paired selection slice, $9.89 the rich-inventory arm, $15.15 earlier probes. The two load-bearing simulations cost **$0**. |
| corpus | 384 decision points over 3,046 candidates from iterations 022/024/026/027 traffic (67% iteration 024) |

## 1. Iteration 024's result cannot be attributed to the sweep

Every gate change since iteration 024 has been justified by reproducing its reward. Its own pooled
counters say arm B differed from arm A by **two** components, not one:

| component | A saved | B saved | B − A |
|---|---|---|---|
| `extract_llm` | **0** | 38,280,211 | **+38,280,211** |
| `format` | 20,603,867 | 38,695,372 | +18,091,505 |
| `extract_llm_sweep` | **0** | 2,353,227 | **+2,353,227** |
| `collapse` | 21,722,964 | 8,482,547 | −13,240,417 |
| `summarize` | 0 | 0 | 0 |

`extract_llm` was **absent from arm A entirely**. Of the incremental removal, the sweep is **6%**.
Arm B also issued 2,207 requests against arm A's 1,808 (+22%, consistent with the recorded +19.4%
steps), so trajectory headroom remains the plausible mechanism — but the experiment cannot say which
component bought it, and the smaller of the two has been carrying the credit.

`format` removing 18M more in arm B is not a component difference: it is first in both pipelines, and
the arms' trajectories diverge. Cross-arm per-component comparisons are confounded for that reason;
the structural fact — `extract_llm` ran only in B — is not.

## 2. The economic gate refuses hardest where compaction matters

`have = (window − reqAfter) / (reqBefore / turns)` is remaining window room measured in turns, so it
shrinks as the request fills. Simulated over all 384 decision points with the shipped functions
(`TestEconGatePassRateByBand`, no model calls), at a 128k window and a floored approval:

| context pressure | points | pass rate | mean inventory |
|---|---|---|---|
| 0–25% | 66 | **86%** | 5.7 |
| 25–50% | 150 | 38% | 8.6 |
| 50–75% | 107 | 17% | 7.3 |
| 75–90% | 16 | **0%** | 9.2 |
| 90%+ | 34 | **0%** | 9.8 |

Pass rate falls monotonically as inventory richness rises. The gate authorises thin, cheap asks on
near-empty requests and refuses the heavy ones. **Iteration 024's 82% pass rate (626 repaid against
138 not) was 700k of unused window**, not permissiveness in any transferable sense: its ~300k requests
against a silently-resolved 1,000,000 window gave `have ≈ 93`.

Validation: the simulation predicts **9%** pass at 64k under a floored approval; the live probe
measured **8%**.

## 3. The approval term shut the component down after three asks

The probe's three asks landed at 08:05:51, 08:06:35 and 08:07:21 — a 90-second window at the start of
a run whose stats were written at 08:20. Thirteen further minutes, no asks.

| ask | offered | approved | running approval |
|---|---|---|---|
| 1 | 2,318 | ~135 | 0.058 |
| 2 | 16,707 | ~332 | 0.025 |
| 3 | 18,502 | 0 | **0.0124** |

`minAskSamples = 3` reports an approval of **1.0** until three asks complete. Those three were exactly
the thin asks section 2's arithmetic admits; they returned **1.24%** of offered mass; the switch then
flipped and `expected = saved × approval` collapsed **20×**. Every later evaluation needed twenty
times the mass to clear the same bar: `prefix_rewrite_not_repaid` 35, `econ_ask_not_repaid` 13.

The trap closes on itself — the gate admits thin asks, thin asks produce a low ratio, the low ratio
declines the rich asks that would revise it, and the rich regime is never sampled.

**Fixed** (`askLedger`): approval is now shrunk on **offered mass** rather than switched on ask count,
`approval = (approved + k·prior)/(offered + k)` with `k = 1,000,000` tokens, and bucketed by inventory
size so a twelve-candidate ask is not priced by three-candidate history (issue #230). Cost keeps
`minAskSamples`, because it is an average over asks. Sequential simulation over 384 points against an
identical swept outcome model, ordering stable in all six cells: k=1M floored bucketed > k=300k >
unfloored > pooled > shipped. At 64k under the pessimistic model, 132 asks against the shipped 36 and
68,955 tokens removed against 7,329.

**The floor stays.** Removing it converged to 0.033 — below the floor — and made 11 fewer asks at
128k. Shrinkage does not subsume it; the floor is an optimistic bias that keeps the component asking.

## 4. The horizon credit is decisive, and testing it at a floored approval hid that

`creditRemoval` (#232) subtracts the expected removal from `reqAfter`, which is the direct answer to
section 2. Measured at a **prior** approval, where it has something to subtract:

| window | with credit | without | delta |
|---|---|---|---|
| 32k | 60% | 14% | **+46** |
| 64k | 86% | 38% | **+48** |
| 128k | 97% | 86% | +11 |
| 256k | 99% | 96% | +3 |

At a floored approval the same comparison reads 34% against 33% — inert. That is a conclusion about
the floor, not about the credit: `expected = saved × 0.05` leaves nothing to credit. The two fixes are
**not separable**, exactly as the preregistration claimed, and an intermediate reading in this
iteration that called the credit inert was an artifact of testing it in the regime where it cannot act.

## 5. Where the mass is, and the band question

| band (prefix tokens) | points | mean candidates | ≥10 candidates | mean ≥5k-token candidates | dead tokens/point |
|---|---|---|---|---|---|
| 0–16k | 22 | 5.9 | 18% | 0.9 | 4,745 |
| 16–32k | 35 | 6.2 | 23% | 1.2 | 9,490 |
| 32–64k | 151 | 8.4 | 50% | 2.0 | 28,729 |
| 64–128k | 33 | 8.0 | 42% | 3.7 | 62,003 |
| 128–200k | 9 | 10.6 | **78%** | 3.9 | 97,927 |
| 200–330k | 4 | 9.2 | 50% | 6.0 | 194,024 |
| 330k+ | 3 | 11.3 | 100% | 8.7 | 328,826 |

**The band requirement is contingent on approval, not on the band.** With a prior-quality approval and
the credit, 64k passes 86% and reaches 12.7M dead tokens; with approval floored, 256k is needed for
73%. A rule of "ten *large* candidates" is unreachable — 0% of points below 330k — while "ten
candidates" is met at 50% of 32–64k points and 78% at 128–200k.

`min_inventory` was therefore raised **3 → 10**. The 3 rested on a claim in that config's own comment
that iteration 024's firings "commonly carried THREE candidates"; its counters refute it —
`sweep_offered` 6,034 plus `sweep_over_ask_cap` 3,682 over ~626 asks is **15.5 eligible** candidates
per ask, capped by `maxAskItems` to a mean of **9.6 offered**. The only run that produced reward was
already at the inflection.

## 6. In the rich regime the adjudicator drops the mass — and drops the wrong mass

60 of the 169 decision points carrying ≥10 candidates; 694 candidates decided, 98% coverage within the
sampled points, $9.89. **Restricted to the 524 candidates the reuse proxy can score** (see section 7):

| arm | tokens removed | false-drop | live-kept |
|---|---|---|---|
| *null: drop everything* | *100%* | *32.1%* | *0%* |
| **index: unreferenced (free)** | 70.6% | **10.7% [8.0–14.4]** | **76.8% [69.8–82.5]** |
| model: sonnet-5 | 85.4% | **36.8% [32.1–41.8]** | 16.1% [11.3–22.4] |
| intersection (both agree) | 58.0% | **8.0% [5.2–12.2]** | **88.7% [83.0–92.6]** |

Rich inventory fixed what it was predicted to fix. Mean dropped candidate **4,414 tokens** against the
probe's 155, and **143 of 159 candidates ≥5,000 tokens were dropped (90%)** — the component no longer
keeps the mass and removes the trivia. Fabricated quotes fell to **2%** (from 27%), and criterion (b),
"an instruction not yet complete", collapsed from 28 keeps to 12.

It did not fix selection. The model's false-drop lower bound (32.1%) **touches the drop-everything
null exactly**. The decisive figure: of the 146 scoreable candidates it dropped that the index would
not, **122 (84%, [77–89])** were reused later. Agreement-gating is the only configuration that beats
the index — at 8.0% false-drop and 88.7% live-kept, removing *less*.

Two checks make these numbers trustworthy rather than merely computed: the index reproduces its
published false-drop (**10.7%** here against **11%** in
[the selection experiment](../../../results/coref-selection-experiment.md)), and the drop-everything
arm returns exactly 0% live-kept, which is the arithmetic test that the join to the proxy is real.

## 7. 46% of the mass cannot be scored, in principle

`coref`'s classifier calls an output **opaque** when `novel == 0`, where
`novel = res_tokens − prior − siblings − common − ref_tokens` and every token passes `distinctive()`
— length ≥ 4 after trimming `._:-/`, and either interior `_./:-`, a digit, CamelCase, or ≥5 digits if
purely numeric.

A **119,846-token** spreadsheet dump yields **five** distinctive tokens in its entire text
(`spreadsheetId`, `valueRanges`, `Score.1`, a UUID, a sheet name), all of them schema keys or sheet
identity, all removed by `prior`/`siblings`/`common`. Its payload — scores, dates, team names —
yields **zero**, because a two-digit score, a four-digit year and a lowercase team name each fail
`distinctive()` by design. Control: a 61,663-token email dump yields **455** (344 novel), because its
ids look like `email_SR1_main-track_8381`.

**The consequence is structural, not statistical.** A reference is detected as
`novel ∩ ref_tokens[j]`. If `novel` is empty the intersection is empty against any future text, so an
opaque candidate is recorded "never referenced" **by construction**:

| index verdict | n | referenced | `novel > 0` |
|---|---|---|---|
| unreferenced | 1,394 | 10.0% | **100%** |
| open | 588 | 76.0% | — |
| closed | 146 | 50.0% | — |
| **opaque** | **918** | **0.0%** | **0%** |

On this corpus that is **30% of candidates and 46% of token mass** with no measurement in either
direction. Three unlike populations are merged into the class: records whose values are
human-readable, re-sends whose every identifier was already in `prior`, and outputs dominated by
boilerplate keys. The second is close to safe-to-cut by definition; the first is unknown.

**Corrections this forces to figures reported earlier in this iteration.** "78% of candidates / 85% of
iteration 024's candidate mass is dead" counted all 918 opaque as dead by construction; on scoreable
candidates it is **69% of candidates and 75% of tokens**, and **53% of the mass called dead was never
measured, only labelled**. A hybrid arm of "index, plus the model on opaque only" appeared to dominate
the index — 79.6% of tokens at 7.5% false-drop with live-kept unchanged — entirely because its 155
extra drops were in the class that cannot be scored wrong. That arm is void.

**And the blindness is wider than the opaque class.** Both sides of `novel ∩ ref_tokens[j]` pass
through `distinctive()`, so the proxy cannot see a positional reference ("the address in row 1"), or
verbatim reuse of a value that is not identifier-shaped — an agent may copy `123 Main St` into an
email it sends and nothing will match. An attempt to quantify this by matching literal values instead
reproduced the exact failure `coref.py:67` documents: it scored `2025`, `2024`, `spreadsheetId` and
`valueRanges` as reuse and returned 74% hit on opaque against 68% on `unreferenced` — no difference,
because a loose matcher fires on ubiquitous tokens. That attempt establishes nothing.

So every false-drop figure here is a **floor**, for every arm, and the size of the gap is unknown
rather than small. What survives is the **comparison**: the proxy is computed per candidate
independently of the arm being scored, and section 6 restricts to candidates where it has some
sensitivity, so the ranking holds under a shared blindness. The absolute rates do not.

Deterministic Tier-2 widening is not the remedy and is already done —
[iteration 009](../iter009/results.md) moved referenced candidates 408 → 473 (+16%) and every arm's
false-drop with them, without narrowing the index-model gap. Widening a proxy does not make it ground
truth, and `opaque` is an **extraction** failure while normalization addresses **matching**.

## 8. Two defects fixed in the measurement path itself

- **The parser discarded complete answers.** `sonnet-5` sometimes replies with one verdict object per
  line and no array. `ParseVerdicts` scanned for `[`, found none, and threw away the whole ask —
  four well-formed, correctly-reasoned verdicts in the measured case. Now accepted, with every guard
  from the array path applied per line, all lines required to parse so truncation stays
  distinguishable, and at least two lines required so the pre-existing bare-object refusal is
  untouched.
- **The offline harness asked a different question.** `cg-selarm` flattened transcript and inventory
  into one user message; production sends the conversation as `messages` with the ask appended
  (`cheapmodel.CompletePrefixed`). Flattened, the model's own outputs arrive as pasted text, criterion
  (c) stops being about its own utterances, and the contract's "the conversation above is your own"
  becomes false. The flattened harness dropped **85%** of candidates where the live component dropped
  33% and iteration 024 dropped 43%. Now built as a prefix body plus trailing ask, with the shape
  asserted in the dry run (27 messages, 14 assistant / 13 user; ask 5,323 bytes against the live
  probe's 5,394).

Both mattered: the first arms run reported `failures=0` while 17 of 30 batches produced nothing, at
39% coverage. The rich-inventory run reported 98% coverage, 0 unparseable, 0 partial.

## What this settles, and what it does not

**Settled.**

- Iteration 024's reward is **not attributable to the sweep**; the experiment confounded it with
  `extract_llm`, which removed 94% of the incremental mass.
- The gate's pro-cyclicality and the three-ask shutdown are real, mechanical, and now fixed.
- The horizon credit and the approval fix are **not separable**.
- `min_inventory: 3` measured a regime the component's own evidence calls a guess.
- Rich inventory fixes *what the sweep acts on* — 90% of ≥5k-token candidates, 4,414-token mean drop.
- On scoreable candidates, the model does **not** beat a free deterministic index, and its marginal
  drops beyond it are wrong 84% of the time. This reproduces the selection experiment's conclusion by
  a different route.

**Not settled.**

- **Whether any of this changes reward.** No reward run. Sections 2–5 are about whether the gate
  fires; section 6 is about a proxy for selection quality. Neither is harm.
- **The 46% of mass that is opaque.** The sweep cuts there and nothing offline can score it. The only
  instrument that can is a paired reward run whose sole difference is whether opaque candidates may be
  dropped.
- **How much reuse the proxy misses on the scoreable 54%.** Unknown, floors only.

**Recommended gate before any further reward spend:** a configuration that dominates the free index on
scoreable candidates. Today exactly one does — the intersection arm — and it wins on quality while
removing less, which is a trade a reward run would have to adjudicate rather than an improvement.
