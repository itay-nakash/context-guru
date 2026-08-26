# Rule-based and learned TTL arms, scored on the rare event that actually pays

This extends [kv-cache-ttl.md](../how-to/kv-cache-ttl.md) (the exact ceiling, the arm
registry, "93% of the ceiling is 526 decisions out of 14,407 — 3.7% of requests") and
[kv-ttl-predictor-features.md](kv-ttl-predictor-features.md) (the `stop_reason` cluster
split, the per-tenant heterogeneity, the UTC hour structure). Both said what to build next:
a rule keyed on `stop_reason`, evaluated per tenant, before anything ML-shaped. This page
builds those arms, one learned arm on top, and scores all of them with rolling-origin time
splits and a session-level bootstrap.

Measured on the live deployment's own capture, read-only, on 2026-08-26: 66,779 requests,
17 tenants, spanning 2026-08-17 11:48 UTC to 2026-08-26 11:47 UTC (9 days — a wider capture
than either prior page, both of which used a 57-hour window). Tenant ids are pseudonymized
(`t01`..`t17`) throughout; the mapping never left the store.

## The short version

- **A one-line rule — ping only when the last turn's `stop_reason` is in the "actually
  done" cluster — buys 1.54% pooled savings vs `fixed-5m`, CI95 [0.60%, 2.79%].** That CI
  does not cross zero: on this window, this is a real, reachable saving from a rule with no
  training step at all.
- **Adding an hour-of-day gate on top buys almost nothing more** (1.57% vs 1.54%, CIs
  overlapping almost completely) — the tuned "good hours" window covers 17 of 24 UTC
  hours, so it rarely disagrees with `stop_reason` alone.
- **The logistic-regression arm wins pooled (1.69%, CI95 [0.79%, 2.94%]) but is not
  distinguishable from the rule arms** — its CI overlaps both rule arms' CIs almost
  entirely. Call this "no significant improvement over the free rule," not "the model
  wins."
- **The per-tenant-tuned `historical-probability` port is statistically indistinguishable
  from doing nothing** (−0.03%, CI95 [−0.08%, +0.02%], straddles zero) — and the reason is
  diagnosable, not a bug: see below.
- **The pooled rule is not a pooled fact.** It is a net win built from tenants who gain a
  lot (t06: +8.1%, t12: +18.5%) and tenants who lose a little from the extra pings (t04:
  −1.5%, t11: −0.13%). A deployment that ships the naive global gate is trading a loss for
  four tenants against a much larger gain for three others.

## Lineage: the stop_reason gate extends a decision already made, it doesn't correct one

`proxy/keepalive.go`'s `pingable()` already measured this and made a call:

> Deliberately NOT gated on the previous turn's `stop_reason`. `end_turn` looks like a
> session-end signal and is the opposite — P(gap > 300s) is 37.15% after it against 0.74%
> after `tool_use`, and 83.7% of the recoverable dollars sit behind it.

That decision (commit `50e3966`) is correct and this page does not revisit it: the shipped
`pingable()` still pings on every `stop_reason` once a session has a second turn. What it
never finished is the other half of the same cluster split — excluding the "looks done,
isn't" cluster (`stop`/``, 2.9–6.1% band rate) and the "still working" cluster
(`tool_use`/`stop_sequence`/`tool_calls`/`length`/`content_filter`, 0.0–0.6%), both well
under the ~8% break-even, from a schedule that currently pings all of them too. The
`stop-reason-gated` arm below is that other half: keep pinging on `end_turn`/`max_tokens`/
`refusal` (unchanged from the shipped call, and for the reason the shipped call already
gives), stop pinging on everything else.

## Method

**Rolling-origin split.** The window is cut at 60% of elapsed time (2026-08-22 21:23 UTC,
26,648 of 66,779 requests — traffic volume grew across the window, so 60% of *time* is only
40% of *rows*), then the remaining 40% is split into three consecutive test folds by time:

| fold | window (UTC) | requests |
|---|---|---:|
| train | 2026-08-17 11:48 → 08-22 21:23 | 26,648 |
| F1 | 08-22 21:23 → 08-24 02:11 | 4,867 |
| F2 | 08-24 02:11 → 08-25 06:59 | 8,627 |
| F3 | 08-25 06:59 → 08-26 11:47 | 26,637 |

Every threshold (`historical-probability`'s per-tenant P5m/P1h, the `stop-reason-x-hour`
good-hours set, the logistic regression's coefficients and its ping threshold) is fit or
tuned on `train` only, exactly the in-sample-for-tuning-only convention `kv_ttl_cost_model.py`
already uses. The `historical-probability` arm's own `History` accumulator (a Python port
of `kvcache.History`, see below) then keeps growing online through F1→F2→F3 in true
chronological order — thresholds are fixed once, but the statistics they read keep
accumulating, which is how the shipped mechanism would actually run in production. Each
fold is re-derived in isolation before scoring (`kv_ttl_cost_model.py`'s own convention: a
fold's "next request" never reaches outside the slice being scored), and every arm is
compared against `fixed-5m` computed the same way on the same fold.

**Bootstrap CI.** Rows inside one session are correlated, so the resampling unit is the
*conversation* — `(tenant, session, model)`, matching the cost model's own key — not the
row. Every conversation's total cost under the baseline and under each arm is computed once
(costs are additive across independent conversation states, so evaluating each conversation
in isolation reproduces the pooled total exactly), then 400 bootstrap replicates resample
conversations with replacement and take the delta. 11,684 conversation-fold-segments feed
the pooled CI.

**Historical-probability, ported.** `kvcache.History`/`Stats` (leak-free fallback chain
`user+model+bucket → user+model → user → model → global`, `minCell=6`, `kvcache.BucketOf`'s
four six-hour bands) is ported to Python verbatim rather than reinvented, observing each
conversation's just-closed gap — bucketed at the *previous* request's hour, exactly
`kvcache/simulate.go`'s `hist.Observe(r.User, r.Model, BucketAt(st.lastTS), gap)` — before
deciding the next request, in one global chronological pass. Per-tenant thresholds are a
small grid search (`P5m∈{0.5,0.7}`, `P1h∈{0.05,0.08,0.15,0.3}`) minimizing simulated
`train` cost per tenant; a tenant with fewer than 30 `actually_done`-cluster requests in
`train` falls back to the globally-tuned default `(0.7, 0.05)` instead of being tuned on too
little.

**Logistic regression.** A single flat classifier — `stop_cluster`, tenant, `request_hour`
sin/cos, weekday sin/cos, `Turn`, and the rolling-gap features (`previous_gap_seconds`,
`rolling_gap_median_seconds`, `ewma_gap_seconds`, `past_return_rate_{5,15,60}m`,
`requests_in_previous_{10,60}m`, `user_history_count`) reused *as a library* from
`kv_ttl_survival_predictor.py`'s own feature engineering rather than re-derived — predicting
`P(next gap lands in the 5m–1h band)` directly, fit once on `train`'s 22,633 labelled rows
(1,102 band events; rows whose outcome is right-censored are excluded from fitting, not
guessed at). Ping threshold (0.03) is grid-tuned on `train`. Coefficients are folded through
the fitted `StandardScaler`/`OneHotEncoder` into a plain `weight · x + intercept` a Go port
can apply with no preprocessing step, at
`/tmp/kvttl_logreg_v1_coefs.json`. A `GradientBoostingClassifier` is fit on the same
features purely for a feature-importance comparison and is never scored as a policy.

All four arms share the shipped keep-alive schedule (280 s/3,360 s intervals, ≤2 pings per
idle span), `Semantics()` defaults, and the `MinPrefixTokens = 20,000` gate
`HistoricalProbability` already ships with.

## Pooled results

Against `fixed-5m` (pooled baseline **$7,239.02** over 11,684 conversation-fold-segments):

| arm | Δ vs fixed-5m | % | 95% CI (%) | significant? |
|---|---:|---:|---:|---|
| `logreg-v1` | +$122.26 | **+1.69%** | [+0.79%, +2.94%] | yes |
| `stop-reason-x-hour` | +$113.44 | +1.57% | [+0.63%, +2.77%] | yes |
| `stop-reason-gated` | +$111.29 | +1.54% | [+0.60%, +2.79%] | yes |
| `historical-probability-tenant-tuned` | −$2.52 | −0.03% | [−0.08%, +0.02%] | **no** |

The three positive arms' CIs overlap almost completely — the data cannot distinguish
"gate on `stop_reason`", "gate on `stop_reason` and hour", and "gate on the logistic
regression's probability" from each other at this sample size. It can distinguish all three
from doing nothing, and it can distinguish `historical-probability-tenant-tuned` from
either: that arm's CI straddles zero, so **the honest reading is "not shown to help,"
not "hurts."**

Per fold (baseline totals: F1 $966.33, F2 $1,232.43, F3 $5,040.26):

| fold | stop-reason-gated | historical-probability | stop-reason-x-hour | logreg-v1 |
|---|---:|---:|---:|---:|
| F1 | +3.61% | −0.01% | +3.85% | +4.02% |
| F2 | +4.75% | +0.13% | +4.61% | +4.96% |
| F3 | +0.35% | −0.08% | +0.39% | +0.44% |

F3 (the largest fold by far, 26,637 requests — the last ~30 hours of the window) is
consistently the smallest per-fold saving for every arm. Every arm's own hit rate on F3
clusters around 58% against F1's ~82% (F2 sits at ~54%, closer to F3 than to F1) — since
every arm agrees with `fixed-5m` on the great majority of rows, that clustering is a good
proxy for the baseline's own hit rate too, and a lower one to begin with leaves less for any
arm to improve on with the same fixed ping budget. Read this as fold-to-fold traffic-mix
variance, not a trend: three folds are not enough to fit a slope to.

## Why the historical-probability port doesn't move

`HistoricalProbability`'s own `Stats` cells are keyed on `(tenant, model, hour-bucket)` —
not on `stop_reason`. A cell blends the ~92.5% of a tenant's turns that are `tool_use`/
`stop_sequence` (near-0% band rate) with the tiny minority that are `end_turn` (11.7–43.3%
band rate, per the features doc), and the blended `ReuseWithin(5m)` almost always clears
even the tuned `P5m` threshold — so the arm takes the "just write 5m" branch on nearly every
decision, indistinguishable from the baseline it's being compared to. This is the same
diagnosis `kv-cache-ttl.md` already made about the two ML arms it tried first ("both
machine-learned arms scored within a rounding error of `fixed-5m`... because 92.5% of gaps
close inside five minutes") — the shipped `Stats` mechanism inherits the identical blind
spot, tuned or not, because nothing about tuning its two thresholds lets it see the one
feature that actually splits the population. **The fix implied by this page's own data: key
`kvcache.History`'s cells on `stop_reason` cluster too, not just tenant/model/hour.** That
was out of scope here (a `.go` change, and a separate workstream owns it), but it's the
concrete next step this measurement points at.

## Per-tenant: the pooled win is not a pooled fact

Ten of 17 tenants clear the 30-`actually_done`-events-in-the-test-window bar; the other
seven (`t03, t05, t08, t09, t10, t13, t16`) are reported as skipped, not as zero:

| tenant | n (actually_done, test) | stop-reason-gated | historical-probability | stop-reason-x-hour | logreg-v1 |
|---|---:|---:|---:|---:|---:|
| t12 | 36 | **+18.5%** | +0.17% | +18.5% | **+18.9%** |
| t06 | 270 | +8.1% | −2.2% | +8.1% | +8.2% |
| t01 | 274 | +5.9% | +0.3% | +6.1% | +5.8% |
| t02 | 47 | −1.8% | 0.0% | +1.3% | +1.4% |
| t14 | 141 | +4.1% | 0.0% | +3.9% | +4.1% |
| t07 | 127 | +3.0% | 0.0% | +3.0% | +4.4% |
| t15 | 390 | +1.1% | +0.2% | +1.0% | +1.1% |
| t17 | 459 | −0.4% | 0.0% | −0.4% | −0.4% |
| t11 | 2,126 | −0.13% | 0.0% | −0.11% | −0.02% |
| t04 | 285 | **−1.5%** | −0.15% | **−1.5%** | −1.5% |

`t01`, `t06`, and `t12` — three of the ten with enough signal — carry almost all of the
pooled gain, echoing `kv-ttl-predictor-features.md`'s own finding that these same three sit
at the *high* end of the P(band | end_turn) range (41.4%, 29.3%, 37.8% respectively).
`t04` and `t11` sit at the low end (11.6%, 0.76%) and the naive `stop-reason-gated`/
`stop-reason-x-hour` rules lose a little money on them — the extra pings cost more than the
rare rescued gap is worth. `historical-probability-tenant-tuned` correctly avoids that loss
(0% or a small positive on every low-heterogeneity tenant) but, per the diagnosis above,
also fails to capture the gain where it exists. **The two arms are complementary failure
modes, not competitors**: a rule that combined `stop_reason` gating with a per-tenant
gate — ping only when *both* the turn looks done *and* this tenant's own history clears
break-even — is the next thing worth building, and would need neither a new mechanism nor a
new measurement, only wiring the two already-built pieces together.

## Feature importance

Standardized logistic-regression coefficients (magnitude) and `GradientBoostingClassifier`
importances, same features, same label:

| rank | logistic regression (|standardized coef|) | gradient-boosted trees (importance) |
|---:|---|---|
| 1 | `turn` (−2.99) | `turn` (0.198) |
| 2 | `stop_cluster=still_working` (−2.09) | `stop_cluster=still_working` (0.197) |
| 3 | `user_id=t02` (−1.16) | `request_hour_sin` (0.150) |
| 4 | `user_id=t04` (−1.08) | `user_id=t01` (0.093) |
| 5 | `user_id=t01` (+0.92) | `previous_gap_seconds` (0.064) |

Both models agree on the top two: **`Turn`** (later turns in a conversation are less likely
to land in the rescuable band — consistent with the shipped `pingable()`'s own finding that
single-request sessions are "79% of the pings and 0.9% of the value") and **excluding the
"still working" cluster** is the single strongest signal either model found, ahead of tenant
identity, ahead of time-of-day, ahead of every rolling-gap feature the survival predictor's
engineering produces. That is the same conclusion the rule arms reached by hand: the
`stop_reason` cluster split is doing most of the work, and everything past it is a smaller
correction.

## The honest downside

- **The overlapping CIs are the finding, not a caveat on it.** With three test folds and
  the ~3.7%-of-requests rare-event structure this whole study inherits from
  `kv-cache-ttl.md`, this measurement cannot distinguish the three positive arms from each
  other. It can distinguish all three from `historical-probability-tenant-tuned` and from
  doing nothing. Reporting "logreg wins" without the CI would overclaim.
- **Per-tenant figures are point estimates with no per-tenant CI.** The pooled bootstrap
  resamples conversations across the whole test population; a per-tenant CI would need its
  own resample restricted to that tenant's conversations, which was not built here. Read
  the per-tenant table as directional, especially for `t02` (n=47) and `t12` (n=36) — both
  near the 30-event reporting floor.
- **Thresholds are tuned once on `train`, not re-tuned per fold.** The `historical-
  probability` `History` accumulator keeps growing online across F1→F2→F3 (the realistic
  production behavior), but its per-tenant thresholds, the good-hours set, and the logistic
  regression's coefficients and ping threshold are all fixed after the single 60% cut. A
  system that re-tuned as it went might do better or worse; this measures the "set once,
  let the statistics grow" policy, not a continuously-retrained one.
- **`agent`/`model` were left out of the learned arm's feature set** even though
  `kv-ttl-predictor-features.md` flagged `agent` as a genuinely distinct population — the
  task scope named `stop_reason cluster, tenant, hour, day-of-week, Turn, rolling gap
  stats` and this page stayed inside it. Adding `agent` is a candidate for a v2 model, not
  a defect in this one.
- **The 8% break-even used to tune `stop-reason-x-hour`'s good-hours set is the same
  simplified derivation `kv-ttl-predictor-features.md` already flagged** (`read_rate /
  write_rate`, ignoring the ping-itself-being-wasted term) — a coarse cut for selecting
  hours, not a claim that 8% is the exact indifference point.
- **The window is 9 days, one deployment, and traffic volume grew across it** (train is
  60% of time but only 40% of rows) — every fold-level number above is this window's, and
  F3's lower per-arm savings could be this window's late-window traffic mix rather than a
  general property of "later in a service's life."
- **The logistic regression's exported coefficients are a v1, not yet ported.** They are a
  faithful `weight·x+intercept` a Go port can apply directly, but nothing here builds that
  port — this page stops at scoring the Python side.

## Files

- `deploy/harbor/kv_ttl_predictor_arms.py` — the four arms, the `History` port, the
  rolling-origin harness, the bootstrap, and the feature/coefficient export. Runs read-only
  as `cg` (see its own docstring); reusable exactly as written, with the DB-read and
  aggregate-only-output structure kept explicit in the script itself.
- `/tmp/kvttl_logreg_v1_coefs.json` — the flattened logistic-regression weights (feature
  name → weight, plus intercept), for a future Go port of `kvcache.Predictor`.
- `/tmp/kv_ttl_arms_result.json` — the full aggregate result set (pooled, per-fold,
  per-tenant with pseudonymized ids, feature importances) behind every table on this page.

## Related

- [Choose a cache TTL, and know what it is worth](../how-to/kv-cache-ttl.md) — the exact
  ceiling, the arm registry, the rare-event finding this page's whole method follows from.
- [What could predict the next request's timing](kv-ttl-predictor-features.md) — the
  `stop_reason` cluster split, the per-tenant heterogeneity table, the hour-of-day table
  this page's `stop-reason-x-hour` arm tunes against.
- `deploy/harbor/kv_ttl_survival_predictor.py` — the feature engineering the logistic
  regression reuses as a library.
- `deploy/harbor/kv_ttl_cost_model.py` — the scorer every arm on this page is priced through.
