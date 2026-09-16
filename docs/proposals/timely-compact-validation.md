# Validating the cache-aware trigger end to end

One live run that shows the whole chain works: a session reaches the fill threshold, `summarize`
fires, the chat continues, and the Components tab's episode figures agree with what the database says
happened.

> **The cache-state link in that chain is gone.** This document was written when firing also required
> the cache to be near expiry or already cold. Those states were withdrawn and the default is now
> `any`, so the chain is fill → fire. The cache phase still matters to `cache_state: pre_expiry`, whose
> caller needs a live prefix to reuse, and the arms below say which of them still assume the old chain.

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
| **A** build-up | 4-6 turns, each pasting ~4-6k tokens of filler so the transcript grows fast | ~5s (stay warm) | under `cache_state: pre_expiry`, no summary and `cache_state_declined_warm` on every turn once past 27k — which proves the size gate opened and the cache gate held. **Under the shipped default (`any`) this phase now SUMMARIZES**, because nothing waits for the cache; the gate names to expect are fill-only |
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
| a warm turn (5s gap) above the fill | fires under the shipped default; `cache_state_declined_warm` only under `cache_state: pre_expiry` |
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
| **D** `d-maxtokens-rule.sh` | shipped trigger, a **second** model | does `W − max_tokens` predict where the client compacts, so `C` can be derived rather than tabulated? |

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
`min_request_frac: 0.9`, with no injected gaps, and reports how many turns fired.

> **This arm's result is what withdrew a default.** When it was written the shipped `cache_state` was
> `pre_expiry_or_cold`, and this arm measured **0 fires in 53 turns** with `cache_state_declined_warm`
> on 51 — the largest gap between any two values the key ever had. That, plus the −$0.84 corpus cost
> of firing warm and the two-turn shape that makes a cold gate pay the first rewrite anyway, retired
> the cold-gated states entirely. The default is now `any`, so **re-running this arm today should show
> the fill gate as the only gate**: fires once the session passes 0.9 of C, and a firing rate near
> zero now means the fill threshold was never reached, not that the cache stayed warm. To reproduce
> the original measurement, set `cache_state: pre_expiry` — which is not the same configuration, and
> should fire even less.

It also records the number that decides whether 0.9 is reachable *at all* on this client: **where
Claude Code runs its own compaction.** If the client caps its transcript below 0.9 of the model
window, the shipped gate cannot fire on that model however long the session runs — the firing rate is
zero for a structural reason rather than a statistical one, and no amount of additional traffic
changes it. The arm prints the client's own ceiling as a fraction of the window beside the 0.900 the
gate needs, so the two are compared rather than assumed.

That is also why the forced arms use `min_request_frac: 0.5`. It is a test lever, not a
recommendation, and arm A is the arm that measures why the lever is needed.

### The compaction point `C` — the definition everything else rests on

> `C(deployment, client, model)` is the **provider-billed input** at which the conversation's own
> compaction mechanism acts — the largest prompt that mechanism allows to be sent before it rewrites
> the transcript.

Two figures derive from it and nothing else should:

| | |
|---|---|
| the **trigger** | `summarize` fires at `frac × C` (shipped `frac` = 0.9) |
| the **span** | an episode covers `C − frac × C` = `(1 − frac) × C` |

Both come from `C` rather than from the model window `W` because that *is* the argument for the
feature: compacting just before the conversation's own mechanism would have acted captures the saving
of a large prefix going cold, and **costs no accuracy that was not already going to be lost** — a
compaction was going to happen there anyway. Firing against `W` is only correct when `C = W`.

#### Which ruler, and why this definition never needs the client's own number

`C` is in provider-billed input (`fresh_input + cache_read + cache_write`) — the ruler the gate
compares, the ruler a context window is stated in, and the only one every party shares. The same
request is counted three ways:

| ruler | value | fraction |
|---|---|---|
| **provider-billed input** | 199,184 | **0.996** ← `C` is in this |
| Claude Code's own count | ~167,000 | 0.835 ← never used here |
| our message-text count (`tokens_before`) | 147,493 | 0.737 |

A client thresholding at ~167,000 of *its* count and the provider billing 199,184 on that same
request are **the same event**. So `C` is *defined* as an observation in billed tokens rather than as
a translation of the client's threshold — which means the conversion factor between rulers never has
to be known, and cannot be got wrong. Putting 0.835 in would be the units error `Trigger.Fires`
shipped with, one level up.

#### "Whenever the native compaction works" — four cases, because it genuinely differs

| case | what compacts | how `C` is known |
|---|---|---|
| **1** | **the client** (Claude Code and similar) | **observed**: the billed input of a request the agent-compaction detector flagged |
| **2** | **the provider, at our request** ([#241](https://github.com/rossoctl/context-guru/issues/241)) | **chosen** — the `trigger.input_tokens` we set. Exactly known |
| **3** | **nothing** (raw API, `llm-d`, self-hosted) | `C = W`, the hard limit, and this is a **fact**. Also the deployment where this component matters most, since nothing else stands between the session and a 400 |
| **4** | **unknown** — never observed, and case 1 vs case 3 indistinguishable | `C = W` as a **guess**. Must not report alike with case 3 |

**Case 1 is measurable from columns already stored.** `proxy/agentcompaction.go` writes its verdict to
`requests.bypassed`, so `C` is one column away. Verified on a real session: exactly **one of 53 rows**
carried `bypassed=1`, and it billed **199,184** — the transcript at its largest. A `tokens_before` drop
on the following turn is an independent second marker and agreed exactly.

**Require both markers.** The phrase detector has a reachable false positive — the phrase is quoted
verbatim in this repo's own `docs/how-to/agent-compaction.md`, so an agent that reads that page gets it
into a `tool_result` — and a false `C` poisons the trigger for the whole model.

#### What varies, which is why `C` cannot be a constant

- **By model.** A client thresholding on a fraction of the window scales `C` with `W`: haiku 200K
  against sonnet/opus 1M.
- **By client, and by that client's configuration.** Claude Code's auto-compact threshold is
  settable, so two tenants on the same model legitimately have different `C`. **This is the largest
  source of variation and the one a shipped table cannot capture.**
- **By client version.** The 0.996 measured here and a ~0.835 reported elsewhere may be exactly this.
- **By provider, twice over:** it sets the hard limit for case 3, and whether case 2 exists at all.

So `C` is properly per `(tenant, client, model)` and **learned**. The table in
`dash/compactionpoint.go` is a fallback for a deployment with no observations yet, and
[#239](https://github.com/rossoctl/context-guru/issues/239) is the issue for learning it.

#### The statistic, once there are several observations

A **low percentile** is conservative for *both* uses, which is what makes it principled rather than a
taste:

- the **trigger**: a lower `C` fires earlier, which still beats the client's mechanism. A `C` that is
  too **high** is the dangerous one — the client resets before billed input ever reaches `frac × C`,
  so the component **never fires at all** and looks broken.
- the **span**: a lower `C` narrows the span, which under-reports.

And note what an observation bounds: the client acts when it *exceeds* its threshold, so the flagged
request is at or above it. An observed `C` is an **upper** bound on the threshold in billed terms, so
taking a low percentile and then `frac × C` pulls safely under it twice.

#### Where the code stands against this definition

`spanFor` uses `C` (from the fallback table, per model, with provenance). **`Trigger.Fires` does
not** — it still compares against `frac × W`. On haiku behind this Claude Code version that is nearly
the same number, because `C = 0.996 W`; it is wrong on any deployment whose client compacts
meaningfully earlier, and wrong in the direction where the component never fires. Retargeting the
trigger is a behaviour change to the shipped gate and is called out as its own decision rather than
carried quietly.

### Why the span is what it is: an attribution boundary whose far end belongs to the CLIENT

The span is `client_ceiling - fill_threshold`, derived rather than configured beside them.

We fire at the threshold. In the counterfactual world where we did *not* compact, that conversation
keeps growing — and not forever, because the **client** compacts when it reaches its own ceiling. Past
that point both worlds are running on a summarized transcript, and nothing further is attributable to
us. So the span is exactly the distance between those two points.

**The ceiling is an assumption about the client, not a property of the model.** An earlier version of
this computed `1 - fill`, which silently asserted the client runs the transcript to the model's limit.
It is now a parameter (`?ceiling=`, default 1.00), and [#239](https://github.com/rossoctl/context-guru/issues/239)
is the issue for learning it instead.

### Which ruler the ceiling is measured in — the third instance of this problem in this feature

Arm A caught the client compacting, and the same moment reads three different ways:

| ruler | value at the client's ceiling | as a fraction |
|---|---|---|
| provider-billed input | 199,184 | **0.996** |
| Claude Code's own count | ~167,000 | 0.835 |
| our message-text count (`tokens_before`) | 147,493 | 0.737 |

The fill gate compares against **billed** input, because a context window is stated in billed tokens.
So the ceiling has to be expressed in billed tokens too, and **0.996 is the figure that belongs here**
— which is why the default is 1.00 and not the 0.835 the client's own indicator would suggest. Quoting
the client's number here would be the same units error `Trigger.Fires` shipped with, one level up.

It is still one client, one model, one version. A deployment whose client compacts earlier — a
configured auto-compact threshold, a different agent, a wrapper of its own — needs its own value.

### The case the old formula could not express

If the ceiling sits at or below the fill threshold, **the client compacts before we would ever fire**,
so there is no window in which anything is attributable to us and the component cannot help on that
deployment at all. The panel now reports `coverage.no_attributable_span` and emits no episodes. The
previous version substituted the shipped 0.10 span, inventing a window the configuration says does not
exist and publishing credits for turns the client had already compacted away.

That is not hypothetical: 0.835 against a 0.9 trigger is exactly the pair the client's own indicator
implies, and whether it is the *right* pair depends on which ruler the client actually thresholds in —
which is the open question #239 exists for.

### Why the two axes line up

Worth stating because it is not obvious: the fill is measured in provider-billed input (prefix plus
new), the span in new content only. They agree because adding X tokens of new content raises billed
input by X — the prefix is re-sent either way.

### Arm B — the cold credit has to accumulate

`ColdCreditUSD` exists to measure one thing: an expiry that happens *after* a summary re-creates the
**compacted** prefix instead of the full one, so the difference is a rewrite that did not happen. A
run with one such event cannot distinguish "the credit is computed" from "the credit accumulates per
event", and it is the accumulation that makes the amortisation model true rather than anecdotal.

So arm B fires once and then forces the cache cold **three separate times inside the same span**,
and checks the bucket holds three prevented rewrites. It prints the counterfactual explicitly: the
full prefix (which t0 itself re-created, so it is measured rather than modelled), the compacted
prefix each cold turn actually wrote, and the difference per event and over all three.

**Three colds fit inside one span, and the reason is the point of the arm.** The span advances on
cumulative *new content*; a cold event happens because of *elapsed time*. Those are independent, and
they are anti-correlated in the helpful direction: **a session idle enough for its cache entry to
lapse is by definition not accruing new content, so the span cannot be closing while a cold event
becomes possible.** A session can go cold having added 2% of the window — and the next turn then pays
a write that, uncompacted, would have covered 92% of it.

Two things have to be right for that to hold, and one of them is the script's own prompts:

- A cache write on a MISS is re-creation rather than new content, so a cold turn advances the span by
  only its few tokens of fresh input. Under the axis this PR replaced — cumulative spend — the first
  cold turn would have closed the span on its own.
- The turns *between* t0 and the idle gaps must add almost nothing. The first version of this arm
  asked them to read files; they wrote 20,095 tokens of new tail against a 20,000 span and the span
  closed before any gap. That produced `cold_credit_usd = $0.00` and an incorrect conclusion — that a
  cold event "essentially cannot fall inside a 10% span". It can, easily. The arm was measuring its
  own prompts.

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

### Arm D — can `C` be derived instead of tabulated?

`C` currently comes from a per-model table with **one** measured entry. This arm tests the rule that
would remove the table entirely.

**The tempting shortcut, and why this arm exists to falsify it rather than adopt it.** On a real
Claude Code session on haiku, `max_tokens` was 32,000 on every request, and `W − max_tokens = 168,000`
— close to the ~167,000 threshold the client's own indicator reports.

That is **one data point and probably a coincidence.** `max_tokens` is the *output* budget; reading it
as a compaction threshold requires the client to have chosen a "reserve one full response" policy, and
nothing observed says it did. There is also **evidence against the mechanism that would make it
non-arbitrary**: if the provider enforced `input + max_tokens ≤ W` the client would be *forced* to
compact around `W − max_tokens`, and it does not — a captured request billed **199,184** input with
`max_tokens` 32,000 on a **200,000** window, 231,184 together, and succeeded.

**And `C` is not on the wire at all.** A real Claude Code request was captured and inspected in full:

```
max_tokens  messages  metadata  model  output_config  system  temperature  thinking  tools  stream
```

No `context_management` block, and nothing anywhere in the record naming context, compaction, a limit,
a window, a budget or a threshold. On reflection that is expected — where the client compacts is the
client's own policy, so the provider has no reason to know it, and there is nothing for it to return
as metadata.

So the only sound source for `C` is **direct observation in billed tokens** (case 1), which already
works. This arm is cheap insurance against someone building on the coincidence.

**An earlier version of this arm was worthless and is worth recording as such.** It set a settings key
named `autoCompactThreshold` and looked for the compaction point to move. **That key was invented** —
it is not in the client's settings and nothing was shown to read it — so the arm tested nothing while
appearing to test something. If the client's threshold really is a reserve rule rather than a
configured fraction, there may be no threshold knob at all, and the settable quantity is the output
budget, which reaches us as `max_tokens`.

**The test.** Run the same growth on a second model with the same window, and see whether the observed
compaction point moves the way `W − max_tokens` predicts. One `(W, max_tokens)` pair cannot distinguish
a rule from a coincidence; two can.

The comparison is a **ratio**, not an equality, because the prediction is in the client's ruler and the
observation is in billed tokens — on haiku that ratio was `199,184 / 168,000 = 1.186`. A ratio near
1.186 on a second model means the rule holds and `C` can be derived from `max_tokens` plus one
calibration. A ratio far from it means the haiku match was a coincidence, which is the **more** useful
outcome: someone would otherwise build on it.

Three ways this can come out, and the arm prints which:

| | |
|---|---|
| `max_tokens` differs and the point moves as predicted | the rule holds on two points |
| `max_tokens` differs and it does not | the rule is refuted |
| `max_tokens` is the same on both models | no variation was available; **the arm proves nothing** and says so |

**The third is what happened, and it is structural for this model pair.** Claude Code sent
`max_tokens = 32,000` on `claude-sonnet-4-5` — identical to haiku. So **no pair of 200,000-window
models can test the rule here**, because the predictor does not vary. Varying it needs a model where
the client picks a different output cap, or control of `max_tokens` itself, and neither is available
cheaply.

That is acceptable rather than a gap, because the rule is a speculation that has already been
retracted and the primary source for `C` — direct observation in billed tokens — does not depend on it.
Keep the arm for the day a model with a different output cap is on hand; do not spend money on it
before then.

**And the run failed for a second, unrelated reason worth recording:** the unprefixed
`claude-sonnet-4-5` returns `403 team not allowed to access model` on the gateway this was written
against, while the same model under `aws/` is routable. Every request 403s, the session never grows,
and the arm reports *"the client did not compact"* — **which looks like a result and is a routing
error.** The default is now the prefixed form. A live arm that can report a plausible-looking finding
from a total failure is the trap this whole suite keeps running into.

### An incidental finding from arm B worth keeping

On its last run a turn arriving **383 s** after the previous one — past the nominal 300 s TTL and past
the 60 s clock-skew allowance — came back as a **partial hit** (`read=22,441 write=10,829`) rather than
a full miss. Two later turns at the same 383 s gap did miss completely.

So the provider's entry can outlive its nominal lifetime, and by more than the margin. That was direct
support for the strict cold test requiring a clock-skew margin past expiry before claiming an entry was
gone: a gate trusting `remaining <= 0` would have called that turn cold and rewritten a prefix that was
still partly live.

That gate is gone — the cold-gated cache states were withdrawn — so this observation no longer defends
a threshold. **It still constrains the rig**, which is the reason it stays: a scenario arm cannot
*guarantee* a cold turn by waiting, only make one likely, which is why arm B checks the verdict it
actually got rather than assuming one. It also remains the evidence for `apply.coldMargin`, which
computes `Ctx.ColdCache` and is now the only margin-bearing cold verdict in the codebase.

### Reading a run

Each arm prints every request row as the provider billed it — `billed`, `read`, `write`, the cache
verdict, our own removed-token count, the summarizer's cost and `cg_ms` — then the panel's JSON, then
a hand-derivation from the raw rows to compare against it. The panel is never the source of its own
check.

## What this run does not prove

> ## ⛔ THE INSURANCE FRAMING BELOW IS RETRACTED
>
> This section argued that the cache gate was cheap insurance whose base rate had not been measured,
> and that the open question was how many production conversations go idle past the TTL. **The gate is
> withdrawn**, so the question no longer decides anything. Arm A's zero was not an unmeasured base
> rate — it was the gate declining 51 warm turns it should have fired on. The three reasons are in
> `summarizeDefaultCacheState`; the shortest is that the corpus cost of firing warm and being wrong
> was **−$0.84 in total**, which is not a risk worth insuring.
>
> Kept, not deleted, because the reasoning error is the useful part: "declining costs nothing" was
> true per turn and false per session, and that is what made a rare trigger look free instead of
> expensive.

**~~The base rate of the event this insures against.~~** Arm A measures the firing rate on a
*continuously active* session and got zero under the withdrawn default — the one population where a
cold event cannot occur, because an active agent keeps touching its own cache. That was read as "the
feature rarely fires, which is fine"; it should have been read as "the gate is declining the turns the
payback argument says to fire on".

~~The structure is cheap insurance with a rare trigger and a large payout.~~ Declining costs nothing
*on that turn* — the component splices nothing and spends nothing — and that is exactly the sentence
that hid the cost. Over a session, declining means every later turn re-reads a prefix that would have
been three quarters smaller. Firing costs one summarizer call plus a cache write, and pays that back
in 2-3 turns. Arm B's **$0.15 per prevented rewrite** of ~115,000 tokens still stands as the payout
figure; what changed is that it is no longer the only thing on the credit side.

**What genuinely remains unmeasured** is narrower: how much a *compacted* session saves over its
remaining turns on natural traffic, which is the 14k-per-turn side of the payback arithmetic rather
than the rare-event side. `dash/kvcache.go` records the per-request idle gap and
`coverage.no_episode_cold_usd` still sizes what conversations we did not fire on paid to re-create
expired prefixes. It needs production data, not new code.

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

---

# The complete test matrix for this feature

**Read this before reviewing or changing the pre-expiry summary gate.** Every scenario below found at
least one real defect, and several found one that a careful reading of the code had already missed.
They are listed with what each one proves, so a maintainer can tell which are load-bearing for a
change they are making rather than re-running everything.

## Level 1 — Go tests, the gate itself (`components/`)

Run: `go test ./components/... ./apply/...`

| Scenario | File | What it protects |
|---|---|---|
| Every cache phase from a `(TTL, idle)` pair | `cachephase_test.go` | the classification, including **zero idle is Warm, not Unknown** — reading it as Unknown let the gate open over a live prefix on 13 of 26 turns of a real run |
| A backwards clock stays Unknown | `cachephase_test.go` | never invent warmth from a clock that went backwards |
| Unknown **with a live prefix** is refused | `cachephase_test.go` | the B1 blocker. Every pre-existing guard set `MaxCachedIdx: -1`, the one value that makes Unknown safe, which is why none caught it |
| Unknown with **no** prefix is still permitted | `cachephase_test.go` | the guard must not disable the component on non-cache-aware deployments — the failure the guard could easily have traded for |
| `pre_expiry` declines once the entry reaches nominal expiry | `cachephase_test.go` | replaces the old **dead zone** row. There used to be two cold thresholds — the sweep needing a LIVE entry, the compactor a DEAD one — and a one-minute window between them where compaction declined. The compactor's threshold went away with the cold-gated cache states, so what is asserted now is that `pre_expiry`, whose caller needs a prefix that is still live, does not fire on an entry that may already be gone |
| `pre_expiry_seconds` validated against the TTL | `trigger_test.go` | a 600s window on a 300s TTL makes every warm turn PreExpiry → compaction every turn. Asserted by first *demonstrating* the misclassification |
| `cache_state` typo refused | `trigger_test.go` | a typo must not read as `any` |
| a **withdrawn** `cache_state` refused, with the replacement named | `trigger_test.go`, `summarize_cachegate_test.go` | `cold` and `pre_expiry_or_cold` are refused rather than aliased — an alias would change when the component fires without saying so. The error has to name `any` / `pre_expiry`, because this operator's config worked yesterday and did not contain a typo |
| the shipped default fires on a **live** cache, and `pre_expiry` declines the same turn | `summarize_anygate_test.go` | the pair is what pins the difference to one key. Asserting only that the default fires would still pass with the cache gate deleted; asserting only that `pre_expiry` declines would still pass if the default had been broken into never firing |
| The two size thresholds are **ANDed**, not `max()`ed | `trigger_test.go` | a semantic change nothing asserted. No shipped config sets both, which is exactly when a test is worth writing |

## Level 2 — Go tests, the accounting (`dash/`)

Run: `go test ./dash/...` (~70s; it is the slowest package)

| Scenario | What it protects |
|---|---|
| The span closes at exactly the boundary and not before | the axis, including that **cumulative new content** is the axis and neither per-turn size nor cumulative spend works |
| A **guessed** window produces no episode, and is counted | `modelinfo.Exact`'s rule: a 5x-low window makes every figure 5x wrong, so exclude rather than estimate |
| A client compaction mid-span **voids** the episode | the client did the thing we were getting ahead of, so the remainder is not comparable |
| An open span is reported but **not totalled** | and its net is rendered — the false-green defect was that `open_net_usd` was computed and displayed nowhere |
| Credit split by cache verdict, **each bucket pinning its RATE** | the dominant defect: a credit on a turn whose cache HIT priced at the cache-WRITE rate, 12.5x too high. Two assertions exist only to *reject* the old arithmetic |
| The debit comes from the write, not the label | a partial hit reads as `hit`, so a label-derived debit is silently zero where we rewrote a live prefix |
| t0 is **debit-only** | crediting the compaction turn front-loads a saving that has not happened |
| A roll-forward inside a span charges that span | not a new episode |
| The turn that closes one span and opens another | charged **once**, to the span it OPENS. The old fixture used `miss(CacheTTLExpiry)` so the debit was 0 and hid the double charge — the fixture is part of the defect |
| An unpriced model publishes **no** dollars | including `summarizer_cost_usd`, the one field independent of the model's rates. The fixture needs a `cgCost` or the assertion is vacuous |
| Two models in one session are two conversations | a cache entry does not transfer between models |
| Recorded and inferred provenance kept apart | never summed |
| The span **derives** from the client ceiling | and the ceiling is per model, carrying its provenance |
| A trigger **above** the client's ceiling is reported | `no_attributable_span`, not an empty dataset. Establishes a precondition first, so the zero is about the ceiling and not the fixture |
| The production-scale case | real live-run shapes, where the first replay turn's stored `saved_usd` is $0.14 against a correct ~$0.0105 |

## Level 3 — Go tests, the async path (`components/offload/`, `proxy/`)

| Scenario | What it protects |
|---|---|
| A forged marker in the summary is stripped | both spellings `expand` accepts, including the JSON-escaped one, plus `</summary>`. The wrapper's OWN marker must survive |
| An ordinary summary is byte-identical | the sanitizer must not mangle prose containing `<` or `>` |
| The async counters reach `/stats` | they had **no caller at all** for a whole review round while three comments claimed they did |
| `-race` over the detached goroutine | it must hold no live `Ctx` |

## Level 4 — live scenario arms (`scripts/scenarios/`)

These need a real gateway and real money. **~1 hour wall clock, dominated by idle, and a few dollars.**
See the arm descriptions above for what each proves and how to read the output.

| Arm | Forces? | Proves |
|---|---|---|
| **A** `a-firing-rate.sh` | no | the firing rate, WHICH gate binds, the gap distribution, and the client's own compaction ceiling |
| **B** `b-cold-events.sh` | yes | the cold credit accumulates per prevented rewrite, and cold events fit inside the shipped span |
| **C** `c-warm-only.sh` | yes | a warm turn's credit is at the cache-READ rate, and equals `gross x rate` rather than the stored `saved_usd` |

### The traps, all of which produced a SILENTLY EMPTY run

A rig that fails silently certifies nothing, and each of these looked like "the feature does not work":

1. **`PATH` replaced rather than prepended** drops `~/.local/bin`, so every turn exits 127 while the log shows only "no rows".
2. **A gap of 310s** was past a 5-minute TTL and still inside the withdrawn gate's clock-skew margin, so the provider had already dropped the entry while the gate refused to say so. That gate is gone, so nothing waits for the arithmetic's permission any more — but a gap still has to clear the TTL **with room to spare**, because the *provider's* entry can outlive its nominal lifetime (a measured turn at 383s came back a partial hit). Arm B keeps `GAP=380` for that reason and checks the verdict it got rather than assuming one. The surviving margin constant is `apply.coldMargin`, which computes `Ctx.ColdCache`; `components.ColdMargin` no longer exists.
3. **Bridging turns that read files.** Two prompts that made the agent read large files wrote 20,095 tokens of new tail against a 20,000 span, closing it before the first idle gap — and produced a `$0.00` cold credit that was written up as a property of the design. Post-summary turns in a cold-event arm must add almost nothing.
4. **Reading the provenance GROUP instead of the EPISODE.** Group credit fields accumulate over CLOSED episodes only; an open episode's figures are in `open_net_usd`. Reading the group printed `0.00000000` for every bucket on a perfectly healthy run.
5. **Hand-checking a different population than the panel measured.** Summing `saved_gross` over every turn after t0 rather than over the turns inside the span disagreed by three whole cold events, and neither figure was wrong.
6. **Claude Code's `settings.json` env OVERRIDES the process env.** `ANTHROPIC_BASE_URL=... claude` does nothing; the first attempt sent every request to the production Guru without a word. The endpoint must be written into a copied `settings.json`.

### What a maintainer should re-run for a given change

| If you change… | Re-run |
|---|---|
| the gate (`trigger.go`, `cachephase.go`) | level 1, then arm A — the firing rate is the only thing that says whether a gate change matters |
| the fill **denominator** (`C`, `internal/compactionpoint`) | level 1, then arm **B** as a regression — it fires reliably, so a behaviour change there is a regression. On stock haiku `C = 0.996 × W`, so **no live run distinguishes the two denominators**; the Go test `TestTheFillFractionIsAFractionOfTheCompactionPointNotTheWindow` is the only thing that does, and arm D tests whether `C` can be derived rather than whether the denominator is right |
| the accounting (`compactepisode.go`) | level 2, then arms B **and** C — B checks the write-rate bucket, C the read-rate one, and a rate error shows in only one |
| the async path | level 3 including `-race`, then arm B (its t0+1 carries the deferred summarizer cost) |
| the client ceiling table | arm A on that model — it is the arm that measures the ceiling |
| anything touching `saved_usd` or `saved_gross` | arm C, which asserts the panel does **not** equal the stored `saved_usd` |

## What no scenario here covers

- **The production base rate** of a high-fill session going idle past the TTL. That is the number that decides whether the shipped default earns its place, and it is a query over stored data rather than an experiment. See "What this run does not prove".
- **A measured client ceiling for sonnet-5 or opus-5.** Only haiku has one. Reaching a 1M client's ceiling costs a full 1M-window session per model.
- **Behaviour at a real 1M fill.** The arms run on haiku's real 200,000 window; the arithmetic is exercised, the provider's behaviour near its actual limit is not.
- **Any client other than Claude Code.** Every ceiling figure, and the whole attribution boundary, is a statement about one client.
