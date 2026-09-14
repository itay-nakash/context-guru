# The plugin install is a script wearing a 423-line prompt

`/context-guru:install` is `skills/install/SKILL.md`: 423 lines, of which 39 are some form of
*"do not improvise this"*, *"this was a real defect"*, or *"observed on a hosted agent"*. Seven
numbered steps, every one of them a fixed sequence of three scripts that already exist and already
speak `key=value`.

This proposes moving steps 1–7 into `scripts/install.sh --route` and reducing the skill to: run one
command, read the plan, make at most two decisions, report. It is a determinism proposal, not a
feature.

## The one constraint that decides everything: prose cannot be granted, and prose cannot be pinned

Two mechanisms in Claude Code both key off the *literal text of a command*, and neither can see an
intention expressed in a skill:

1. **Permission rules match a command PREFIX.** `Bash(/path/to/scripts/**)` covers
   `.../start-proxy.sh --unrouted --upstream <url>` but not `SOMEVAR=1 .../start-proxy.sh`
   ([`docs/how-to/install-plugin.md:178`][d178]). An env-prefixed command is one nobody can approve.
2. **The auto-mode classifier reads the command, not the skill.** `--force` was denied for its *name*
   (`[Safety Bypass Flag]`) and had to be renamed `--unrouted`. A prefixed
   `ANTHROPIC_BASE_URL=… start-proxy.sh` was denied as `[Traffic Redirection]`.

So the surface that decides whether an install completes is the set of command strings it emits. A
skill that *describes* those strings in prose has, at best, influence over them. Every incident below
is the same shape: the prose was correct, and the emitted command differed from it.

## The evidence: every defect in this skill's history is a latitude defect

| What happened | Where the prose lives now |
|---|---|
| `${CLAUDE_PLUGIN_OPTION_PORT:-8787}` in a Bash call always expands to `8787` — plugin options reach hook environments only. The routing key named 8787 while every later hook read the *configured* port, self-gated on it, saw an unrouted project, and did nothing. The one running proxy had no auto-restart behind it; once it idle-exited, nothing brought it back, silently. | step 2, ~20 lines |
| Settings written *before* the proxy was started. Claude Code picks up an `env` change live, so the installing session's next API call went to a dead port and died with `Connection refused` — never reaching the step that starts the proxy. The installer produced the exact hang state the design exists to prevent. | step 5, ~10 lines, shouting |
| `on_path=false` was reported and ignored; the agent symlinked the binary into a directory under the plugin cache that happened to be on `PATH`. It worked, and would have broken at the next plugin update for a reason nobody would connect to a symlink. | step 5, ~8 lines |
| `--force` denied for its name; renamed `--unrouted`, with a paragraph asking future readers not to reintroduce the old name. | step 5 |
| An env-prefixed invocation was recommended two paragraphs after being prohibited, in the same step. Both the recommendation and the prohibition are still there. | step 5 |
| **2026-09-14:** after a denial, the agent hand-composed the paste-ready command for the user. `settings.py add` performs *no* validation on `--url` — verified: it accepts `127.0.0.1/anthropic` (no scheme, no port), writes it into the routing key, and reports `result=added` with exit 0. A project routed to an unroutable URL is a broken project, reported as a success. | nowhere — this one is not in the prose |

That last row is the argument in miniature. The skill says *"do not reword the command to look less
like what it is"*, which is exactly the right instruction and exactly the kind that cannot be
enforced. Nothing downstream checked the result.

> **On the 2026-09-14 evidence, precisely — and on what it is not.** The colleague's transcript
> shows `base_url=127.0.0.1/anthropic`, but also `dashboard: 127.0.0.1/dashboard`, and
> `start-proxy.sh:370` provably prints `http://127.0.0.1:${PORT}/dashboard/`. His terminal stripped
> schemes and ports from every URL it rendered, so his settings file was most likely fine. What is
> *verified* is only that nothing would have stopped it.
>
> **Uninstall is not affected, and an earlier revision of this document said it was.** `is_ours()`
> (`settings.py:78`) answers "did we write this?" from the `$context-guru.installed_base_url`
> record, never from the URL's shape — deliberately, because litellm's default is
> `http://127.0.0.1:4000/anthropic` and two local proxies are indistinguishable by URL. Verified:
> `remove` with no `--url` cleans up a malformed URL correctly, because the provenance matches. The
> defect is confined to `add` writing an unroutable value and calling it success.
>
> One secondary consequence is worth recording because it is not obvious: `_is_loopback()`
> (`settings.py:479`) returns `False` for a scheme-less URL, and it feeds the reset hatch's
> "is this file already routed?" check — the check whose stated purpose is to avoid taking a copy
> of an already-routed file and calling it an original. Provenance is the stronger signal in that
> function, so a normal install is safe; the hedge is that a malformed URL erodes a defence that
> exists for a reason.

## What actually needs judgment: two things

Reading all seven steps for decisions that a script cannot make, there are two:

1. **Which settings file.** Project (`.claude/settings.local.json`), team (`.claude/settings.json`),
   or machine-wide (`~/.claude/settings.json`). Blast radius differs by an order of magnitude and the
   third can lock the user out of every session they could use to fix it.
2. **What to do about a base URL that is already set.** Chain behind it (the right answer on a hosted
   agent or behind a corporate gateway), replace it, or abandon. Requires knowing whose endpoint it
   is, which is a conversation.

Everything else — resolving the port, ordering the proxy before the write, constructing the URL,
passing `--bin` when `on_path=false`, health-checking, recording the undo — is mechanical, and each
one has already been got wrong at least once by being left to interpretation.

`--mode` is a third input but not a third judgment call: it is a property of the deployment, known
before the install starts, and on DAM it is known by whoever built the pod. It belongs in the
invocation, not in a question to the user.

## The mechanism that makes it actually deterministic: `!` pre-execution

An orchestrator script alone does **not** make the install deterministic, and an earlier revision of
this document quietly assumed it did. The model still has to read the skill, work out that it should
run `install.sh`, and type the command — so the command string is still model-composed, which is the
one thing every incident in the table above has in common.

Claude Code has a mechanism that removes that step. In a skill body, `` !`<command>` `` **runs the
shell command before the skill content reaches the model**, and the output replaces the placeholder.
Multi-line form is a ```` ```! ```` fenced block. `allowed-tools:` in the frontmatter pre-approves
tools for the turn the skill invokes. Plugins ship skills, not `.claude/commands/`, so this stays in
`skills/install/SKILL.md` — it is a restructuring of that file, not a new artifact.

```markdown
---
name: install
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/scripts/**)
---

!`"${CLAUDE_PLUGIN_ROOT}/scripts/install.sh" --route --plan`

The block above is this project's real state. Read it, then: <~100 lines of how to read it>
```

The difference is not cosmetic:

- **the command string is fixed in a file the model cannot rewrite.** No wording, no flags, no
  re-typing after a denial. The 2026-09-14 incident — an agent hand-composing a paste-ready command
  and dropping the scheme and port — is not reachable, because no model composed anything.
- **the model receives output, not an instruction to obtain output.** It cannot skip the step,
  reorder it, or decide it already knows the port.
- **it is faster.** Seven steps of *decide → emit → read result* collapse into one pre-rendered block.
  Every removed round trip is also a removed opportunity to improvise.

### The principle this generalizes to: gated commands take no model-composed arguments

`--plan` is read-only, so pre-executing it is free. The *real* run needs the two decisions, and if the
model supplies them as argv then the dangerous command is model-composed again — exactly what we just
removed.

So decisions travel through a state file, not through `argv`:

```
1. !`install.sh --route --plan`        fixed string, no arguments        (skill renders)
2. model asks its one question in prose, gets an answer
3. install.sh --decide --scope project --on-conflict chain               (writes a decision file;
                                                                         touches no settings, starts
                                                                         nothing, so not gated)
4. !`install.sh --route --confirm`    fixed string, reads the decision  (the one gated command)
```

Step 4 is the only command that redirects traffic, and it is a **constant**. That is as deterministic
as this can get while still asking a human the two things only a human knows.

### The fully deterministic form: `! cg-install`, and the precedent already in the repo

Everything above still starts with `/context-guru:install`, and that is **not** fully deterministic.
Two residues remain: if the user asks in prose ("install context-guru") the model chooses whether and
which skill to invoke, and after the `` !`` `` block renders the model still decides what to do with
the output. `` !`` `` fixes the *command strings*; it does not remove the model from the loop.

The form that removes it entirely is the one the user types themselves:

```
! cg-install
```

A `!`-prefixed line in the Claude Code REPL is the user's own shell command. No model composes it, no
model reads it before it runs, and **no classifier gates it** — which is exactly why the two denied
steps in the 2026-09-14 install completed the moment the user pasted them by hand.

**This repo already ships this pattern.** The reset hatch is deliberately dropped at a plain path
outside the plugin (`~/.local/state/context-guru/context-guru-reset`) precisely so it works with "no
Claude, no network, no proxy and no plugin". `cg-install` is the same reasoning applied to the install:
a shim next to the binary in `~/.local/bin`, so the deterministic path is short enough to type instead
of 90 characters of plugin-cache path.

So there are two entry points, and they are for different things:

| | `/context-guru:install` | `! cg-install` |
|---|---|---|
| who chooses to run it | the model (from a skill invocation) | the user, directly |
| command string | fixed, in a `` !`` `` block | typed by the user |
| classifier | gates the confirm step (assume so until measured) | **not involved** |
| can ask the two questions conversationally | yes — this is its whole remaining job | no |
| discoverable | yes | only if we print it |

**The honest limitation, and why this is not simply better.** A shell script cannot hold the
conversation that resolves the two judgment calls — `!` gives it no reliable interactive stdin — so
`cg-install` must either take them as flags or refuse. It should refuse: default to project scope, and
on a foreign base URL exit 2 naming the value and the `--on-conflict` flag needed, rather than guess
about somebody's corporate gateway. That makes it the right tool for a re-run, a repair, a second
project, or a scripted pod, and the wrong tool for a first install on a machine whose environment
nobody has looked at yet.

Which is the real division of labour: **the model is there for the conversation, not for the
mechanism.** Every step that does not need a human sentence should be a fixed string, and the two that
do should be the only reason a model is in the path at all.

### Open questions, to settle by testing before building

Three, and the first one decides how much of "A" survives:

1. **Does `allowed-tools:` (or `!` pre-execution) clear the auto-mode classifier, or only the
   permission prompt?** They are different layers. If `allowed-tools` satisfies the classifier too,
   the step-0 permission-rule advice becomes unnecessary and A is fixed by construction. If it only
   satisfies permission rules, the `[Traffic Redirection]` gate remains and the rule advice stays.
   **Assume it does not until measured** — the classifier denied a command that a permission rule
   would have allowed, which is evidence they are independent.
2. **Does a `` !`` `` block that gets denied fail the whole skill invocation, or render as an error the
   model can read?** If the former, step 4 must stay a normal tool call so a denial is recoverable and
   the user gets the paste-ready command.
3. **Does `${CLAUDE_PLUGIN_ROOT}` expand inside `` !`` ``?** It expands in hook commands and in
   `allowed-tools` patterns; unverified in this position, and the whole shape depends on it.
4. **Is `~/.local/bin` on the PATH of the shell `!` uses?** `install.sh` already reports
   `on_path=false` for exactly this case, and if it is false then `! cg-install` does not resolve and
   the shim has to be printed as an absolute path — which costs it most of its point.

## The design: `install.sh --route`

### Flag surface

```
install.sh --route
    --mode local|attach           # default local. attach = point at a proxy that already
                                  # exists (DAM gateway); skips install and start entirely
    --base-url <url>              # attach only, and REQUIRED there. Validated, never derived
    --health-url <url>            # attach only, optional; default <base-url host>/healthz
    --no-health-check             # attach only. Writes routing without proving anything answers.
                                  # Prints what that costs: a routed project whose failure mode
                                  # is a HANG, which is the state the whole design avoids
    --scope project|team|user      # the first judgment call. `user` additionally requires
                                   # --i-understand-machine-wide, mirroring settings.py's
                                   # existing user_scope_needs_flag refusal
    --on-conflict chain|replace|abort   # the second. No default: absent + a foreign base URL
                                        # present = exit 2, print the value, change nothing
    --cache-strategy <name>        # see "C folds in" below. Default: 5-min-ping
    --upstream <url>               # only with --on-conflict chain; otherwise derived
    --plan                         # print the whole plan, touch nothing
```

Deliberately **not** flags: `--port`, `--preset`, `--idle-exit`.

They are resolved *inside* the script by calling `settings.py config` and falling back to the
`plugin.json` default **per option** (the partial-config trap that step 2 spends a paragraph on). No
caller can supply a shell default, so that entire bug class stops being reachable.

### Two install modes, and only one of them derives the URL

An earlier revision of this document made `--url` unrepresentable, on the argument that a URL nothing
types is a URL nothing can malform. That is right for a laptop and **wrong for DAM**, where
[the standing recommendation][transport] is to *land the proxy in the gateway* and ship the plugin as
the Claude-Code-session layer on top of it. There is no local port to derive from: the endpoint is a
policy-enforced gateway the pod never started and cannot health-check into existence.

So the mode is explicit, and it selects the step list:

```
--mode local      (default)  install the binary, start a proxy on 127.0.0.1:<resolved port>,
                             route to it. URL is DERIVED: http://127.0.0.1:<port>/anthropic
--mode attach --base-url URL  the proxy already exists elsewhere (DAM gateway, shared pod,
                             sidecar). Steps 1 and 5 are SKIPPED — nothing is installed and
                             nothing is started. URL is SUPPLIED and VALIDATED
```

`attach` is not a smaller `local`; it is a different install. It never downloads a binary, never
writes a pidfile, and must never start anything — a pod that starts its own proxy alongside a gateway
proxy is the double-interception case. What it still does: validate the URL, health-check it before
writing (`GET <base-url>/../healthz` is not assumable on a gateway, so `attach` accepts
`--health-url` or `--no-health-check` with the consequence stated), write the one key with
provenance, and record the undo.

**Because `attach` makes the URL an input again, validation stops being optional.** This is the
change to the sequencing at the end of this document: the `settings.py --url` guard moves from
"worth doing on its own terms" to a **prerequisite** for `attach` mode. The rule is not
`is_ours()` — that is provenance, not shape — and it is not `_is_loopback()`, which is deliberately
generous. It is a new `valid_base_url()`:

- parses, with an `http://` or `https://` scheme and a non-empty host;
- path ends in `/anthropic`, the path the proxy serves the Anthropic dialect on;
- an explicit port is **required for a loopback host** and **not required otherwise** — a gateway on
  `https://gw.internal/anthropic` is legitimate at 443, while `http://127.0.0.1/anthropic` is the
  malformed shape actually observed;
- refuses on failure with exit 2 and writes nothing.

In `local` mode the URL is still constructed internally from the resolved port, so the guard is
belt-and-braces there and load-bearing only for `attach`.

[transport]: 2026-08-30-claude-code-plugin-transport-design.md#does-this-help-the-dam-push

### The ordering becomes structural

```
                                                                       local   attach
1. install binary            → result=/path=/on_path=                    ✓      skip
2. resolve port/preset/idle  → settings.py config, per-option fallback   ✓       ✓
3. inspect target file + env → settings.py show, plus $ANTHROPIC_BASE_URL ✓      ✓
4. decide, or stop           → foreign base URL + no --on-conflict ⇒ 2   ✓       ✓
5. start proxy               → start-proxy.sh --unrouted --port …        ✓      skip
6. health check              → fail ⇒ exit 3, settings untouched         ✓       ✓
7. write the one key         → settings.py add --file … --url <url>      ✓       ✓
8. health check again        → fail ⇒ AUTO-REMOVE the key, then exit 3   ✓       ✓
```

In `local` the URL at step 7 is constructed from the port resolved at step 2. In `attach` it is
`--base-url`, validated before step 6 so a malformed value fails without a network call.

Step 5-before-7 is the ordering whose violation killed an installing session. As a numbered
paragraph it is a request; as line order in a script it is a fact.

Step 8's rollback is new. Today the skill *asks the model to offer* removing the routing key if
health fails — "say so and offer to remove the routing key". A routed project with no proxy is the
one state strictly worse than not installing, so it should not depend on the model choosing to
offer.

### Output contract

`key=value` on stdout, matching the three scripts' existing convention, with exit codes matching
`settings.py`: `0` success, `2` refusal needing a human decision, `3` operational failure,
`4` crash.

```
result=routed|planned|repaired|unchanged|error
mode=local|attach
scope=project|team|user
file=/abs/path
port=8787
preset=cache
cache_strategy=5-min-ping
base_url=http://127.0.0.1:8787/anthropic
upstream=            # empty unless chaining
chained=false
proxy=up
healthz=ok
backup=/abs/path.context-guru-backup-…
reset_hatch=/abs/path/context-guru-reset
permission_rule=Bash(/abs/plugin/root/**)
```

`--plan` emits the same keys with `result=planned` and writes nothing, so the model can ask its one
question with real values instead of `<target>` placeholders.

**What `--plan` is not for: getting the user to approve a plan.** An earlier revision argued it was
"approvable on its own", which quietly assumed a user reads `key=value` output and understands the
consequences better than they understand a bash command. They do not — a plan they cannot evaluate and
a command they cannot evaluate are the same opaque approval, and claiming otherwise is the kind of
consent theatre this repo's fail-open rule exists to avoid.

Its real value is narrower and holds up: the model's question becomes concrete, the resolved port
becomes visible before anything is baked into a settings file, and — once `--plan` runs via
`` !`` `` — the number of things the user must approve drops from two-or-more to **one**. What makes
that one approval legible is the model's plain-English sentence next to it ("you are already pointed
at your company's gateway; I would sit in front of it so it keeps handling your login"), not the
`key=value` block, which is for the model.

## What stays with the model

Not zero, and the residue is the part it is actually good at:

- the three-line "what this does" preamble, and the two risk sentences;
- reading `--plan` and putting the scope / conflict question to the user in their terms;
- interpreting `result=error reason=…` — `no_release_found` wants the source-build offer,
  `checksum_mismatch` wants a hard stop, `unparseable_json` wants hands off their file;
- the summary, including printing `reset_hatch=` verbatim on its own line.

Estimated skill size after: ~120 lines, most of it the error table.

## A folds in: the permission rule becomes step 0, and there is only one of it

The two gates the colleague hit are documented and correct: starting the proxy, and writing the
routing key ([`docs/how-to/install-plugin.md:189`][d190]). The doc's first section is
*"Recommended first: grant the plugin's scripts once"*, and line 184 records that a correct rule
clears both gates — confirmed on a hosted agent. **The skill never mentions it up front.** It
surfaces the rule only as option 3 *after* a denial has already stopped the install, which is how
one install became two manual `!` pastes.

Two changes, both cheap:

- the skill prints the rule, with the real absolute path, **before step 1** — the script emits
  `permission_rule=` so the path is never hand-typed;
- one orchestrator means **one gate instead of two**, and one prefix covers it.

## C folds in: `--cache-strategy`, and the strategy has a name

Today `config/config.go:381` is `"cache": {"cachesplit"}` and keep-alive is a separate
`cache.keepalive` bool, off by default. `start-proxy.sh:301` already loads
`${STATE}/keepalive-${PORT}.yaml` via `--config` if it exists. So arming keep-alive at install time
is: write that file. The mechanism needs nothing new.

What it needs is a **name**, so switching away and back is one word rather than four tuning
parameters. Three strategies exist in the code:

| Name | Pipeline / config | Cost | Honesty note |
|---|---|---|---|
| `split` | `preset: cache` (cachesplit alone) | free — no model calls | today's default; best-evidenced single component (−34.1% cost, 0→96.7% prefix hit) |
| `5-min-ping` | `split` + `cache.keepalive: true` | **spends the caller's credential on idle turns** | the new default. Defaults: ping at 280 s idle, ≤2 pings/span, ≤$0.25/ping, ≥20k-token prefix. Named for the provider's 5-minute TTL, which is why 280 s is the ceiling — `config.go` refuses ≥300 |
| `1-hour-head` | `cache.head_ttl_1h: true` | free | **offer it labelled.** `config/config.go:132` measured it GRANTED on Haiku 4.5 and *silently downgraded* on Sonnet 5 — zero 1h writes in 19,805 production requests. Honest projection on Opus/Sonnet is $0. `Usage.CacheWrite1h` is what proves otherwise |

### The strategy is settable in both modes — but through different doors

An earlier revision of this document said the strategy was "gateway-owned" in `attach` mode and the
picker should refuse. That is wrong: **the proxy in a DAM gateway is our binary**, so it has the same
keep-alive scheduler, and it has something `local` does not — a durable, named control plane.

`tenant.Strategy` (`tenant/keepalivestrategy.go:59`) already carries a `Name` alongside exactly the
tuning fields this feature needs (`IdleSeconds`, `MaxPings`, `MinPrefixTokens`, `MaxUSDPerPing`), plus
`Windows`, `Target` and `Active`. It is CRUD-backed and survives restart:

```
GET|POST      /api/keepalive/strategies        ctlManager   durable, named, account-wide
PATCH|DELETE  /api/keepalive/strategies/{id}   ctlManager
POST          /api/me/keepalive/sessions       ctlManager   one session, EPHEMERAL by design
DELETE        /api/me/keepalive/sessions/{id}  ctlTenant    withdrawal is never gated harder
DELETE        /api/me/keepalive                ctlTenant    account-wide off switch
```

So the picker has two backends for one vocabulary:

| | `local` | `attach` (DAM / hosted) |
|---|---|---|
| where it writes | `keepalive-<port>.yaml`, read by `start-proxy.sh:301` | `POST /api/keepalive/strategies` |
| takes effect | next proxy start — **requires a restart** | live; matching is in-memory and re-read on every write |
| durable | yes (file) | yes (registry). A per-session override is deliberately **not** — an authorization to spend must not outlive a restart |
| who may arm | whoever can write the file, i.e. the user | `ctlManager`. **This is the real constraint in `attach`, not ownership** — the pod needs a manager principal |
| who may disarm | same | any principal on their own account (`ctlTenant`), on purpose: withdrawal must never be harder than consent |

**The names must be one vocabulary across both.** `5-min-ping` should be the `Name` of a seeded
`tenant.Strategy` *and* the name written into the local YAML, so "switch back to `5-min-ping`" is the
same sentence on a laptop and on DAM. If the two deployments name the same parameters differently,
the naming has bought nothing.

Two honest limits for `attach`, neither of which is a refusal:

- **it needs a credential with the manager role.** If the pod has no such principal, the picker
  reports that and stops — it does not fall back to writing a local file nothing reads.
- **`local`'s restart requirement has no equivalent**, so the two modes' confirmation messages differ:
  `attach` can say "in effect now", `local` cannot and must not.

In `local` mode, `/context-guru:cache-strategy-picker` is the same code path as the install: resolve
the port, write the named config, stop the proxy the way uninstall does (pidfile, ownership
confirmed, never a `pkill` pattern), restart. It replaces `/context-guru:keepalive`'s hand-rolled heredoc, which already
carries a `REFUSING:` guard for an empty preset because `--config` *replaces* `--preset` rather than
layering over it — a trap a named strategy removes by construction, since the writer always knows
the preset it resolved.

**Two things default-on changes, and both must be said out loud.** `plugin.json` currently sells
`cache` as *"the prompt-cache split and NOTHING else: no content dropped, no markers, no extra tool,
no model calls."* With `5-min-ping` as the default, the last clause stops being true — idle pings
are model calls that cost money. That claim has to change in `plugin.json` and in the skill's
preamble, and the install summary has to name the strategy and its bounded cost in one line. The
guardrails make the spend bounded, not invisible.

## What this does not fix

- **The classifier still gates it, and should.** One command that starts a traffic-intercepting
  proxy and repoints `ANTHROPIC_BASE_URL` is a fair `[Traffic Redirection]` read. The goal is one
  grantable gate, not zero gates.
- **One command means one all-or-nothing denial.** Mitigated by `--plan`: it writes nothing, so it
  is approvable on its own, and a denial of the real run then happens with the full plan already on
  screen and a `permission_rule=` line ready to paste.
- **A plugin cannot grant itself permissions.** By design. Step 0 is advice, not a mechanism.
- **Chaining still needs a human.** Whose gateway `$ANTHROPIC_BASE_URL` names is not knowable from
  the string.

## Testing

`plugin_test.go` is 4,948 lines and a large share of it asserts *skill prose* — that a paragraph
still says a thing. Those assertions get shorter and much stronger: assert the script's `key=value`
contract, its exit codes, its refusal to write on a foreign base URL without `--on-conflict`, its
auto-removal on a failed post-write health check, and that the URL it constructs carries the
resolved port. Add fixtures for `--plan` against a settings file that is empty / already routed /
routed to a foreign endpoint / unparseable.

Go is not installed on this laptop, so these run on the eval box per that section of the root
`CLAUDE.md`; the shell and Python paths are exercisable there too.

## Recommendation

**Settle the three open questions above first — by testing, in one throwaway session.** All of them
are about mechanism, none needs this repo changed to answer, and the first decides whether A is a
prose fix or nothing at all. Building the orchestrator before knowing whether `` !`` `` clears the
classifier risks shipping a determinism story that still stops at a gate.

Then build the orchestrator, and land A and C on top of it rather than into prose that is about to be
replaced. Sequence:

0. **Measure**: does `allowed-tools` / `` !`` `` clear the auto-mode classifier; does a denied
   `` !`` `` block break the invocation; does `${CLAUDE_PLUGIN_ROOT}` expand there.
1. `install.sh --route` with `--plan`, `--decide` and `--confirm`, the contract above, and the install
   skill restructured around a `` !`` `` block and cut to ~120 lines. Carries A for free if question 1
   says the classifier is satisfied; otherwise keeps `permission_rule=` as an output key and the
   step-0 advice as prose.
2. `--cache-strategy` and `/context-guru:cache-strategy-picker`, with the `plugin.json` copy change
   that default-on keep-alive requires.
3. `valid_base_url()` in `settings.py`, gating `add`. **Not optional and not last if `attach` is in
   scope** — it is the only guard on a URL a human types, and `attach` exists precisely to let one be
   typed. If `attach` is deferred, this stays a small independent PR on its own merits, since
   `settings.py` is a public entry point other skills call.

[d178]: ../../how-to/install-plugin.md
[d190]: ../../how-to/install-plugin.md
