# Manager-controlled keep-alive strategies

## Problem

The keep-alive ping mechanism (`proxy/keepalive.go`) is real and measured (net
+$125.08 / 3.98% of spend at shipped defaults), but it is only reachable two
ways: a manager flips one account-wide boolean in that account's own config,
or a manager arms one session for up to 12h. There is no way to run a
schedule ("only during Israel business hours"), no way to apply one policy
across every account at once, and no way to see cost vs. savings broken out
per policy per account. This adds that layer without touching the existing
per-account config path or the per-session override path, both of which stay
exactly as they are today.

## Model

A **strategy** is a durable, manager-authored rule:

```
Strategy {
  ID          string
  Name        string
  Idle        seconds   // default 280
  MaxPings    int        // default 1 ("one ping")
  MinPrefixTokens int
  MaxUSDPerPing   float64  // manager-set; strategies MAY set this, unlike
                            // per-session overrides, because this is an
                            // audited manager action, not an ephemeral grant
  Windows     []Window
  Target      Target
  Active      bool
  CreatedBy, CreatedAt, UpdatedBy, UpdatedAt
}

Window { Days []time.Weekday /* empty = every day */; Start, End string /* "HH:MM" */; TZ string /* default "Asia/Jerusalem" */ }

Target { Mode string /* "all" | "list" */; TenantIDs []string }
```

Two windows on one strategy cover "9-12 and 14-18": `[{Start:"09:00",End:"12:00"}, {Start:"14:00",End:"18:00"}]`, `TZ:"Asia/Jerusalem"`.

## Why this can't be "patch the tenant's config"

The decided requirement is that a strategy overrides a tenant's own
account-wide off switch. That rules out implementing "apply" as writing each
target tenant's `CacheConfig` document, because the tenant's own document is
exactly what `DELETE /api/me/keepalive` (the existing self-serve off switch)
edits — patching it would just get overwritten the next time that tenant
touches their own switch, and the two writers would fight silently.

So a strategy is not config. It is evaluated **live**, the same way a
per-session override already sits above account config today
(`keeper.overrideFor`). This generalizes that one hook into a resolution
chain:

```
account CachePolicy (config, as today)
  -> highest-priority matching ACTIVE strategy (forces KeepAlive=true,
     replaces Idle/MaxPings/MinPrefixTokens/MaxUSDPerPing)
  -> session override (unchanged: still wins on top, still cannot widen
     MaxUSDPerPing)
```

A strategy "matches" a request when: `Active`, the request's tenant is
covered by `Target` (`all`, or in `TenantIDs`), and `now` (converted to the
strategy's `TZ`) falls inside one of `Windows`. Because this is evaluated per
request rather than snapshotted at apply time, an `all`-target strategy
automatically covers tenants created after it was set up — no separate
"reapply" step.

**Precedence among strategies**, when more than one matches the same tenant
at the same instant: a `list`-target strategy beats an `all`-target one
(more specific wins); among equally-specific matches, the most recently
updated wins. This is resolved once per request, not configured — no
priority field to expose in the UI, one less knob a manager can get wrong.

## Persistence

New table `keepalive_strategies` in the tenant registry DB (same DB file as
`tenants` / `tenant_config_audit` in `tenant/tenant.go`'s schema — this is
account/control-plane data, not request-metrics data, so it does not belong
in `dash/schema.go`'s DB). Columns: `id, name, idle_seconds, max_pings,
min_prefix_tokens, max_usd_per_ping, windows_json, target_json, active,
created_by, created_at, updated_by, updated_at`.

Loaded into the keeper's memory (`k.strategies []strategy`, mutex-protected,
alongside the existing `k.overrides`) at process start and refreshed on every
write through the control routes below — the same durability trade-off the
rest of `keepalive.go` documents at length does NOT apply here: unlike a
per-session override (deliberately memory-only, an authorization to spend
that should not survive a restart), a strategy is a standing rule a manager
expects to still be there after a deploy, so it is written through to SQLite
on every create/update/delete and reloaded at boot.

## Attribution for stats

`kaEntry` gains an `appliedStrategy string` (empty when none matched).
`record1` (which already turns a fired ping into a `dash.Event{KeepAlive:
true, ...}`) tags that event with `KeepAliveStrategyID`. This needs one new
nullable column, `keepalive_strategy_id TEXT`, on `dash/schema.go`'s
`requests` table (migration, additive, defaults NULL for every existing row
— never backfilled, since there is no way to know which pre-feature pings
would have matched a strategy that didn't exist yet).

## Control plane

New file `proxy/keepalivestrategy.go`, table-driven exactly like
`keepalivectl.go`, every route `ctlManager`:

- `GET /api/keepalive/strategies` — list, with each strategy's live-resolved
  "currently in a matching window: yes/no" for the UI's at-a-glance state.
- `POST /api/keepalive/strategies` — create. Validates windows (valid
  `HH:MM`, `End` after `Start` within the same day — an overnight window is
  out of scope, since "9-12 and 14-18" never crosses midnight and the extra
  case is not worth the parsing complexity), valid IANA `TZ`
  (`time.LoadLocation`), `Target.Mode`, and the same numeric bounds
  `validOverride` already enforces for Idle/MaxPings (a strategy is a
  broader-blast-radius version of the same spend authorization, so it gets
  at least as tight a check, not a looser one).
- `PATCH /api/keepalive/strategies/{id}` — update any field; takes effect on
  the next request that would match (no re-push needed, since matching is
  live).
- `DELETE /api/keepalive/strategies/{id}` — delete. Anything currently held
  under that strategy is not retroactively un-pinged (nothing to undo — a
  ping already sent already happened), but it stops matching new requests
  immediately.

Every write gets an audit row via the existing `tenant.AuditWrite`, actor and
target both the acting manager's own tenant ID (there is no single target
tenant for an `all`-target strategy, so — unlike every other audited action
in this codebase, which names a specific tenant — the audited object here is
the strategy itself, named by its ID in the `field` column).

## Dashboard read side

New file `dash/keepalivestrategy.go`:

- `StrategyLedger(strategyID string) (StrategyLedgerView, error)` — pings,
  ping cost, saved (ceiling, same caveat as the existing account ledger),
  net, grouped by tenant, costliest first. Built the same way
  `KeepAliveSessions` already is, filtered by `keepalive_strategy_id`
  instead of by `Filter.Tenant`.
- Exposed at `GET /api/keepalive/strategies/{id}/ledger`, `scopeManager`.

## UI

**New manager-only "Strategies" tab**, following the exact pattern the
existing "Tenants" tab uses (`data-manager` attribute on the tab button,
`hidden` by default, gated by the existing `isManager()` check in
`dash/ui/app.js`): a create/edit form (name, idle, max pings, min prefix,
max $/ping, one or more windows with a day picker, target: all users or pick
from the tenant list already available to managers on the Tenants tab), and
a table of strategies with an active/paused toggle, edit, delete, and an
expandable per-tenant stats breakdown sourced from `StrategyLedger`.

**Overview tab addition** (every user, not just managers): a small "keep-alive
savings" callout reusing the account's own already-computed
`GET /api/keepalive` ledger (`SavedUSD`, `NetUSD`, `Pings`) — pure UI, no new
backend call beyond the one that already exists.

## Defaults

New strategies default to `MaxPings=1` ("one ping"), `Idle=280s`, matching
the literal ask; a manager can widen either per strategy. `MaxUSDPerPing`
defaults to the existing package sentinel (`DefaultMaxUSDPerPing = 0.25`)
when left at 0, same as account config does today.

## Testing

- Unit: window/timezone matching (including a case straddling a DST
  transition in `Asia/Jerusalem`), precedence between an `all` and a `list`
  strategy, precedence between two `list` strategies, a strategy correctly
  losing to a live session override, validation bounds on create/update,
  persistence across a simulated restart (reload from DB).
- Integration: a strategy created via the control route is visible in
  `keeper.resolvePolicy` on the very next request with no restart.
- Live: drive a real Claude Code session through a local build of the
  proxy with a strategy active for the current wall-clock minute, and
  confirm a ping actually fires and lands in `/api/keepalive/strategies/{id}/ledger`.

## Out of scope

- Overnight windows (`End` before `Start`).
- A priority field — precedence is fixed by specificity + recency, not
  manager-configurable.
- Retroactively crediting/attributing pings sent before this shipped.
- Changing anything about the existing per-account config switch or the
  per-session override mechanism; both are untouched.
