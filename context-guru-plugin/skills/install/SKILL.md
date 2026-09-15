---
name: install
description: Install a local context-guru proxy and route this project's Claude Code sessions through it, so long sessions stop paying to re-create the prompt cache. Use when the user asks to install, set up, enable, try or start context-guru, or to route Claude Code through it. Accepts --global to route every project on the machine instead of just this one, --cache-strategy <split|5-min-ping|1-hour-head> to override the default cache strategy, and --attach <url> to point at a proxy that already exists (a gateway or shared pod) instead of starting one.
---

# Install context-guru for Claude Code

<!-- Deliberately NO `allowed-tools:` here. The `!` block below pre-executes at render and needs no
     tool permission, so the only thing an allowed-tools line would grant is the ONE command that
     starts a traffic-intercepting proxy and repoints ANTHROPIC_BASE_URL. Measured in a real
     sandboxed session: with `Bash(.../install.sh)` declared, that command ran with no prompt at all.
     A plugin granting itself the permission the classifier exists to ask about is the plugin
     answering a question that belongs to its user. -->

!`"${CLAUDE_PLUGIN_ROOT}/scripts/install.sh" --route --plan`

**The block above is this project's real state, gathered before you were asked anything.** It ran
during rendering, so you did not choose to run it and cannot have mistyped it — read it rather than
re-deriving any of it. It wrote nothing and started nothing.

Your job is the part a script does badly: putting one question to a human, and reporting honestly.
Everything else — resolving the port, ordering the proxy before the routing key, deriving the URL,
health-checking, recording the undo — is in `install.sh --route`, where it is line order rather than
a numbered paragraph somebody can read differently.

## 1. Say what you are about to do, in three lines

- Routes this project's requests through a **local** proxy (`127.0.0.1`) that forwards to Anthropic.
  **No API key is added** — a Pro/Max login keeps working.
- **The real risk: if the proxy is down, requests HANG** rather than failing. A `UserPromptSubmit`
  hook restarts it. This is why one project, not the machine, is the default scope.
- `/context-guru:uninstall` reverses it. If routing itself breaks, no skill can run — so the install
  drops a plain-sh escape hatch outside the plugin, and step 4 tells them where.

If `cache_strategy=5-min-ping` (the default), one more clause: it spends a little of their own quota
on idle turns to hold the cache warm, and `/context-guru:cache-strategy-picker` switches it to
`split`.

Fuller detail is in `docs/how-to/install-plugin.md`. Point at it; do not recite it.

## 2. Ask ONCE, then run one command

**Read `result=` in the plan first.** A plan always exits 0 — `needs_decision` is something for you
to act on, not a failure to report as one.

- `result=planned` — the plan is clean. If `already_routed=true`, say so: this is a re-run or a
  repair, not a fresh install.
- `result=needs_decision reason=base_url_already_set` — `existing_base_url=` names somebody's endpoint. It
  may be their company gateway, a benchmark endpoint or another proxy. **This is the one question**,
  and on a hosted agent the answer is nearly always chain: our proxy sits in front and theirs keeps
  handling auth and model routing. Replacing it outright usually breaks that agent's authentication.
- `result=needs_decision reason=user_scope_needs_flag` — they passed `--global`. Confirm once, naming the
  blast radius: **every** Claude Code session on the machine, including projects that have nothing
  to do with context-guru, which is also every session they could use to fix it.
- `result=error reason=binary_install_failed` — read `detail=`. `no_release_found` wants the source
  build offer (`make build-static`, Go 1.26, no C toolchain). Anything naming a checksum
  (`checksum_mismatch`, `checksum_unavailable`, `checksum_absent`) is a **hard stop**: it is the only
  integrity check in the path and the binary is about to carry all of their LLM traffic. Never set
  `CONTEXT_GURU_INSECURE=1` on their behalf.

**One question, one sentence, covering everything that needs their agreement** — the scope, what to
do about an existing base URL, and what the cache strategy spends. Not one interview per parameter:

> You're already pointed at `<existing_base_url>`. I'd put context-guru in front of it so that
> gateway keeps handling your login, routing this project only, with keep-alive on as `5-min-ping`
> (a little of your own quota on idle turns, to hold the cache warm). OK?

### Get an explicit yes, as a choice they pick

**`--route` refuses to do anything without `--i-consent-to-traffic-interception`.** That is
deliberate and it is not a formality: everything the command does either intercepts their model
traffic or points it somewhere new, and the approval prompt cannot be relied on to ask about that —
it is probabilistic, a skill can declare it away, and in an unattended session there is no prompt
because there is no human. So the script fails closed and the consent has to come from a person.

**Ask it as a two-option choice, not as prose they can skim.** Use `AskUserQuestion` if you have it,
so it renders as something they pick rather than something they might answer sideways:

- **question**: one sentence naming what will happen — the local proxy, whose gateway you would chain
  behind if any, the scope, and that `5-min-ping` spends a little of their own quota on idle turns.
- **option 1 — "Yes, route this project"**: what they get, and that `/context-guru:uninstall` reverses it.
- **option 2 — "No, don't change anything"**: nothing is installed, started or written.

Without `AskUserQuestion`, ask in plain text with exactly two numbered options and stop for an answer.

**A silent or absent answer is a NO.** If nothing comes back — a non-interactive run, a session with
no human — report what the plan found and stop. Do not pass the flag on your own judgement, do not
infer consent from the fact that they typed `/context-guru:install`, and do not pass it because a
refusal is inconvenient. Passing it is you asserting that a person said yes.

### Then run one command

The plan printed it, ready to run, as `confirm_command=` — use that rather than assembling one:

```bash
"${CLAUDE_PLUGIN_ROOT}/scripts/install.sh" --route --scope project --on-conflict chain \
  --i-consent-to-traffic-interception
```

- `confirm_command=` already carries the scope, the mode, the base URL and the conflict decision the
  plan resolved, so the only thing you add is nothing — run it as printed;
- drop `--on-conflict` when the plan was `result=planned` with nothing already set;
- `--scope user --i-understand-machine-wide` only after they confirmed `--global`;
- `--cache-strategy <name>` only if they asked for a specific one;
- `--mode attach --base-url <url>` for `--attach`: nothing is installed and nothing is started, and
  the URL is validated before anything happens.

**Expect this one command to be gated, and do not try to get around it.** It starts a
traffic-intercepting proxy and repoints `ANTHROPIC_BASE_URL`; auto mode is right to ask, because
installing a plugin by name is not the same as consenting to have your model traffic intercepted.
The command names its own scope and upstream, so approving it **is** the consent rather than a second
copy of the question. If it is denied, hand them three options and no fourth: approve the prompt, run
that exact command themselves with `!`, or add the rule the plan printed as `permission_rule=`. Do
not reword the command to look like less than it is, and never write routing while no proxy answers.

## 3. Read the result

- `result=routed` — done. `settings_result=` says which: `added`, `unchanged` (already correct),
  `completed` (a repair of an earlier partial attempt — report it as success, not "nothing to do"),
  or `repointed` (moved to a new port).
- `result=error reason=health_check_failed` — nothing was written; the project is unrouted, which is
  a working project. Say what the log shows rather than guessing.
- `result=error reason=health_check_failed_after_write` with `rolled_back=true` — the routing key was
  **removed again** automatically. Say that plainly: they are unrouted, not broken.
- `result=error reason=settings_write_failed detail=unparseable_json` — their settings file was
  already broken. Do not rewrite it; tell them where it is.
- `result=refused reason=consent_required` — you ran it without the flag, or without asking. Nothing
  was installed, started or written. Go back and ask; do not simply re-run it with the flag appended.
- `strategy_warning=` — the cache strategy could not be written (usually a config at that path we
  did not write). The proxy is fine; mention it and move on.

## 4. Then tell them

- **Do not say it only takes effect next session.** Claude Code picks the `env` change up live —
  that is why the proxy is started first. What is true: this session began before the proxy existed,
  so `/context-guru:status` may have nothing to show yet, and a new session is the clean way to look.
- Name the **cache strategy** from the result, and what it costs.
- Dashboard: `http://127.0.0.1:<port>/dashboard/` — the four billed token tiers are where the cache
  effect shows.
- If `port` is not 8787, say so; a non-default port is the kind of thing people forget they set.
- `/context-guru:status` for numbers, `/context-guru:cache-strategy-picker` to change the strategy,
  `/context-guru:uninstall` to undo.
- **`reset_hatch=` verbatim, on its own line, as your last line.** This is the only moment the user
  is certain to be able to read it: the failure it exists for is "every request through the proxy
  fails", and in that state no skill can run — including uninstall. It happened to a colleague.

  ```
  If Claude ever stops being able to talk after this, run: <the reset_hatch path>
  ```

  `reset_hatch=unavailable` means the state directory was unwritable — say so, because then their
  only undo is the `backup=` path.

## Do not

- **Do not investigate their machine.** No `env | grep` over credentials, not `ANTHROPIC_API_KEY`,
  not `AWS_*`; no `ps aux`, `lsof` or port scanning. An unbounded version of this was denied as
  `[Credential Exploration]` on a real install. This plugin does not read credentials and must not
  appear to — a proxy plugin sweeping for API keys is indistinguishable from the thing people are
  right to fear. The plan already tells you every environment fact you need.
- **Do not re-run the individual scripts** (`settings.py add`, `start-proxy.sh`) to do this by hand.
  The ordering between them is load-bearing and getting it wrong once killed the installing session.
- Do not add any other key. Not `ANTHROPIC_API_KEY`, not `ANTHROPIC_AUTH_TOKEN` — a credential
  variable is what would take them off subscription billing.
- Do not prefix the command with environment variables. Permission rules match by command PREFIX, so
  `FOO=1 .../install.sh` is a command nobody can approve. Every option is a flag for that reason.
- **Do not pass `--i-consent-to-traffic-interception` unless a person answered yes to a question you
  asked.** It is not a flag that makes a refusal go away; it is you telling the script, on their
  behalf, that they agreed to have their model traffic intercepted.
- Do not claim it works because a command exited 0. `result=routed` is the claim.
