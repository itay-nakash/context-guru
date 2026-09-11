# Iteration 027 — pre-registration: does the reward result survive an honest gate?

**Status: REGISTERED, nothing run.** Written before any number beyond a config-load check exists.

| | |
|---|---|
| binary | `cg-i027-proxy-v02`, SHA-256 (first 32) `827876e9e0893db05f6b4abebe073de6` — v01 ran the mechanism probe only; v02 adds the summarize decline labels |
| arms | `cfg-iter027-{A-baseline,B-merged}.yaml` |
| B differs by | **five lines** — `evidence`, `econ_trigger`, `reward_premium: 20`, `min_pressure: 0.20`, `min_inventory: 3` |
| pipeline | **`housellm` + `summarize`** — `collapse` removed from both arms, see [The pipeline is now the one being claimed](#the-pipeline-is-now-the-one-being-claimed) |
| band | 64k |
| design | 2 arms × 15 tasks × 5 seeds = 150 runs, interleaved A then B within each seed |
| funded | **step 0 and step 1 only** — see [Staging and budget](#staging-and-budget) |

## Why this iteration exists

Two facts established after iteration 026 stopped, both from logs that already existed:

**1. Iterations 008–024 never ran at their declared band.** The proxy resolved the model's real
1,000,000-token window, not the configured one, because an unreachable model-window document was answered
from a built-in table with no error, no log line and no counter. Iteration 024 logged `ctx_window:
1000000` on all 2,207 requests across ten passes. Consequences:

- `summarize`'s 0.78 trigger became **780,000** against a largest-ever request of 345,996, so it fired
  **zero times in either arm** — 4,015 invocations, all declined. **Iteration 024 had no compaction of
  any kind** (the agent's own `clear_tool_uses` is discarded by the gateway, separately established).
- the econ trigger's turn horizon is linear in the window, so it came out ~16× too long: `haveTurns` never
  fell below 32 and ran to 11,611. It authorised **626 asks**. The gate was never in a position to decline.

**2. Iteration 024's reward result is nonetheless the cleanest one this line of work has.** With no
compaction in either arm there is no compaction confound: B−A is the sweep alone. And the endpoints its
own results page never reported:

| iteration 024, 74 pairs | arm A | arm B | B−A |
|---|---|---|---|
| accuracy | 0.486 | **0.608** | **+25.0%** — 8 tasks better, 0 worse, clustered p = 0.0078 |
| cost / task | $2.808 | $3.419 | +21.8% |
| steps / task | 24.3 | 29.0 | +19.4% |
| cost / **step** | $0.1156 | $0.1179 | **+2%** |

The sweep's own overhead was 2%; the whole cost increase was **more steps**. The 8 tasks that gained
accuracy took **+10.2** steps on average, the 7 that did not took **−1.2**. **The mechanism is trajectory
headroom, not token savings** — and headroom appears nowhere in `S × T > 11.5 × W`.

So every gate tightening since iteration 024 was calibrated against a baseline that was accidentally
permissive, and iteration 026 — gate correctly denominated, floor at 0.70 — fired **zero** asks in a $65
run. This iteration asks whether the result reproduces with the arithmetic honest.

## What changed, and why each change is not optional

### In the binary

**`reward_premium` (default 1, this run 20).** The break-even values a removal at the cache reads it
saves. Iteration 024 spent **$20.26** to bank **$0.72** of them — 28:1 against — while accuracy rose 25%.
Two independent routes give the same size: sizing a premium to reproduce that run's firings needs ~28, and
dividing its spend by its banked savings gives 28. Priced at face value the gate authorises **19%** of the
removal value that produced the result; at 20 it reaches **53%**. It multiplies the *benefit*, so it also
shortens repayment for the adjudication call — intended, and asserted in a test.

**The turn horizon credits the removal it is pricing** ([#232](https://github.com/rossoctl/context-guru/issues/232)). It was measured on the request *as it
arrived*, which asks "how many turns remain if we do nothing" and then charges the removal against that
answer. On iteration 024's decisions the horizon was **exactly zero on 51 of 203 firings (25%)**, and
`ceil(need/premium) ≥ 1 > 0` refuses at any premium. **The premium handles the value side, the credit
handles the no-runway side, and neither alone reproduces the behaviour under test.** The growth *rate*
still comes from the pre-removal request; only the *room left* is credited. `coref` keeps the uncredited
form — its drop selection was calibrated against it.

**The decline-label counterfactual holds every term but the ask.** It used to reset the approval discount
too, so a batch refused by the discount could be counted as refused by the ask; under a premium above 1 it
would re-price every decline at premium 1 and send all of them to `prefix_rewrite_not_repaid`. Noted in
iteration 025's results, whose 368 / 751 split is therefore not directly comparable.

**Three model-info silences closed.** `fetch()` now checks the HTTP status (a 404 was indistinguishable
from a malformed document); the background refresh **reports** its error instead of discarding it; and an
explicitly-configured `MODEL_INFO_URL` is probed **synchronously at startup**, with the proxy refusing to
run if it does not load — the same treatment `MODEL_PRICES` already had. **Iteration 024 would not have
started.** `/stats` gained `model_info_unresolved` and Prometheus `cg_model_info_unresolved_total`.

### In the config

| knob | 026 | 027 | why |
|---|---|---|---|
| `min_pressure` | 0.70 | **0.20** | Counting only iteration 024's firings that still repay at a correct window (74 of 203): a 0.20 floor keeps 51 and costs 12% of the removal value; 0.70 keeps **four** and costs 67%. A floor is also a horizon cap — the horizon is `turns × (1/p − 1)`, so 0.70 caps it at 0.43×turns. |
| `min_inventory` | 7 | **3** | I raised it to 7 on the reasoning that a high floor admits only large requests and therefore large inventories. Iteration 024's firings commonly carried **three** candidates; 7 blocks a further 50 of its 203. |

Together, iteration 026's config would have permitted **1 of iteration 024's 203 firings** — 127 blocked
by the pressure floor, 50 by the inventory floor, 25 by the econ gate.

### The pipeline is now the one being claimed

**Amended after the mechanism probe, on the probe's own data, before any endpoint was read.** Every
iteration from 023 to 026 ran `housellm` plus `collapse` plus `summarize`. `housellm` is the shipped
baseline this work is measured against and already contains `extract_llm_sweep`; **no shipped preset
contains `collapse` and `summarize` together**, and nothing documented adding it.

Removing it, for four reasons the probe measured:

| | |
|---|---|
| its documented job | prevent upstream 400s on bodies of **2.6–14.8 MB** |
| largest request that reached it | **93,791** tokens, against sonnet-5's 1,000,000 |
| upstream failures | **0** in the probe, **0** in both arms of iteration 026 |
| its default threshold | **`max_tokens: 2000`** |
| what it removed | **544,336** tokens, against the sweep's **35,318** — 15× |

At `max_tokens: 2000` it capped every tool output above 2,000 tokens to 20 head + 20 tail lines with **no
judgement about whether the output was spent** — which is the question `extract_llm_sweep` is paid to
answer two positions earlier. Whatever the sweep declined to remove, collapse truncated anyway, so the
sweep's marginal value has been measured throughout this series against a baseline that had already
truncated every large output.

It also ran **before** `summarize`, and therefore before summarize's window fraction was evaluated. The
probe's two sessions separate cleanly:

| | collapse removed | summarize saw | its 0.90 trigger | summarize acted |
|---|---|---|---|---|
| 6 turns, session 1 | **54,589** per turn | 28,613–39,202 | no | 0 |
| 6 turns, session 2 | **0** | 81,385–81,580 | **fires** | 0 |

So where collapse acted it pre-empted summarize, and where it did not, summarize's trigger fired and
summarize declined anyway with **no gate recorded**. Both are fixed here: collapse is gone, and the five
silent decline paths in `summarize.go` now raise `summary_below_trigger`, `summary_span_empty`,
`summary_span_empty_after_expand_trim`, `summary_no_model`, `summary_span_below_min_tokens` and
`summary_empty_reply`.

**Consequence for the deferral row:** it becomes meaningful for the first time. With collapse present,
"summarize acted" largely answered "did collapse act". It stays *reported, not vetoing*.

**The risk, stated plainly:** an uncapped oversized output causing an upstream 400. Measured as absent on
this band and this task set — and that is **not** a claim about a 128k band or a different task set. If
step 2 moves off this configuration, check `capfail` before assuming it still holds.

### In the rig

`stage022.sh` refuses to start when the model-window document is unreachable (`:6980` is whichever
`http.server` got there first, whatever directory it serves), and after the pass it asserts the
**resolved** window off the proxy's own log — a document that fetches fine but does not name the run's
model id falls through to the fallback just as silently as a 404.

## Endpoints

### Primary — what a user would notice, in this order

1. **accuracy**, clustered by task (15 clusters), sign test on task means
2. **cost per task**, arm B against arm A
3. **steps per task**, as the latency proxy this benchmark supports

**A +20% cost increase is acceptable if accuracy improves.** Registered in advance, at the user's
direction, because the alternative — "cost must not regress" — makes iteration 024's result unpassable
while being the wrong test of a mechanism that buys solves by letting the agent run further. This is the
*only* honest place for that tolerance: 90% of iteration 024's cost increase was extra agent steps, which
no runtime gate can see coming.

### Mechanism — why the primary moved, never a substitute for it

- `model_info_unresolved` **must be 0** and the resolved `ctx_window` **must be 64000** on every pass.
  Nothing below means anything otherwise.
- **did it fire at all** — `cg.sweep.ask` count, `sweep_offered`. Iteration 026's answer was zero.
- `haveTurns` distribution on `cg.sweep.econ`, and `premium` on every row, so declines can be re-priced
  offline instead of re-run.
- `econ_ask_not_repaid` against `prefix_rewrite_not_repaid` — which cost is refusing now.
- `sweep_below_min_pressure` and `sweep_inventory_below_min` against `sweep_offered` — what the two
  floors cost at their new values.

### Reported, not vetoing

**Deferral.** `summarize` acted, arm B against arm A. This is the thesis iteration 026 was built for and
it has **never been observed**: iteration 024's summarize never fired at all, and iterations 023–026 ran
`collapse` ahead of it, which removed 15× more and disarmed its trigger before it was evaluated. This is
the first configuration in which the number can mean what it says — see [The pipeline is now the one being
claimed](#the-pipeline-is-now-the-one-being-claimed).

Two warnings that stand regardless. Iteration 026 seed 1 showed arm A 71 acted against arm B 15 — a 79%
reduction that looks exactly like deferral and cannot be, because arm B's sweep fired zero times; **at one
seed this figure is trajectory divergence.** And a decline is now labelled, so read *which* label:
`summary_span_below_min_tokens` and `summary_span_empty` mean the transcript's mass sits in the protected
tail, which no amount of sweeping will change. Reported, vetoes nothing.

## Staging and budget

| step | what | cost | answers |
|---|---|---|---|
| **0** | mechanism probe, arm B, 2 tasks | ~$10 | Did the window resolve? Did anything fire? |
| **1** | seed 1, arm A then B | ~$100 | Direction on the three endpoints. **Not significant.** |
| **2** | seeds 2–5 | ~$400 | The registered design. **Needs new budget and an explicit go-ahead.** |

Per-pass cost is estimated from iteration 024 ($41.57 arm A, $52.66 arm B) and is **uncertain in a known
direction**: arm A's `summarize` will now actually fire, which no prior iteration measured.

### The probe's tasks, and the selection error that nearly wasted it

**Amended before the probe ran, on no data from it.** The first choice was `AcademicWarningS2LEnv` and
`MachineOperatingS2LEnv`, picked as "the cheapest long transcripts" — 51 and 27 steps at $3.60 and $1.67.
Both are wrong, and wrong in the direction that would have produced a **false negative**:

`min_pressure: 0.20` gates on the request's size as a fraction of the window, i.e. **12,800 tokens of the
64k band**. Those two tasks average **8,865** and **7,827** tokens per request across iteration 024's five
seeds. Neither reaches the floor, so the probe would have fired nothing for a reason that has nothing to
do with the gate under test — and the pre-registered stop rule for "step 0 fires nothing" is *stop*.

**A long transcript is not a large request.** Step count and request size are nearly uncorrelated on this
benchmark: `AcademicWarningS2LEnv` runs 51 steps at 8.9k per request, `UpdateMaterialInventoryS2LEnv` runs
14 steps at 40.4k. Selecting a fixture for a size-gated mechanism on step count is selecting on the wrong
axis.

The corrected pair, chosen on **mean request size first**, then on whether iteration 024 showed a gain on
that task, then on cost:

| task | mean request | steps | iteration 024 cost | iteration 024 accuracy A → B |
|---|---|---|---|---|
| `SetConfCrDdlS2LEnv` | **28,252** (2.2× the floor) | 12 | $1.33 | 0.40 → 0.60 |
| `FilterLowSellingProductsS2LEnv` | **18,370** (1.4×) | 47 | $6.58 | 0.20 → 0.60, +31 steps |

Both cleared the floor comfortably in iteration 024 and both **gained** accuracy there, so a null result
is about the gate rather than about the task. Two rather than one because a single transcript shape can be
idiosyncratic; the second is the strongest step-delta gainer in the set, which is the shape the mechanism
is claimed to help. Seed 1, so every other parameter matches the three runs already on record.
`deploy/harbor/task-configs/i027-probe.json` on the eval box holds exactly those two, filtered from
`i024-64k-s1.json`.

**The remaining budget is about $100, so steps 0 and 1 exhaust it.** Step 2 is registered so that the
design is fixed in advance, not because it is funded.

### Stop rules, fixed now

- **Step 0 fires nothing** → stop. Do not pay for step 1. Read `haveTurns` and the two floor counters and
  come back with a diagnosis, not a bigger run.
- **Any pass reports `model_info_unresolved > 0` or a resolved window ≠ 64000** → discard that pass,
  do not compare it, fix the rig before continuing.
- **Step 1 shows accuracy worse in arm B on 3 or more of 15 tasks** → stop and report. Reproducing a
  reward *regression* is worth knowing and does not need four more seeds.
- **Step 1 costs more than 40% over arm A** → stop and report. Double the registered tolerance is a
  different finding, not a noisy version of this one.

## Pre-registered reading table

Written before the data exists.

| step 1 outcome | reading | next |
|---|---|---|
| fires, accuracy up, cost within +20% | the result survives an honest gate | ask for step 2's budget |
| fires, accuracy up, cost +20–40% | reproduces at a price above the registered tolerance | step 2, and the tolerance becomes the discussion |
| fires, accuracy flat | the iteration 024 result does not survive correct denomination | stop; the premium is refuted at 20 and the next question is whether any value works |
| fires, accuracy worse | the mechanism harms once the window is real | stop; write it up |
| **still fires nothing** | the gate is not the binding constraint and three iterations of gate work were misdirected | stop; the next iteration removes the econ gate rather than tuning it |

## Limits, stated in advance

- **No attribution.** Four terms move at once (premium, horizon credit, two floors). A B−A difference
  cannot be assigned to any one. A factorial is 40 passes this budget does not have; if the result
  reproduces, taking the terms apart is the next iteration's job.
- **One seed is directional, not significant.** Iteration 024's p = 0.0078 came from 5 seeds; 15 clusters
  at one seed can show a direction and nothing more. Seeds stabilise each cluster's mean — they are not
  independent observations, because they are the same 15 tasks.
- **`reward_premium` is a belief, not a measurement.** 28:1 is what iteration 024 *paid* while reward
  improved; it is not proof that each firing earned its cost. The premium is an aggregate claim, and the
  right test of it is whether reward reproduces.
- **No prior iteration's `summarize` behaviour is comparable**, because iteration 024's never fired and
  iterations 025–026 ran a different threshold. Arm A is the only baseline for it.
- **The harm-gate threshold is still unsettled** and deliberately deferred: it affects how this run is
  read, not how it is executed. At 15 clusters the Clopper-Pearson upper bound is 21.8% with zero
  worsened tasks, so a 25% veto cannot be cleared by any single-seed result.
