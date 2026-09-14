# LOCA — iteration 028 (preregistration)

**The question, in one sentence.** Does reward track how MUCH the sweep removes, or how WELL it
chooses — because iteration 027 measured a selector no better than dropping everything, and
iteration 024 won reward with one just like it.

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
iteration 024's own ledger: **`expand_unresolved_missing` = 175 in arm B against 0 in arm A** — content
the agent asked for and did not get back.

Offline scoring cannot separate these. It ranks selectors against a proxy for reuse; it has no access
to whether the agent was harmed. This is the measurement the whole line has been deferring.

## 2. It will fire often enough to be measurable, and that is simulated rather than hoped

Iteration 026 fired **zero** asks and measured nothing. The precondition for spending here is a
configuration whose firing rate is known in advance. Simulated over 384 decision points with the
shipped gate functions (`TestEconGatePassRateByBand`), as a fraction of **all** decision points:

| window | `min_inventory` | qualify | ask/all | dead tokens behind asks |
|---|---|---|---|---|
| **64k** | **10** | 44% | **36%** | 5.54M |
| 128k | 10 | 44% | 43% | 7.40M |

Iteration 024's measured rate was **28%** (626 asks / 2,207 requests). So 64k with the fixed estimator
exceeds the only firing rate that ever produced reward, **without** the 1,000,000-token window that
made iteration 024's gate permissive by accident.

The estimator holds because of the floor, not despite it: the rich-inventory run measured the
adjudicator approving **~85% of offered mass**, where the three thin asks that shut the component down
approved **1.24%**. `min_inventory: 10` feeds the ledger the evidence that keeps the gate open.

**Pre-flight gate — do not start the run unless all four hold.** Iterations 008–024 all ran outside
their declared band without anyone noticing, and iteration 026 spent a full run to discover a zero.

1. `model_info_unresolved` = 0 and the resolved `ctx_window` printed as **64000**.
2. A probe pass shows `sweep_offered` > 0 and at least **five** asks across the pass.
3. `sweep_inventory_below_min` and `sweep_offered` both reported, so the half declined by the floor is
   visible rather than inferred.
4. On the probe, arm B's `sweep_index_disagreed` (new counter, §3) is **> 0** — the arms must actually
   differ on live traffic, or the run measures two identical things.

## 3. Arms

All arms share: pipeline = `housellm` + `summarize`, **no `collapse`**; band 64k; `evidence: true`;
`econ_trigger: true`; `reward_premium: 20`; `min_pressure: 0.20`; `min_inventory: 10`;
`min_later_turns: 3`; `keep_recheck_turns: 4`; the mass-shrunk bucketed approval estimator.

| arm | selection | expected on iteration 027's offline numbers |
|---|---|---|
| **A — volume** | the model's verdict alone, today's drop path | 85.4% of tokens, false-drop 36.8%, live-kept 16.1% |
| **B — agreement** | drop only where the index says `unreferenced` **and** the model says drop | 58.0% of tokens, false-drop 8.0%, live-kept 88.7% |
| **C — free** | `coref` alone: deterministic index, **no asks at all** | 70.6% of tokens, false-drop 10.7%, live-kept 76.8% |

Arm C is the benchmark that matters. If neither LLM arm beats a component that costs nothing, the
component's case is closed regardless of how A and B compare.

**Arm B is NOT the refuted pre-filter, and the distinction is load-bearing.** `extract_sweep.go`
records why: that design offered the model only what the index had already judged spent, so its
inventory was starved and the comparison it is good at was destroyed (`4ca1f13`). Arm B offers the
**full** inventory — the model sees and ranks everything, which is the condition its judgement needs —
and the index acts only as a **veto on the drop**. Index proposes and model disposes is the refuted
shape; here the model proposes and the index vetoes.

**What arm B cannot do, stated so it is not read as a defect later.** `opaque` candidates are never
`unreferenced`, so arm B can never remove them: **46% of token mass is out of its reach by
construction**. Arm A can and does. If A wins, part of the win may come from mass B is structurally
forbidden to touch, and that is a real confound between "volume" and "reach", not a clean contrast.

### Code required before the run

- A config key on `extract_llm_sweep` gating the drop on index agreement (arm B), with the index verdict
  read from the record the evidence line already computes — not recomputed.
- A counter, `sweep_index_disagreed`, incremented when the model says drop and the index does not.
  Without it a null result cannot be told from an arm that never diverged, which is the failure
  iteration 026 spent a run on.

## 4. Endpoints, margin and predictions — fixed now

**Primary:** task accuracy, arm vs arm. **Two-sided**, and the margin is declared here because
iteration 007's failure was declaring it afterwards.

- **Margin: 8 percentage points of accuracy.** A difference smaller than that is reported as "not
  separated at this n", never as equivalence.
- **Secondary, in this order:** cost per task; steps per task; cost per step; `expand_unresolved_missing`;
  `summarize acted` (which is a real deferral signal for the first time now that `collapse` is gone).
- **`expand_unresolved_missing` is a first-class endpoint, not a footnote.** It is the direct measure of
  irreversible loss, and it is the quantity that decides whether iteration 024's reward was bought with
  it. An arm that wins accuracy while raising it has not demonstrated a shippable mechanism.

**Power, honestly.** 15 tasks × 5 seeds = 75 runs per arm, but the seeds are 5 draws of the **same 15
tasks**, so observations are clustered and effective n is nearer 15 than 75. At that n only large
effects resolve: iteration 008's own figures put the detectable harm bound at ~10% for n=30 and ~6% for
n=45. **8 points is therefore near the edge of what this design can see**, and a null is the most
likely single outcome. Reported as such rather than dressed up.

### Pre-registered reading

| outcome | conclusion | next |
|---|---|---|
| **A > C** beyond the margin, `expand_unresolved_missing` flat | volume buys reward and selection quality is a red herring; the offline scorer has been measuring the wrong thing | ship the sweep; retire the index-vs-model framing |
| **A > C** but `expand_unresolved_missing` rises | the win is bought with irreversible loss; iteration 024's 175 is the same effect | not shippable as is; the reversibility invariant comes first |
| **B > C** beyond the margin | selection quality is what pays, and the LLM's value is as a **veto** on a deterministic proposer | pursue agreement-gating; drop arm A's design |
| **B ≈ A ≈ C** within the margin | the LLM adds nothing reward can see at this n, and C costs nothing | close the LLM sweep for selection; the index is the selector |
| **C > A and C > B** | the asks are net harmful, not merely unprofitable | close it, and record that offline triage was right |
| the run fires < 5 asks/pass | the pre-flight gate failed and nothing is concluded | fix firing before spending again |

## 5. Cost, and why the figure is a range

Derived rather than asserted, because four cost estimates in iteration 027 were wrong by 2–3x.

- Agent traffic: LOCA's **measured $1.13/run** (iteration 024) × 75 runs ≈ **$85/arm**.
- Ask spend for an LLM arm: iteration 024 spent **$20.26** on 626 asks over 75 runs ≈ **$0.27/run**, so
  ≈ **$20/arm**. Arm C spends **$0**.
- Bottom-up total for three arms ≈ **$190**.
- But the plan file's own estimate for a *two*-arm 32k reward run was **$340**, i.e. ~$170/arm — twice
  the bottom-up figure.

**Budget $200–$400 and treat the lower end as optimistic.** Two mitigations, both to be decided before
launch and recorded here: drop to 3 seeds (~40% less, and the seeds are the correlated dimension, so
little power is lost), or drop arm A and test only agreement-gating against free — which halves the
LLM spend but forfeits the volume-versus-quality contrast that is the entire question.

## 6. What this cannot settle

- **Nothing about the 46% of mass that is `opaque`.** No offline instrument can score it (iteration 027
  §7) and arm B cannot touch it. A separate paired run whose only difference is whether opaque
  candidates may be dropped is the only way, and it is not this one.
- **Whether the reuse proxy's blindness matters.** The offline expectations in §3 are floors, and the
  proxy cannot see positional or non-identifier reuse at all.
- **Generalisation past LOCA.** One benchmark, 15 tasks. SWE-bench is deliberately out of scope until a
  configuration beats arm C here.
