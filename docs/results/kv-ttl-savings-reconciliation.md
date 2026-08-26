# Why the Strategies tab and the Overview page show different keep-alive savings

Reported by: a manager who felt the Strategies tab and the Overview/dashboard page were
"not synced" on savings numbers. This reconciles them with real numbers from the live DB.

## The three places a "keep-alive saving" number is computed

| Figure | Source | Scope | Time window |
|---|---|---|---|
| `Overview.KeepAliveSavedUSD` (`dash/overview.go:526`) | `SUM(r.keepalive_saved_usd)` over agent rows (`keepalive=0`) | Whatever `Filter` the page is showing (all tenants or one, per `TenantAll`) | **Respects the dashboard's selected range** (`Filter.Since`/`Until`) |
| `KeepAliveLedger.SavedUSD` (`dash/keepalive.go:139`) | Same query, same `Filter` | Same as above | Same as above — this is the Keep-Alive *tab*'s own ledger, and it agrees with Overview by construction (identical SQL, identical filter) |
| `StrategyLedgerRow.SavedUSD` (`dash/keepalivestrategy.go:70-74`) | `SUM(keepalive_saved_usd) FROM requests WHERE tenant_id = ? AND keepalive_saved_usd > 0`, one query **per tenant that had a ping under this strategy** | Every tenant the strategy's pings touched, **for that tenant's entire history** | **No `since`/`until` at all — always unconditional, all-time.** `StrategyLedger` never calls `Filter.where()`. |

So there are two independent, structural reasons the Strategies tab can disagree with
Overview, and only one of them is already documented in the UI.

## Reproduction (live DB, read-only, aggregates only)

Ran the Overview SQL and the StrategyLedger SQL against the live `cg.db` as `cg`
(`mode=ro`, no raw rows or tenant ids left the query process; tenant ids below are
pseudonymized t01, t02, ... and strategy ids s1, s2, s3):

```
1. Overview-style ledger, ALL-TIME, all tenants
   keepalive_saved_usd (agent rows):        $28.1283  (21 rescued requests)
   pings: 145   ping_usd: $23.2304
   net_usd:                                 $4.8979

2. Same ledger, LAST-24H window (what Overview shows with that range selected)
   keepalive_saved_usd:                     $25.2329  (19 rescued requests)
   pings: 143   ping_usd: $22.9779
   net_usd:                                 $2.2550

3. StrategyLedger reproduction — no time filter, ever
   strategy s1: tenants=10 pings=104 ping_usd=$11.1655  saved_usd=$27.8477  net=$16.6822
   strategy s2: tenants=6  pings=28  ping_usd=$9.2537   saved_usd=$15.8526  net=$6.5989
   strategy s3: tenants=3  pings=11  ping_usd=$2.5587   saved_usd=$11.4037  net=$8.8450

4. Reconciliation
   SUM of SavedUSD across all 3 strategy ledgers:            $55.1040
   Overview all-time SavedUSD (same 21 rescued requests):    $28.1283
   difference:                                               $26.9757

   Tenants whose pings appear under MORE THAN ONE strategy:  5 of 11 touched
     t01: {s1, s2}   t06: {s1, s2, s3}   t09: {s1, s2, s3}
     t11: {s1, s2}   t14: {s1, s2, s3}

   Unique tenants touched by any strategy:                   11
   Their DEDUPED combined saved_usd (each counted once):     $27.8477
   Naive sum of the 3 strategy ledgers' saved_usd:            $55.1040
   Inflation from double counting alone:                      $27.2563

5. Pings/ping_usd sanity check — these do NOT double count
   sum of pings across strategy ledgers: 143  == pings under any strategy_id: 143
   sum of ping_usd across strategy ledgers: $22.98 ≈ overall keepalive=1 ping_usd $23.23
   (the $0.25 gap is pings with no strategy_id at all — manual/legacy/session-override pings)
```

The deduped combined `saved_usd` for the 11 tenants any strategy has touched ($27.85) is
almost exactly the Overview all-time total ($28.13) — the $0.28 difference is one tenant
with keep-alive credit that isn't attributed to any configured strategy (a manual or
per-session-override ping). This confirms the underlying `keepalive_saved_usd` accounting
is consistent; the divergence is entirely in how the Strategies tab aggregates it.

## Root cause: two real, unlabeled gaps — not a computation bug

The SQL in `keepalivestrategy.go` computes exactly what its own comment says it computes:
"the tenant's WHOLE keep-alive credit, not only the share this strategy's own pings
produced." That is a **deliberate, documented ceiling** — the same pattern this codebase
already uses elsewhere (e.g. `total_usd` vs `cache_premium_usd` on the KV-cache page). The
per-strategy drawer already surfaces this in the UI (`app.js:7182`, tile labeled "Saved
(ceiling, whole account credit)", plus an explanatory note at `app.js:7185-7189`). Read in
isolation, one strategy's ledger is honestly labeled.

The manager's actual comparison — Strategies tab vs Overview — hits two things the
existing caveat does **not** cover:

1. **Cross-strategy double counting.** `Pings`/`PingUSD` are exact and additive across
   strategies (confirmed above: 143 == 143). `SavedUSD` is **not** additive: a tenant
   running more than one strategy (5 of 11 touched tenants here) has its whole credit
   counted once per strategy it appears under. Nothing in the UI says "do not add these
   numbers together across strategies" — and mentally summing the Strategies tab's
   numbers to compare against Overview's one total is exactly the comparison a manager
   would make. On this corpus that comparison is inflated by ~2x ($55.10 vs $28.13).
2. **Time-window mismatch.** `StrategyLedger` never calls `Filter.where()` — it has no
   `since`/`until` concept at all, so it is always all-time. Overview and the Keep-Alive
   tab both respect the dashboard's selected date range. A manager viewing Overview on
   "last 24h" ($25.23 saved) and then opening any strategy's ledger drawer (always
   all-time) is comparing two different questions with no visual signal that the ranges
   differ — the drawer shows no time-range chip or label at all.

Both are genuine UX defects, but neither is a bug in the underlying arithmetic:
`keepalive_saved_usd` itself reconciles cleanly (see the dedup check above). This is the
"two honestly-different, correctly-labeled numbers" case the KV-cache page precedent
suggested was plausible — except the *existing* label only covers one of the two ways a
manager can go wrong, not both.

## Proposed fix (not applied — for review)

Smallest change that prevents this manager (and the next one) from making either bad
comparison again:

1. **Strategies list page** (`renderStrategiesList`, `app.js`): add one line of prose above
   the table — "Ping counts and cost are exact and additive across strategies. Saved
   figures are a per-tenant ceiling and are NOT additive: a tenant running more than one
   strategy is counted under each one. Compare against the Overview/Keep-Alive tab's
   total, not against a sum of these rows." This is the one sentence that would have
   headed off the report.
2. **Strategy ledger drawer** (`openStrategyLedger`, `app.js`): show that the ledger is
   all-time, unconditionally — e.g. a small "(all time, ignores the date-range filter)"
   label next to the drawer title or beside the Saved tile — so it reads as a different
   question from whatever range chip Overview currently shows, rather than looking like a
   stale or wrong number.
3. Optional, larger, not proposed for now: make `SavedUSD` an exact per-strategy share by
   propagating the strategy id from the ping onto the real request it rescues (currently
   only the ping row carries `keepalive_strategy_id`; the rescued request does not). The
   design doc already treats the ceiling as an accepted simplification, and this would be
   a real schema/write-path change — bigger than what a labeling fix requires, and out of
   scope unless the smaller fix turns out not to be enough.
