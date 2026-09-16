# LOCA — iteration 028 (preregistration)

**The question, in one sentence.** Does iteration 024's reward result reproduce at a band that is
actually 64k, with a gate that is honestly priced — because iteration 027 measured the same selector as
no better than dropping everything, and iteration 024 won reward with it anyway.

Written before the run. Predictions, margin and stopping rule are fixed here; anything decided after
the numbers exist is recorded as an amendment with its date.

## 1. Why this is worth reward money when nothing since iteration 024 has been

Two facts from iteration 027 that are only compatible under a specific hypothesis:

- On the 524 candidates a reuse proxy can score, the adjudicator's false-drop lower bound (32.1%)
  **touches the drop-everything null exactly**, and of the 146 scoreable candidates it dropped that
  the free deterministic index would not, **122 (84%) were reused later**.
- Iteration 024, whose arms differed in **two keys on that same component and nothing else**, gained
  **+25% accuracy** at **+2% cost per step**.

A selector that removes wrongly should not win reward. It can only have done so if **removal volume,
not selection quality, is what buys reward** — plausible because every removal is reversible through
`expand`, so a wrong drop usually costs a round-trip rather than the task. The counter-evidence is in
iteration 024's own ledger: **`expand_unresolved_missing` = 175 in ITS arm B against 0 in its arm A** — content
the agent asked for and did not get back.

Offline scoring cannot separate these. It ranks selectors against a proxy for reuse; it has no access
to whether the agent was harmed. This is the measurement the whole line has been deferring.

## 2. It will fire often enough to be measurable, and that is simulated rather than hoped

Iteration 026 fired **zero** asks and measured nothing. The precondition for spending here is a
configuration whose firing rate is known in advance. Simulated over 384 decision points with the
shipped gate functions (`TestEconGatePassRateByBand`), as a fraction of **all** decision points:

| window | `min_inventory` | qualify | ask/all | dead tokens behind asks |
|---|---|---|---|---|
| 64k | 10 | 44% | 36% | 5.54M |
| **128k** | **10** | 44% | **43%** | **7.40M** |

Iteration 024's measured rate was **28%** (626 asks / 2,207 requests). So 64k with the fixed estimator
exceeds the only firing rate that ever produced reward, **without** the 1,000,000-token window that
made iteration 024's gate permissive by accident.

The estimator holds because of the floor, not despite it: the rich-inventory run measured the
adjudicator approving **~85% of offered mass**, where the three thin asks that shut the component down
approved **1.24%**. `min_inventory: 10` feeds the ledger the evidence that keeps the gate open.

**Pre-flight gate — do not start the run unless all four hold.** Iterations 008–024 all ran outside
their declared band without anyone noticing, and iteration 026 spent a full run to discover a zero.

1. `model_info_unresolved` = 0 and the resolved `ctx_window` printed as **128000**.
2. A probe pass shows `sweep_offered` > 0 and at least **five** asks across the pass.
3. `sweep_inventory_below_min` and `sweep_offered` both reported, so the half declined by the floor is
   visible rather than inferred.
4. On the probe, the BASELINE arm reports `acted = 0` and arm A reports at least five asks. The arms must
   actually differ on live traffic, or the run measures two identical things — which is what iteration
   026 spent a full run discovering.

## 3. Arms — two, paired within one pass

Arms B and C from an earlier draft of this file are **deferred**, at the operator's direction: nothing
is worth comparing until the fixed configuration is shown to beat doing nothing.

Both arms share: pipeline = `housellm` + `summarize`, **no `collapse`**; band **128k** (see the
amendment below; this file originally said 64k);
`min_inventory: 10`; `min_later_turns: 3`; `keep_recheck_turns: 4`; the same 15 environments and the
same 5 seeds iteration 024 used.

| arm | sweep block |
|---|---|
| **baseline** | `min_tokens: 100`, `min_inventory: 10` — **`econ_trigger` absent**, so the component cannot act |
| **A** | the same, plus `evidence: true`, `econ_trigger: true`, `reward_premium: 20`, `min_pressure: 0.20`, the horizon credit and the mass-shrunk bucketed estimator |

**Why a baseline arm exists at all, when iteration 024 already has one.** It does not: iteration 024 ran
at a window that silently resolved to **1,000,000**, and this runs at 64k. That is established from its
own transcripts rather than inferred — **19% of its decision points carried prefixes already over 64,000
tokens, to a maximum of 507,263**, which cannot happen against a 64k window. Reading a 64k arm against
iteration 024's stored numbers would confound the band with the change under test, which is the exact
defect that invalidated iterations 008–024. Same seeds control instance variance; they do not rescue a
different independent variable.

**The baseline's zero is measured, not assumed.** With `econ_trigger` absent only the pre-expiry trigger
can fire, and iteration 024's arm A recorded `not_in_pre_expiry_window` on **378 of 378** requests —
eight parallel workers never leave an idle gap for a cache entry to approach expiry.

### What has to exist before the run

**No new code.** Every term arm A needs already ships: the mass-shrunk bucketed approval estimator, the
horizon credit, `reward_premium`, `min_pressure`, `min_inventory`, `min_later_turns` and
`keep_recheck_turns`. That is a reason to prefer this design over the three-arm one, whose agreement-gate
arm needed a new config key and a new counter.

What is needed is **one config file** — the baseline, identical to arm A's with `evidence` and
`econ_trigger` removed — and the per-pass readout that the stopping rule in §4 consumes.

## 4. Endpoints, margin, power and the stopping rule — all fixed now

**Primary:** task accuracy over all 15 environments, arm vs arm, **two-sided**. Margin declared here
because iteration 007's failure was declaring it afterwards.

- **Margin: 11 accuracy points**, which is what this design can actually resolve (below).
- **Secondary, declared NOW so it is not a post-hoc subgroup:** the same difference over the **12
  environments that are not degenerate**. Across 18 valid iteration-024 passes, `CanvasArrangeExamS2LEnv`,
  `CanvasListTestS2LEnv` and `WoocommerceNewWelcomeS2LEnv` were solved **0 times**; they contribute
  nothing to any arm difference and only dilute it.
- **Then, in order:** `expand_unresolved_missing`; cost per task; steps per task; cost per step;
  `summarize acted` (a real deferral signal for the first time now that `collapse` is gone).
- **`expand_unresolved_missing` is a first-class endpoint.** It is the direct measure of irreversible
  loss and the quantity that decides whether iteration 024's reward was bought with it — 175 in its arm
  B against 0 in arm A. An arm that wins accuracy while raising it has not demonstrated anything
  shippable.

### Power, measured from iteration 024's own passes

Pass-to-pass variability, each pass being 15 tasks, over 19 recorded passes (the one pass that scored
0.000 excluded as a wholesale failure):

| endpoint | mean | SD | SD/mean |
|---|---|---|---|
| accuracy | 0.520 | **0.087** | 16.8% |
| steps | 24.3 | 5.74 | 23.6% |
| cost | $40.89 | $17.02 | 41.6% |

Minimum detectable difference, two arms, k passes each, p<0.05 two-sided:

| endpoint | k=3 | k=5 | k=8 | iteration 024's effect |
|---|---|---|---|---|
| **accuracy (pts)** | 14 | **11** | 9 | **~13** |
| steps | 9.2 | 7.1 | 5.6 | ~4.7 |
| cost ($) | 27 | 21 | 17 | ~8.9 |

**So k=5 is the minimum that can see the effect being chased, and k=3 cannot** — it needs 14 points
where ~13 are expected, which makes "directional but not significant" the *expected* result of a
three-seed run rather than bad luck. **Accuracy is also the best-powered endpoint of the three**: steps
and cost have higher relative variance and neither resolves iteration 024's effect even at k=8.

Two structural facts about the noise, both measured: between-seed SD is only **0.048** while within-seed
SD is **0.091**, so instance identity is the small part and pairing arms on a seed buys little; and one
task is **6.7 accuracy points**, so a single-seed difference can only take values in multiples of 6.7 —
there is no observable "+5 points".

### The stopping rule

Read out after **every seed**, on the **cumulative** mean difference (arm A − baseline), not on the
seed in isolation.

- **Stop for futility if the running mean is below −6.7 points** — one whole task, which is also the
  granularity of the measurement.
- **The rule is futility-only.** It may end the run; it may **not** declare a win. The confirmatory
  two-sided test happens at k=5. Stopping early for lack of benefit does not inflate type-I error;
  looking, continuing because it looked good, then testing at 0.05, does.

Simulated 200,000 times at the measured one-seed difference SE of 12.3 points:

| threshold | abandons a true +13.3 | stops early if truly 0 | stops early if truly −13.3 |
|---|---|---|---|
| 0.0 | 11.6% | 69.0% | 99.6% |
| **−6.7** | **3.2%** | 33.1% | **93.3%** |
| −13.3 | 0.8% | 11.3% | 67.9% |

At −6.7 a genuinely harmful arm stops at seed **1.6** on average, saving ~3.4 of 5 passes — roughly
**$140**.

**A pass that fails is not a futility signal and must not enter the running mean.** One of iteration
024's 19 passes scored 0.000. Re-run, do not pool, if a pass scores 0.000 accuracy, or if arm A produces
fewer than 5 asks (§2's pre-flight applies per pass, not only at the start).

### Pre-registered reading

| outcome | conclusion | next |
|---|---|---|
| **A > baseline** beyond 11 points, `expand_unresolved_missing` flat | iteration 024's result reproduces at a correctly denominated band, and volume buys reward despite a selector no better than the null | then, and only then, arms B and C to ask whether selection quality adds anything |
| **A > baseline** but `expand_unresolved_missing` rises | the win is bought with irreversible loss, and iteration 024's 175 was the same effect | not shippable; the reversibility invariant comes first |
| **within 11 points**, running mean positive | not separated at this n. NOT equivalence | the effect, if any, is below what 15 environments can resolve; more seeds do not fix a 15-environment ceiling |
| **futility rule fires** (running mean < −6.7) | the fixed configuration is not better and may be worse | stop; ~$140 of the budget is unspent |
| fewer than 5 asks in a pass | the pass is invalid, not informative | re-run that pass; do not pool it |

## 5. Cost

Derived from measured per-pass cost, because five estimates in iteration 027 were wrong by 2–3x.

`results.json` records **$40.89 per pass** of 15 tasks (mean over 19 passes, SD $17.02). Two arms x 5
seeds = 10 passes ≈ **$409**, plus arm A's ask spend — iteration 024 spent $20.26 across 5 seeds ≈ **$20**.

**Budget ≈ $430**, less whatever the futility rule saves (~$140 in expectation on a harmful arm).

An earlier draft of this file said ~$190, from the iteration 024 page's "LOCA's measured $1.13/run" x 75
runs. That does not reconcile with $40.89 per 15-task pass ($2.73/task), and which quantity the $1.13
refers to is unresolved. The measured per-pass figure is used here and the discrepancy is flagged rather
than settled quietly.

## 5b. Amendment, 2026-09-16: the band is 128k and the pruner's horizon is credited

Written after the pre-flight and before any reward pass. No seed had been run, so nothing is
retro-fitted.

**Why the band moved.** At a 64k declared band this agent's requests EXCEED the window — 73,550
tokens measured, pressures of 1.15 and 3.62 — and `turnsRemainingAfter` returns 0 whenever
`reqAfter >= window`. That zeroes three terms at once: the econ trigger's `have`, the horizon credit,
and `selectAffordableDrops`' `S·T`. The measured cascade:

| stage | |
|---|---|
| candidates offered | 349 |
| declined by `min_inventory: 10` | **339** |
| asks made | **1** |
| verdicts | 10 |
| drops the model authorised | 9 |
| **pruned as unaffordable** | **8** |
| **tokens removed, whole pass** | **105** |

105 tokens against a baseline that removes 0 is not separable at any n, so the pre-flight did the job
it exists for: it cost ~$14 instead of $430.

At 128k the same 73,550-token request sits at 0.57 pressure with a real horizon. **This is also the
first mechanical account of iteration 024**: its window resolved to 1,000,000, so that request sat at
7% pressure and every term was healthy. Its result was not bought by a permissive gate in any
transferable sense — it was bought by a window large enough for the arithmetic to be non-degenerate.
Every gate tightening since iteration 024 was treating a symptom.

**Why the pruner changed.** `prefixRewriteNet` measured its horizon on the pre-removal request while
deciding whether to remove — #232's defect, in the one place #232 did not reach. The comment
justifying the asymmetry attributed it to coref's calibration, but coref never calls that function;
it reaches the break-even through `prefixRewritePays`, which passes `creditRemoval` false. So the
asymmetry protected nothing and cost 8 of 9 drops.

**What neither change claims.** 128k is not large enough for every request — the 3.62-pressure
request is still 1.81 at 128k and still degenerates. What improves is the common case, and a large
enough removal can now bring a heavy request back under the window rather than being refused for
having no runway.

**Also noted, and not yet a plan.** The pre-flight's two environments both scored 0.0000 where
iteration 024 scores them at 0.89 and 0.47. With 105 tokens removed and
`expand_unresolved_missing = 0`, the arm cannot be the cause; that is an environment failure in the
chosen pair and it means the pre-flight says nothing about task outcomes. A pre-flight condition for
"the tasks actually ran" is missing and should be added before the pair is trusted for anything but a
firing check.

## 6. What this cannot settle

- **Nothing about the 46% of mass that is `opaque`.** No offline instrument can score it (iteration 027
  §7), and this run does not separate it either: arm A may drop there, so any win is partly a win on mass
  nothing can audit. A paired run whose only difference is whether opaque candidates may be dropped is
  the only way to isolate it, and it is not this one.
- **Whether the reuse proxy's blindness matters.** The offline expectations in §3 are floors, and the
  proxy cannot see positional or non-identifier reuse at all.
- **Generalisation past LOCA.** One benchmark, and really 12 non-degenerate environments. SWE-bench is
  deliberately out of scope until a configuration beats the baseline here.
- **Whether the agent model matters.** Everything here runs the agent on `aws/claude-sonnet-5`, and the
  sweep necessarily uses the same model — its ask carries only an inventory and reads the outputs from
  that model's prompt cache, so `source: config` is refused by the component rather than merely
  discouraged. Running the agent on haiku is therefore a change to BOTH the agent and the adjudicator at
  once. It is ~3x cheaper per pass and the published table has haiku matching sonnet on live-kept at
  bulk (58% both, at 1/12 the cost per call), so it is worth one seed on its own — but it risks flooring
  the accuracy endpoint, since power lives in the 12 non-degenerate environments and a weaker agent
  pushes more of them to zero. Tracked as the next question after this iteration, not folded into it.
- **Whether selection quality matters at all.** That was the three-arm question and it is deferred: this
  run asks only whether the fixed configuration beats doing nothing. If it does, the agreement-gated arm
  becomes worth its own iteration; if it does not, that question is moot.
