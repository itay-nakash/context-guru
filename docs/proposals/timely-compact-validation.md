# Validating the cache-aware trigger end to end

One live run that shows the whole chain works: a session reaches the fill threshold, its cache goes
near-expiry, `summarize` fires, the chat continues, and the Components tab's episode figures agree
with what the database says happened.

This is a **functional validation of the proxy**, so the traffic must go **through** Context Guru —
it is not a benchmark measurement of an agent, and the "benchmarks bypass Guru" rule does not apply.
Nothing here is compared against a non-Guru baseline.

## Why this needs a plan at all

Three conditions have to be true on the same turn, and two of them are timing:

| Condition | Controlled by |
|---|---|
| billed input ≥ `0.9 × window` | how much transcript has accumulated |
| the window is exactly known | the model id resolving in a price list or LiteLLM |
| cache phase is `pre_expiry` — `0 < TTL − idle ≤ 60s` | **the gap between two turns** |

The third is the whole reason this is hard to catch by accident and easy to force on purpose: with a
5-minute TTL, a turn arriving **240–300s** after the previous one lands in the window. So the test
is a sequence of `sleep`s.

## Making the fill threshold cheap to reach

Reaching 0.9 of a real 1M window costs ~900k input tokens per turn. Instead, **pin a small window
in an operator price list**, which is the first link of the resolver chain and reports its answers
as exact (`modelinfo.Table.WindowExact`), so `CtxWindowExact` is true and the fill gate engages:

```yaml
# /tmp/cg-validate/prices.yaml
cache_read_frac: 0.1
cache_write_frac: 1.25
models:
  - match: "aws/claude-sonnet-5"
    in: 3.0
    out: 15.0
    window: 30000        # THE TEST LEVER: 0.9 of this is 27,000, not 900,000
    note: "validation run only — the real window is 1,000,000"
```

`0.9 × 30,000 = 27,000` billed tokens to fire, and the episode span is `0.10 × 30,000 = 3,000`
tokens of transcript growth. That turns a $25 run into well under $1 and a multi-hour run into
about fifteen minutes.

This tests the trigger's **arithmetic and plumbing**, which is what has been wrong twice. It does
not test behaviour at a genuine 1M fill; see "What this run does not prove".

## Setup

An isolated proxy — its own port and its own database, so nothing lands in the shared dashboard and
no other session's traffic lands in ours:

```bash
mkdir -p /tmp/cg-validate
cat > /tmp/cg-validate/config.yaml <<'YAML'
components:
  summarize:
    keep_last: 3
    min_tokens: 500
    resummarize_tokens: 2000
    # The shipped defaults, written out so the run is self-describing:
    trigger:
      min_request_frac: 0.9
      cache_state: pre_expiry
      pre_expiry_seconds: 60
YAML

MODEL_PRICES=/tmp/cg-validate/prices.yaml \
CHEAP_MODEL=aws/claude-haiku-4-5 \
/tmp/cg-purego \
  --listen 127.0.0.1:4111 \
  --config /tmp/cg-validate/config.yaml \
  --dashboard --dashboard-db /tmp/cg-validate/dash.db \
  --anthropic-upstream "$ANTHROPIC_BENCHMARK_BASE_URL" &
```

Confirm the window resolved exactly before spending anything — if this line is absent the run
cannot fire and there is no point continuing:

```bash
grep "model price list loaded" <proxy log>   # expect entries=1
```

Client turns go to `127.0.0.1:4111` with a stable session header, which is what keys the
checkpoint, the TTL record and the billed-input figure:

```bash
curl -sS http://127.0.0.1:4111/anthropic/v1/messages \
  -H 'content-type: application/json' \
  -H "x-api-key: $KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'x-context-guru-session: validate-1' \
  -d @turn.json > /dev/null
```

### Seeding from a real session instead

If you would rather start from traffic that genuinely reached 0.9 rather than a synthetic
build-up, pull a recorded transcript and replay it as the first turn:

```bash
curl -sS "$GURU/api/sessions/<session-id>/transcript" > /tmp/cg-validate/seed.json
```

That reproduces a real near-full transcript in one request instead of paying to generate one, and
it makes the fixture representative rather than invented. It replaces phase A only; phases B-D are
unchanged.

## The run

Each turn re-sends the whole transcript, so `Billed` per request is the prompt size and the episode
span is transcript **growth** from t0.

| Phase | Action | Gap before it | Expected |
|---|---|---|---|
| **A** build-up | 4-6 turns, each pasting ~4-6k tokens of filler so the transcript grows fast | ~5s (stay warm) | no summary. Gate `cache_state_declined_warm` on every turn once past 27k, which is itself a result: it proves the size gate opened and the cache gate held |
| **B** fire | one turn | **250s** | `summarize` acts. `fresh_summary` in its events, and the request body upstream is `[msg0, summary, last-3]` |
| **C** cold turn | one turn | **310s** (past the TTL) | `cache_miss_reason = ttl_expiry`, `cache_read = 0`, `cache_write > 0`. This is the turn that earns **cold credit** |
| **D** close | 3-5 turns, small | ~5s | each replays the checkpoint (`reused_checkpoint`, zero model calls) and earns **read credit**; the span closes when growth reaches 3,000 |

Phase A's gate is worth reading rather than skipping past: `cache_state_declined_warm` while the
transcript keeps growing is the design working, and it is also the thing that makes the coverage
number on the panel non-zero.

## Verification — hand-compute, then compare

The panel agreeing with itself proves nothing. Recompute each figure from the raw rows and require
exact agreement.

```bash
SPAN=/tmp/cg-validate/dash.db
curl -sS 'http://127.0.0.1:4111/api/components/compaction-episodes' > /tmp/cg-validate/api.json
```

**1. The trigger fired for the stated reason, once.**

```sql
SELECT r.id, r.ts, r.cache_miss_reason, r.cache_read, r.cache_write,
       c.events, c.gates, c.saved_usd
FROM requests r JOIN request_components c ON c.request_id = r.id
WHERE c.component = 'summarize' ORDER BY r.ts;
```

Expect exactly one row whose `events` contains `fresh_summary`; every later row carries
`reused_checkpoint` and `saved_usd > 0`. If a later row also carries `fresh_summary`, the
checkpoint is rolling forward more often than intended — record it, it is a real finding about
`resummarize_tokens`.

**2. The cache really went cold.** The phase-C row must show `ttl_expiry` with `cache_read = 0`.
If it shows `hit`, the sleep was too short or a keep-alive ping refreshed the entry — check for
`keepalive = 1` rows and disable the keeper for the run if so. Without a genuine cold turn there is
no cold credit to check.

**3. The span closed where it should.** From the API, `start_billed` and `end_billed`; require
`end_billed − start_billed ≥ 3000` and that the immediately preceding turn was below it.

**4. Cold credit is the sum it claims to be.**

```sql
SELECT ROUND(SUM(c.saved_usd), 8) FROM requests r
JOIN request_components c ON c.request_id = r.id
WHERE c.component = 'summarize' AND r.cache_miss_reason = 'ttl_expiry'
  AND r.ts BETWEEN <start_ts> AND <end_ts>;
```

Must equal `cold_credit_usd`. Repeat with `= 'hit'` for `read_credit_usd`, and with
`= 'prefix_change'` — that one must appear in **no** column of the API response.

**5. The invalidation debit is t0's own write.** `cache_write` on the `fresh_summary` row × the
5-minute write rate (`in × 1.25` from the price list) must equal `invalidation_debit_usd`.

**6. Net is the arithmetic.** `cold + read + other − invalidation − summarizer = net_usd`.

**7. Coverage counted the conversation.** `coverage.conversations = 1` and `with_episode = 1`. Then
run a **second** session that reaches 27k with no long gap at all: it must land in `no_episode` with
a non-zero `no_episode_cold_usd`, and produce no episode.

## Negative controls — the part that makes the run mean something

A trigger that fires on everything would pass every check above. Each of these must **not** fire,
with the named gate present:

| Control | Expected gate |
|---|---|
| a warm turn (5s gap) above the fill | `cache_state_declined_warm` |
| a pre-expiry turn on a small transcript | `below_request_trigger` |
| a pre-expiry turn above the fill on a model **absent** from the price list, with `MODEL_INFO=off` so only the substring table answers | `window_not_exact` |
| a session's **first** turn (no previous billed figure) | `window_not_exact` |

The third is the specific defect this branch fixed on the window side, and the fourth is the one it
fixed on the units side. Both are cheap to run and both would have caught a regression that
arithmetic review did not.

## Pass criteria

1. Exactly one `fresh_summary`, in the pre-expiry window, above the fill.
2. At least one genuine `ttl_expiry` turn inside the span.
3. All six recomputations in the Verification section agree to 8 decimal places.
4. All four negative controls decline, each with its named gate.
5. The second session appears in `no_episode` with a non-zero opportunity figure.
6. `mkdocs`-documented behaviour matches what the panel shows — in particular that `recorded` and
   `inferred` are separate rows.

## Cost

About 250-350k input tokens across both sessions on a sonnet-class model, most of it cache reads:
well under $1. Wall time ~15 minutes, dominated by two sleeps of 250s and 310s. The summarizer's
own calls are on `CHEAP_MODEL` and are cents.

## The scenario suite — `scripts/scenarios/`

The single forced run above validates the mechanism against its specification. It cannot validate
three things that turned out to matter, and each got its own arm. They are checked in, because a
measurement nobody else can re-run is an assertion:

| Arm | Config | What it answers |
|---|---|---|
| **A** `a-firing-rate.sh` | **shipped** defaults, no injected idle | how often do both gates open on traffic nobody arranged? |
| **B** `b-cold-events.sh` | forced, then **three** cold events in one span | does the cold credit *accumulate* per prevented rewrite? |
| **C** `c-warm-only.sh` | forced, then **warm turns only** | is a warm turn's credit priced at the cache-**read** rate? |

```bash
export CG_SCEN_UPSTREAM="https://your-gateway.example.com"   # the plain provider gateway
export CG_SCEN_SRC=/path/to/a/frozen/checkout                # not a tree you are still editing
tmux new -d -s scen 'scripts/scenarios/run-all.sh'           # ~1 hour, dominated by idle
```

Every arm builds its own proxy binary and runs it on its own port with its own dashboard database,
and points a private `CLAUDE_CONFIG_DIR` at it. **The production Context Guru is never in the request
path.** That matters more than it sounds: Claude Code's `settings.json` env *overrides* the process
env, so `ANTHROPIC_BASE_URL=... claude` does nothing at all — the first attempt at this sent every
request to the production Guru without a word. The endpoint has to be written into a copied
`settings.json`, which `scen_home` does.

### Arm A — the firing rate, and why 0.9 may be unreachable

This is the arm with no forcing in it, and therefore the only one whose result does not depend on any
of this feature's code being correct. It runs a real `claude -p` session on the **shipped**
`min_request_frac: 0.9` and `cache_state: pre_expiry_or_cold`, with no injected gaps, and reports how
many turns fired.

It also records the number that decides whether 0.9 is reachable *at all* on this client: **where
Claude Code runs its own compaction.** If the client caps its transcript below 0.9 of the model
window, the shipped gate cannot fire on that model however long the session runs — the firing rate is
zero for a structural reason rather than a statistical one, and no amount of additional traffic
changes it. The arm prints the client's own ceiling as a fraction of the window beside the 0.900 the
gate needs, so the two are compared rather than assumed.

That is also why the forced arms use `min_request_frac: 0.5`. It is a test lever, not a
recommendation, and arm A is the arm that measures why the lever is needed.

### Arm B — the cold credit has to accumulate

`ColdCreditUSD` exists to measure one thing: an expiry that happens *after* a summary re-creates the
**compacted** prefix instead of the full one, so the difference is a rewrite that did not happen. A
run with one such event cannot distinguish "the credit is computed" from "the credit accumulates per
event", and it is the accumulation that makes the amortisation model true rather than anecdotal.

So arm B fires once and then forces the cache cold **three separate times inside the same span**,
and checks the bucket holds three prevented rewrites. It prints the counterfactual explicitly: the
full prefix (which t0 itself re-created, so it is measured rather than modelled), the compacted
prefix each cold turn actually wrote, and the difference per event and over all three.

**Three colds fit inside one span, and that is itself worth demonstrating.** The span axis is
cumulative *new* content, and a cache write on a MISS is re-creation rather than new content, so a
cold turn advances the span by only its few tokens of fresh input. Under the axis this PR replaced —
cumulative spend — the first cold turn would have closed the span on its own, which is exactly how
the headline bucket came to be structurally empty.

### Arm C — the rate on a warm turn

The defect that inverted the panel's sign: a credit on a turn whose cache **hit** was priced at the
cache-**write** rate, 12.5x too high. The mechanism is worth stating because it is not obvious and it
was introduced by going async — the stash is written by the detached goroutine, which has no
`Report`, so the checkpoint key first reaches `rep.CacheKeys` on the turn that *replays* it.
`Recorder.MarkUnique` sees a key it has never seen, calls the whole removal new content, and the row
lands priced at the creation rate. That turn is a cache hit, and unlike t0 it **is** credited.

Arm C fires once and then takes only warm turns, so:

- every credit must land in `read_credit_usd` at the cache-read rate,
- `cold_credit_usd` must be exactly zero,
- the figure must equal `sum(saved_gross) x read rate` and must **not** equal the sum of the stored
  `saved_usd`. The arm prints both, so a regression reads as the panel agreeing with the wrong one.

It also queries the panel a second time at `?span=0.002`, so the same rows produce a **closed**
episode. An arm that only ever reports an open one cannot check the settled total, which is the
figure a reader actually trusts.

### Reading a run

Each arm prints every request row as the provider billed it — `billed`, `read`, `write`, the cache
verdict, our own removed-token count, the summarizer's cost and `cg_ms` — then the panel's JSON, then
a hand-derivation from the raw rows to compare against it. The panel is never the source of its own
check.

## What this run does not prove

**That the firing rate is acceptable.** Arm A measures it; nothing here decides whether the answer is
good enough. If both gates are shown to be practically unreachable under the shipped defaults, that
is an argument about the defaults — lower the fraction, widen `pre_expiry_seconds`, or accept the
feature as a narrow safety net for the walk-away-and-return case and document it as one. It is not an
argument that the mechanism is broken.

**A net over a settled span on natural traffic.** Arm B and arm C both force their conditions. A
forced span that is cut short shows a loss almost by construction, because t0 pays the model call and
the cache write up front while the credit accrues turn by turn afterwards — so a negative net on a
truncated span is a statement about the span's length, not about the mechanism. Break-even is on the
order of six warm turns after the summary.

**Behaviour at a real 1M fill.** The pinned 30,000 window exercises the arithmetic, not the
provider's behaviour near its actual limit. If you want that too, drop the `window:` override and
repeat phases B-D on a session that genuinely reached 900k — same script, ~$25, several hours.

**That 0.9 and 60s are the right numbers.** Neither is measured. This validates the mechanism
against its specification, not the specification against reality.
