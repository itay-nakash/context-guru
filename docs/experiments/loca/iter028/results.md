# LOCA — iteration 028

**One sentence.** The band was raised to 128k so the horizon arithmetic would stop returning zero, the
sweep then removed 322,939 tokens across 105 drops with no irreversible loss and no accuracy change —
and the iteration was **stopped after one seed** because three separate readings established that this
benchmark cannot price the feature, the last of them by refuting a comparison this document had already
drafted as a finding.

| | |
|---|---|
| arms | `cfg-iter028-{baseline,A}.yaml`, differing in exactly two keys: `evidence: true`, `econ_trigger: true` |
| band | **128k** (iteration 024 ran at a declared 64k that resolved to 1,000,000) |
| seeds | **1 of 5.** Stopped by decision, not by a futility rule — the rule permitted continuing. |
| spend | **$151** over seven passes |
| result | accuracy **0.333 vs 0.333**; 322,939 tokens removed; `expand_unresolved_missing` 0 → 0 |
| verdict | non-inferior on accuracy, **break-even on cache economics, and unpriceable on this workload** |

## 1. What the seed measured

| | baseline | arm A |
|---|---|---|
| accuracy | 0.3333 (5/15) | 0.3333 (5/15) |
| steps | 16.5 | 19.1 |
| cost | $28.80 | $29.97 (+4.0%) |
| `acted` | 0 | 12 |
| verdicts / drops | 0 | 176 / 105 |
| `saved_tokens` | 0 | **322,939** |
| `expand_unresolved_missing` | 0 | **0** |

The sweep acted, at scale, and broke nothing. That is the result worth keeping.

**The identical means conceal a rotating cast, and that turned out to be the story.** Arm A gained
CourseAssistant and MachineOperating and lost SetConfCrDdl and ExcelMarketResearch. A first draft of
this section read that as "the mean conceals real movement". It does not: a third pass, the
bounded-thinking baseline of §3, also scored exactly 5/15 with a *third* composition. Across the three,
**one** environment (AcademicWarning) solved in all of them and **seven** solved in at least one. The
count is stable and the cast is close to a coin flip, so a 2-gain/2-loss swap is this rig's noise floor,
not a signal.

## 2. The truncation finding, and how much of it survived

`--max-tokens` defaults to 4096 and it is a hard wall: 61 responses landed on exactly 4096, the
next-highest was 3593. LOCA adds no `thinking` field under `--no-enable-thinking`
(`inference/run_claude_api.py:1147` only *adds* one when enabled), so Sonnet 5 applies its own adaptive
default and thinks anyway — the banner prints `Extended thinking: DISABLED` while 168 of 243 responses
carried thinking blocks.

Three consequences, in descending order of how well they held up:

1. **A truncated response corrupts the next tool call.** The `tool_use` JSON is cut mid-object and the
   tool fails schema validation. 93 of 96 validation failures in the baseline and 120 of 123 in arm A
   landed immediately after a truncated response. This one is solid.
2. **An episode whose *final* response was truncated never scored** — 0 of 81 across 13 passes, against
   94% of survivors solving. But 859 of 940 truncations (91%) were survived, so "truncation kills the
   episode" is true only of the terminal ones. An earlier draft of this document stated the general
   claim; it is wrong.
3. **Iteration 024's arms differed in kill rate**, 37/75 against 26/75 — 14.7 accuracy points available
   with no selection effect, against a reported effect of about the same size. This remains a live
   confound for that iteration. It is **not** established as the mechanism: §3 shows kill rate and
   accuracy moving independently.

## 3. Two rig changes, both probed before committing, both negative

The purpose of probing was to avoid buying five seeds on a change measured once.

### `--max-tokens 8192` — actively harmful

| | 4096 | 8192 |
|---|---|---|
| NhlB2bAnalysis truncations | 5 | **16** |
| responses at the wall | 61 @ 4096 | 16 @ 8192 |
| NhlB2bAnalysis cost | $2.49 | **$18.02** |
| accuracy, both tasks | 0.000 | 0.000 |

Thinking expanded to fill the larger budget. $19.19 for a clean negative.

### Bounded thinking — no effect on the rig, despite a clean gateway measurement

On the gateway, same prompt at `max_tokens: 4096`, three repetitions each:

| | output tokens | hit the cap |
|---|---|---|
| field omitted (the rig) | 4096, 4096, 4096 | **3/3** |
| `thinking {enabled, budget_tokens: 1024}` | 1327, 1279, 1729 | 0/3 |
| `thinking {enabled, budget_tokens: 2048}` | 1746, 1459, 1405 | 0/3 |
| `thinking {disabled}` | 1143, 1422, 1419 | 0/3 |

1024 and 2048 produce the same output, so the bound is not what binds — *having* one is. Hence the
counter-intuitive shape: thinking is turned **on** to bound it, because "off" here means "unbounded
adaptive". No patch is needed; `--enable-thinking --thinking-budget-tokens N` already emits the measured
shape.

**It did not transfer.** Like-for-like on the same two environments, truncation went 17.6% → 15.9%, and
over the full 15 tasks the bounded pass truncated **more** (29.8% vs 23.5%) at the same accuracy (5/15)
and the same cost ($29.18 vs $28.80). The gateway prompt had no tools; in the rig the budget is consumed
by tool-call payloads as well as thinking. `LOCA_THINKING_BUDGET` is therefore shipped defaulting to
empty and is **not** recommended.

One trap worth recording: `thinking.adaptive.effort` is rejected 400 (*"Extra inputs are not
permitted"*), while a **top-level `effort`** returns 200 and is **silently ignored** — thinking still
capped 3/3. `output_config: {effort: low}` does work (1532, 1110, 1342) but LOCA has no flag for it.

## 4. Haiku as the agent: worse and more expensive

Asked because the sweep needs Sonnet for the adjudication but the agent could in principle be cheaper.

| | sonnet | haiku |
|---|---|---|
| solved, live 12 | **5** | **1** |
| mean accuracy | 0.3333 | 0.1333 |
| total steps | 247 | **514** |
| cost | $28.80 | **$33.16** |

Half the per-token price, 2× the steps, higher total. Paired McNemar p = 0.375, so one seed does not
establish it — but the direction plus the cost closes the question: an agent that cannot finish the work
gives the sweep nothing to work with. Haiku also killed **zero** episodes and still solved 2/15, which is
independent evidence that surviving is not what determines solving.

Incidentally, haiku solved **CanvasArrangeExam** — one of the three environments recorded as constant-zero
across nineteen iteration-024 passes — completely, at 35/35 courses matched. "Never scores" was a claim
about sonnet at a 4096-token output budget, not about the task. The degenerate list is now annotated as
such in `locaterm.py`.

## 5. Why the iteration was stopped: the workload cannot price this feature

Pressure is **not** the problem. Per environment, 9 of 15 peak at ≥70% of the 128k window, several above
100% (UpdateMaterialInventory 123%, SetConfCrDdl 99%, ABTesting 94%).

The problem is supply against a calibrated floor:

| run level (286 runs) | |
|---|---|
| `not_in_pre_expiry_window` | 286 (100%) — trigger one is structurally dead under 8 parallel workers, as the config comment already recorded for iterations 022 and 024 |
| `sweep_below_min_pressure` | 198 (69%) — no collection below 0.20 pressure |
| runs that collect | ~88 (31%) |

| candidate level (1,158 at depth → 441 offered) | |
|---|---|
| `sweep_inventory_below_min` | **253** — 57% of offered, discarded whole |
| `sweep_over_ask_cap` | 186 — trimmed, with no second ask for the remainder (#132) |
| asks | 15 |
| verdicts → drops | 176 → 105 |
| acted | 12 requests, 8.75 drops per invalidation |

**441 offered over ~88 collecting runs is ~1.5 settled candidates per run, against `min_inventory: 10`.**
About 170 runs carried 1–9 and asked nothing; about 15 carried ≥10 and asked. The component is not
malfunctioning — it is waiting for a batch size its own measurements say it can judge (6–14% live-kept at
one candidate, 58% at ~15), and this workload delivers that roughly once per task.

### The comparison this section originally made, and why it was withdrawn

A draft reported that the offline corpus supplies a median of 8 candidates per request and clears the
floor in 44% of them, against LOCA's 1.5 and ~5%, and concluded that the benchmark under-supplies the
feature relative to real sessions.

**Every corpus on the eval box is LOCA-derived.** All six carry `conv` values of the form
`inf_claude_api_…|<LOCAEnvironment>`. The comparison was LOCA against LOCA. What the 8-vs-1.5 gap
actually measures is a looser candidate *definition* in the corpus builder — the live component
additionally excludes already-decided outputs, marker-present ones, `kept_verbatim`, anything under
`min_tokens`, and anything with fewer than `min_later_turns` turns after it.

The consequence is larger than the retraction: **`min_inventory: 10`, the `min_tokens` and
`min_later_turns` floors, the 6–14%-vs-58% decision-quality curve, and the 8,105-decision selection
experiment are all calibrated on LOCA transcripts.** The feature's thresholds and its only validation
share one source, and the feature has never been measured against a workload independent of the one it
was tuned on. That, not a fifth seed, is what iteration 028 established.

## 6. The economics, exactly, and what they do and do not say

| | tokens | $/M | cost |
|---|---|---|---|
| cache reads saved | −1,385,986 | 0.20 | −$0.277 |
| cache creation forced | +126,296 | 2.50 (incremental 2.30) | +$0.290 |
| **cache subtotal** | | | **+$0.013** |
| output tokens (more steps) | +97,780 | 10.00 | +$0.978 |
| **total** | | | **+$1.16** |

The realised saved-read : forced-write ratio is **11.0** against the code's own break-even constant
`cacheWriteX = 11.5`. The rewrite cost is set entirely by the **shallowest** removal
(`prefix_econ.go:158-163`; `rewritten -= saved`), so once an invalidation is paid for, further removals
downstream of it are free. Eight drops per invalidation is what this workload allowed.

Paired per environment the dollar difference is **+$0.077 ± $0.80** — indistinguishable from zero. The
$1.16 is almost entirely output tokens, i.e. steps, i.e. the same rotating cast as §1.

**This verdict is workload-scoped.** The gate is `S·T > 11.5·W`: LOCA supplies thin `S` (1.5
candidates/run) and short `T` (10–30 step tasks). Both terms are structurally small here, so 11.0 is not
evidence that the mechanism is marginal — it is evidence that this instrument cannot separate the two.

## 7. And the comparator was inert the whole time

UpdateMaterialInventory peaked at **158,006 tokens against a 128,000 `--clear-trigger-tokens`**, and LOCA
logged **zero** `CONTEXT MANAGEMENT` events — in all three 15-task passes. The harness's own context
clearing, which is both the thing the sweep should be compared against and the mechanism through which
"a removed token buys headroom" (`reward_premium: 20`) would pay off, has never fired in any pass this
project has run. Filed as benchmark-specific; it means the headroom half of the value claim currently has
no instrument at all.

## 8. What was fixed here, and what it cost to find

| fix | why it mattered |
|---|---|
| `prefixRewriteNet` credits the horizon of the removal (`81ec2dd`) | pruned 8 of 9 authorised drops in the 64k pre-flight; after it, 0 of 10 and `saved_tokens` 105 → 67,323 |
| mass-weighted bucketed approval shrinkage (`f980bc5`) | approval floored at 0.05 after three asks shut the component down |
| JSONL verdict fallback (`1ffdf2f`) | `sweep_answered_via_prose` 7, `sweep_fallback_used` 1 in the seed |
| readout resolves a pass by its own log (`f29467b`) | it globbed `outputs/*<tag>*`, which matches nothing — the seed's first readout said "no passes on disk" with both passes complete and paid for. Keying on the task-config name instead would have resolved **both arms to the same directory** and printed a clean zero. |
| kill rate reported per arm, with a warning when the arms differ (`f29467b`) | so the iteration-024 confound cannot be read as an effect again |

## 9. Errors in this iteration's own reasoning

Recorded because two of them were corrected only after being presented as findings.

1. **"Truncation kills the episode."** True of terminal truncations only; 91% are survived (§2).
2. **"13 of 15 tasks never approach the window."** Read off proxy session ids, which do not map to
   tasks. Per environment, 9 of 15 peak ≥70% (§5).
3. **"Real sessions supply 5× the candidates."** Circular — the corpus is LOCA-derived (§5).
4. **"Fix `min_inventory`."** Proposed twice, and the code's own comment refutes it: below ten a drop is
   a guess, and ten is a measured inflection.
5. **A cost estimate 2× low** on the 8192 probe ($19.19 against ~$10).

## 10. What follows

1. **Do not run seeds 2–5.** Detecting the observed 4% cost difference needs ~107 seeds (~$6,300); the
   affordable resolution (±27% at 5 seeds) is an order of magnitude coarser than the effect.
2. **Get non-LOCA traffic.** Enablement guidance is in
   [`extract_llm_sweep`](../../../components/extract_llm_sweep.md#enabling-it-on-real-traffic).
3. **SWE-bench after the PR merges** — longer horizons and larger tool outputs raise both `S` and `T`.
4. **The rig defects are benchmark-specific and low priority.** They do not affect shipped behaviour.
