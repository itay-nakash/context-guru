#!/usr/bin/env bash
# Put a context-guru-proxy binary on the user's PATH, without a toolchain.
#
# The binary is statically linked pure Go (scripts/gate-a-purego.sh proves it, and the release
# workflow asserts it on every run), so this is a download, a checksum, and a move. No
# compiler, no runtime dependencies, nothing to configure.
#
# Ranked by friction, and this script tries them in that order:
#
#   1. ALREADY INSTALLED — do nothing, report the version. This script is idempotent because
#      the install skill may be re-run, and re-downloading 30 MB to end up where we started is
#      not a neutral act on someone's laptop.
#   2. RELEASE TARBALL — the default path. Checksum-verified against the release's
#      checksums.txt, and on macOS the quarantine attribute is stripped, otherwise Gatekeeper
#      refuses an unsigned download with "cannot be verified" and the trial ends there.
#   3. go install — only if a Go toolchain is already present. Cheap to offer, and some people
#      would rather build.
#
# There is deliberately NO `brew` path yet: the tap repo does not exist and release signing is
# an open ownership question (see the spec). A `brew install` line that fails is worse than one
# that is absent, and adding the tap later changes nothing else here.
#
# Every fact this discovers is printed as `key=value` on stdout, so the calling skill can act
# on the outcome without parsing prose.
set -uo pipefail

REPO="${CONTEXT_GURU_REPO:-rossoctl/context-guru}"
VERSION="${CONTEXT_GURU_VERSION:-latest}"
DEST="${CONTEXT_GURU_DEST:-$HOME/.local/bin}"
BIN=context-guru-proxy

emit() { printf '%s\n' "$*"; }
die()  { emit "result=error"; emit "reason=$1"; exit 1; }

# =========================================================================================
# --route: the orchestrator
# =========================================================================================
#
# Steps 1-7 of skills/install/SKILL.md, as ONE script. The argument for moving them here is in
# docs/superpowers/specs/2026-09-14-plugin-install-orchestrator-design.md; the short version is
# that every defect in that skill's history has the same shape — the prose was right and the
# command a model emitted differed from it — and an ordering expressed as a numbered paragraph is
# a request, while the same ordering expressed as line order in a script is a fact.
#
# Two modes:
#   --mode local  (default)  install the binary, start a proxy on 127.0.0.1:<resolved port>,
#                            route to it. The URL is DERIVED and never passed in.
#   --mode attach            the proxy already exists elsewhere (a DAM gateway, a shared pod).
#                            Steps 1 and 5 are SKIPPED — nothing installed, nothing started —
#                            and --base-url is supplied and validated.
#
# Everything that needs a human is a FLAG, and there are exactly two such decisions: --scope and
# --on-conflict. They travel in argv rather than in a state file, deliberately: a gated command
# that reads its decisions from a file elsewhere shows the user nothing when they are asked to
# approve it, which turns one meaningful consent into two meaningless ones.
#
# SELF-LOCATION, and why it is not $CLAUDE_PLUGIN_ROOT: measured 2026-09-14, that variable is
# substituted into a `!`-block's command STRING but is NOT exported to the child process. A script
# reading it from its own environment gets nothing. So this locates its siblings from $0.

route_here() { CDPATH= cd -- "$(dirname -- "$0")" && pwd -P; }

# Every fact the plan and the confirm both report. Kept in one place so `--plan` cannot describe a
# different install from the one `--confirm` performs.
R_MODE=local R_SCOPE=project R_ONCONFLICT= R_BASEURL= R_HEALTHURL= R_NOHEALTH=0
R_STRATEGY= R_UPSTREAM= R_USERSCOPE=0 R_PLAN=0 R_CONFIRM=0
R_PORT= R_PRESET= R_IDLE= R_BIN= R_ONPATH= R_FILE= R_EXISTING= R_CHAINED=false
R_ALREADY=false R_CONSENT=0 R_OURS= R_FROMENV=0

route_die() { emit "result=error"; emit "reason=$1"; [ -n "${2:-}" ] && emit "detail=$2"; exit 3; }

# route_refuse is for the WRITING path: exit 2, a refusal a caller can distinguish from a failure.
#
# route_needs is for anything the PLAN discovers, and under --plan it exits 0. That distinction is
# not cosmetic, and getting it wrong made this script unusable in the exact shape it was designed
# for: the install skill runs `--route --plan` from a `!` block, the commonest real state of a
# hosted machine is "a base URL is already set", and the first version exited 2 there. Measured in a
# real sandboxed session, the whole skill invocation then produced NO OUTPUT AT ALL — the model
# never saw the plan, never asked the question, and the run looked like a success because nothing
# had been written.
#
# So: a plan REPORTS. `needs_decision` is data for the caller to act on, not an error to propagate,
# and only a command that actually declines to do something exits non-zero.
route_refuse() { emit "result=refused"; emit "reason=$1"; [ -n "${2:-}" ] && emit "note=$2"; exit 2; }
route_needs() {
  if [ "$R_PLAN" = 1 ]; then
    emit "result=needs_decision"; emit "reason=$1"
    [ -n "${2:-}" ] && emit "note=$2"
    [ -n "$R_EXISTING" ] && emit "existing_base_url=$R_EXISTING"
    emit "permission_rule=$(route_permission_rule)"
    exit 0
  fi
  route_refuse "$1" "${2:-}"
}

# One value from a key=value block, last occurrence wins (install.sh prints result= last).
kv() { printf '%s\n' "$1" | sed -n "s/^$2=//p" | tail -1; }

route_scope_file() {
  case "$R_SCOPE" in
    project) printf '%s\n' "$PWD/.claude/settings.local.json" ;;
    team)    printf '%s\n' "$PWD/.claude/settings.json" ;;
    user)    printf '%s\n' "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/settings.json" ;;
    *)       route_refuse "unknown_scope" "--scope must be project, team or user" ;;
  esac
}

# The rule the plugin cannot enforce for itself: a permission rule covers a command by PREFIX, so
# one rule over this directory covers every command the install runs. Printed as a fact so the
# absolute path is never hand-typed by a model.
route_permission_rule() { printf 'Bash(%s/**)\n' "$(dirname -- "$(route_here)")"; }

route_resolve_options() {
  local cfg
  cfg=$("$(route_here)/settings.py" config 2>/dev/null) || cfg=""
  # Per-option fallback, never "source= was set so everything is set". `settings.py config` prints
  # a line only for keys the user actually configured, so a partial config — port set, preset
  # never touched — reports a real source= and simply omits option_preset=.
  R_PORT=$(kv "$cfg" option_port);            : "${R_PORT:=8787}"
  R_PRESET=$(kv "$cfg" option_preset);        : "${R_PRESET:=cache}"
  R_IDLE=$(kv "$cfg" option_idle_exit);       : "${R_IDLE:=24h}"
  [ -z "$R_STRATEGY" ] && R_STRATEGY=$(kv "$cfg" option_cache_strategy)
  : "${R_STRATEGY:=5-min-ping}"
  [ -z "$R_UPSTREAM" ] && R_UPSTREAM=$(kv "$cfg" option_upstream)
}

route_inspect() {
  local shown
  R_FILE=$(route_scope_file) || exit $?
  shown=$("$(route_here)/settings.py" show --file "$R_FILE" 2>/dev/null) || shown=""
  R_EXISTING=$(kv "$shown" base_url)
  R_OURS=$(kv "$shown" ours)
  # `settings.py show` prints the SENTINEL `(unset)` rather than an empty value, and reading that
  # as a real base URL made every clean project look already-routed — so the install refused with
  # `base_url_already_set` and named `(unset)` as the conflicting endpoint. Caught by the first
  # smoke run; it is the exact class of bug this script exists to remove, arriving in the script
  # itself. Normalise every "absent" spelling the siblings use, not just the one seen.
  case "$R_EXISTING" in "(unset)"|"(none)"|"") R_EXISTING= ;; esac
  # The environment matters as much as the file: on a hosted or containerised agent the base URL is
  # often set in the process environment and no settings file mentions it at all, so `show` reports
  # exists=false while the session is already routed somewhere.
  if [ -z "$R_EXISTING" ] && [ -n "${ANTHROPIC_BASE_URL:-}" ]; then
    R_EXISTING="$ANTHROPIC_BASE_URL"
    R_FROMENV=1
  fi
  # Already ours is NOT a conflict — but it is not nothing either, and reporting it as an empty
  # existing_base_url would let a re-run be narrated as a fresh install. Say which it is.
  #
  # This asked the URL's SHAPE (`*127.0.0.1:$R_PORT*`), which is the inference valid_base_url()'s own
  # docstring forbids twenty lines away in the sibling this calls: two local proxies are
  # indistinguishable by URL. So somebody else's proxy on our port was reported as ours, with
  # `existing_base_url=` emptied — the one question this design says can never be defaulted was never
  # asked, and the skill narrated it as "a re-run or a repair". Port 4000 would have claimed litellm's
  # own endpoint, the exact collision the docstring names.
  #
  # `show` now reports `ours=`, answered from the recorded installed_base_url. Prefer it whenever the
  # value came from the FILE, which is the case the shape test got wrong.
  if [ "$R_OURS" = true ]; then
    R_EXISTING=; R_ALREADY=true
  elif [ "$R_FROMENV" = 1 ]; then
    # The value came from the ENVIRONMENT, so there is no record to consult and shape is the only
    # signal there is. Kept, but anchored on `//host:port/` — the old unanchored match made port 8787
    # claim an endpoint on 87870. Stated plainly as the residual inference: a foreign proxy on our
    # port, injected through the environment rather than a file, still reads as ours here.
    case "$R_EXISTING" in
      *"//127.0.0.1:${R_PORT}/"*|*"//localhost:${R_PORT}/"*) R_EXISTING=; R_ALREADY=true ;;
    esac
  fi
  # A value in the FILE that is not ours stays exactly where it is: a conflict, reported, and
  # answerable only by a human. That is the case the shape match silently swallowed.
}

# The URL is DERIVED in local mode and never accepted as an argument, which is what makes the
# malformed-URL class unreachable from this path. In attach mode it is supplied, and validated by
# settings.py's valid_base_url() before anything is written.
route_url() {
  if [ "$R_MODE" = attach ]; then printf '%s\n' "$R_BASEURL"
  else printf 'http://127.0.0.1:%s/anthropic\n' "$R_PORT"; fi
}

route_health_url() {
  if [ -n "$R_HEALTHURL" ]; then printf '%s\n' "$R_HEALTHURL"
  elif [ "$R_MODE" = attach ]; then
    # Strip the trailing /anthropic and ask for /healthz beside it. Not assumable on a gateway,
    # which is why --health-url and --no-health-check both exist.
    printf '%s\n' "${R_BASEURL%/anthropic}/healthz"
  else printf 'http://127.0.0.1:%s/healthz\n' "$R_PORT"; fi
}

route_health_ok() {
  [ "$R_NOHEALTH" = 1 ] && return 0
  command -v curl >/dev/null 2>&1 || return 0   # fail open: no curl is not evidence of a dead proxy
  curl -fsS --max-time 5 "$(route_health_url)" >/dev/null 2>&1
}

# The exact command that would perform this install, decisions included. Printed by the plan and by
# the consent refusal so nothing has to be reassembled by hand — the failure mode on 2026-09-14 was a
# model re-typing a command and dropping part of it.
# Quote a value so it cannot contribute SYNTAX to the line we print for someone to run. Values that
# are already unambiguous pass through untouched, because the place this line is most likely to be
# read is a permission prompt shown to a human, and `--scope 'project'` reads worse than `--scope
# project` while being no safer.
#
# This is not hypothetical tidiness. `--base-url` was interpolated raw, valid_base_url() checked the
# host only for emptiness, and the two together were an arbitrary-command hole: a `check-url`-approved
# `http://x;touch /tmp/PWNED;cd /anthropic` produced a confirm_command whose payload EXECUTED when the
# line was run the way SKILL.md and the suite both run it. It fired regardless of the consent answer,
# because it rides the string the *plan* prints and the gate is downstream in route_main. The host
# character class in settings.py closes today's vector; this closes the class, so the next value added
# to this line cannot reopen it.
shq() {
  case "$1" in
    ""|*[!A-Za-z0-9._:/=@,+-]*) printf "'%s'" "${1//\'/\'\\\'\'}" ;;
    *)                          printf '%s' "$1" ;;
  esac
}

route_confirm_command() {
  local c="$(shq "$(route_here)/install.sh") --route --scope $(shq "$R_SCOPE")"
  [ "$R_MODE" != local ] && c="$c --mode $(shq "$R_MODE")"
  [ -n "$R_BASEURL" ] && c="$c --base-url $(shq "$R_BASEURL")"
  [ -n "$R_ONCONFLICT" ] && c="$c --on-conflict $(shq "$R_ONCONFLICT")"
  # The strategy was missing, and it is the one decision that spends the user's money. Dropped from
  # here, a user who asked for `split` ("install it, but do not spend my quota") ran the printed
  # command, the strategy re-resolved to the `5-min-ping` default, and the install reported
  # `result=routed` — success, while doing the opposite of what was asked. The consent artefact has
  # to name the whole proposition the user was asked to agree to, not just the interception.
  [ -n "$R_STRATEGY" ] && c="$c --cache-strategy $(shq "$R_STRATEGY")"
  [ "$R_USERSCOPE" = 1 ] && c="$c --i-understand-machine-wide"
  printf '%s --i-consent-to-traffic-interception\n' "$c"
}

# The proposition a human is asked to agree to, generated from the SAME resolved facts as the command
# above rather than composed from the skill's example paragraph. Two things drifted apart before this
# existed: the question mentioned the spending strategy and the gated command did not.
route_consent_question() {
  local q="route this project's model traffic through $(route_url)"
  [ "$R_SCOPE" = user ] && q="route THIS MACHINE's model traffic (every project) through $(route_url)"
  [ -n "$R_UPSTREAM" ] && q="$q, chained in front of $R_UPSTREAM"
  if [ "$R_MODE" = attach ]; then
    q="$q (attach mode: nothing is started, the URL is assumed to be already serving)"
  fi
  case "$R_STRATEGY" in
    5-min-ping) q="$q, with cache strategy 5-min-ping, which SPENDS THE USER'S OWN QUOTA on idle \
turns to hold the cache warm" ;;
    *)          q="$q, with cache strategy $R_STRATEGY (no spend)" ;;
  esac
  printf '%s\n' "$q"
}

# start-proxy.sh derives this identically; only files that EXIST are reported, so if the two
# derivations ever drift the symptom is a missing line rather than a wrong path.
route_state_dir() {
  printf '%s\n' "${CONTEXT_GURU_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/context-guru}"
}

# What steps 4b and 5 may have left behind, reported on every failure path.
#
# "SETTINGS WERE NOT TOUCHED" is true, and "nothing happened" is what a caller infers from it. A proxy
# left listening on a port, with a pidfile, a dashboard DB and a strategy config, is not nothing — and
# the skill was instructing the model to say "nothing was written; the project is unrouted, which is a
# working project", whose first clause was false. It matters most when the health check fails
# TRANSIENTLY (a slow start, a busy laptop): the user is told nothing happened, and their retry meets a
# stale pidfile on an occupied port.
#
# This reports rather than cleans up. Stopping a proxy needs the pidfile-first, ownership-confirmed path
# that /context-guru:uninstall already owns, and half of one here would be worse than an honest line.
route_report_side_effects() {
  local st pf sf pid
  st=$(route_state_dir)
  pf="$st/proxy-${R_PORT}.pid"
  sf="$st/keepalive-${R_PORT}.yaml"
  if [ -f "$pf" ]; then
    emit "pidfile=$pf"
    pid=$(cat "$pf" 2>/dev/null)
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      emit "proxy_started=true"
      emit "proxy_pid=$pid"
      emit "stop_command=kill $pid"
      emit "side_effect_note=A PROXY IS STILL RUNNING on port ${R_PORT} and was NOT stopped. Say so; \
do not tell the user nothing happened. /context-guru:uninstall stops it, or run stop_command above."
    else
      emit "proxy_started=false"
      emit "side_effect_note=a pidfile is present with nothing running behind it, so a retry meets a \
stale pidfile on port ${R_PORT}."
    fi
  fi
  [ -f "$sf" ] && emit "strategy_file=$sf"
  return 0
}

route_report() {
  emit "mode=$R_MODE"
  emit "scope=$R_SCOPE"
  emit "file=$R_FILE"
  emit "port=$R_PORT"
  emit "preset=$R_PRESET"
  emit "idle_exit=$R_IDLE"
  emit "cache_strategy=$R_STRATEGY"
  emit "base_url=$(route_url)"
  emit "health_url=$(route_health_url)"
  emit "existing_base_url=$R_EXISTING"
  emit "already_routed=$R_ALREADY"
  emit "chained=$R_CHAINED"
  emit "upstream=$R_UPSTREAM"
  emit "permission_rule=$(route_permission_rule)"
  emit "consent_required=true"
  emit "consent_question=$(route_consent_question)"
  emit "confirm_command=$(route_confirm_command)"
}

route_main() {
  route_resolve_options

  # A typo'd strategy name used to reach step 4b, where `settings.py strategy set` correctly refused it
  # with exit 2 — and `|| true` plus a catch-all `*)` turned that refusal into `strategy_warning=`.
  # No config file written means `split`, so `--cache-strategy 5-minute-ping` installed a DIFFERENT
  # mechanism from the one named and reported `result=routed`. That is the same shape as the unknown
  # flag this script refuses eighty lines down: a dropped decision that reports success. Checked here,
  # before a binary is downloaded, against the one machine-readable list of names.
  local names
  names=$("$(route_here)/settings.py" strategy list 2>/dev/null | sed -n 's/^names=//p')
  case ",${names}," in
    *",${R_STRATEGY},"*) : ;;
    *) route_needs "unknown_strategy" "--cache-strategy $R_STRATEGY is not a strategy. Known: \
${names:-unavailable}. Nothing was installed, started or written." ;;
  esac

  if [ "$R_MODE" = attach ]; then
    [ -n "$R_BASEURL" ] || route_needs "attach_needs_base_url" \
      "--mode attach has no local port to derive a URL from; pass --base-url"
    # Validate BEFORE doing anything, not at the write. `attach` exists precisely so a human can
    # type a URL, and the first version of this only found a typo at step 7 — after a health check
    # had been spent — reporting it as `settings_write_failed`, which names the wrong step.
    local vout
    vout=$("$(route_here)/settings.py" check-url --url "$R_BASEURL" 2>&1) || {
      if [ "$R_PLAN" = 1 ]; then
        emit "result=needs_decision"; emit "reason=invalid_base_url"
        emit "detail=$(kv "$vout" detail)"; emit "url=$R_BASEURL"
        emit "note=the supplied --base-url cannot be used; nothing was checked or written"
        exit 0
      fi
      emit "result=refused"; emit "reason=invalid_base_url"
      emit "detail=$(kv "$vout" detail)"; emit "url=$R_BASEURL"
      emit "note=nothing was started and nothing was written. In attach mode the URL is the one \
thing that cannot be derived, so it is the one thing checked first."
      exit 2
    }
  else
    [ -z "$R_BASEURL" ] || route_needs "base_url_is_local_mode_nonsense" \
      "--base-url is for --mode attach; in local mode the URL is derived from the resolved port"
  fi
  [ "$R_SCOPE" = user ] && [ "$R_USERSCOPE" != 1 ] && route_needs "user_scope_needs_flag" \
    "--scope user routes EVERY project on this machine, including every project that has nothing to \
do with context-guru. Confirm with the user, then add --i-understand-machine-wide"

  route_inspect

  # THE ONE DECISION THAT CANNOT BE DEFAULTED. A base URL already set may be their company gateway,
  # a benchmark endpoint or another proxy; replacing it silently breaks their setup while looking
  # like success, and guessing which it is is not something a script can do.
  if [ -n "$R_EXISTING" ] && [ -z "$R_ONCONFLICT" ]; then
    if [ "$R_PLAN" = 1 ]; then
      # THE COMMON CASE on a hosted machine, and the reason route_needs exists. Report the whole
      # plan alongside it: the model has one question to ask and needs the real values to ask it.
      emit "result=needs_decision"; emit "reason=base_url_already_set"
      emit "note=ask the user, then re-run with --on-conflict chain (usually right: our proxy sits \
in front and theirs keeps handling auth), replace (theirs is recorded and uninstall puts it back), \
or abort"
      route_report
      exit 0
    fi
    emit "result=refused"; emit "reason=base_url_already_set"
    emit "existing_base_url=$R_EXISTING"
    emit "note=pass --on-conflict chain (usually right: our proxy sits in front and theirs keeps \
handling auth), replace (theirs is recorded and uninstall puts it back), or abort"
    exit 2
  fi
  if [ -n "$R_EXISTING" ] && [ "$R_ONCONFLICT" = abort ]; then
    emit "result=aborted"; emit "existing_base_url=$R_EXISTING"
    emit "note=nothing was changed, at the caller's request"; exit 0
  fi
  if [ -n "$R_EXISTING" ] && [ "$R_ONCONFLICT" = chain ]; then
    R_CHAINED=true
    [ -z "$R_UPSTREAM" ] && R_UPSTREAM="$R_EXISTING"
  fi

  if [ "$R_PLAN" = 1 ]; then
    emit "result=planned"
    if [ "$R_MODE" = attach ]; then emit "binary=(not needed in attach mode)"
    else emit "binary=would_check"; fi
    route_report
    emit "note=nothing was written, nothing started. Re-run without --plan to perform this."
    exit 0
  fi

  # ---- CONSENT GATE ---------------------------------------------------------------------
  #
  # Refuses to act at all without an explicit token. Everything below this line either intercepts the
  # user's model traffic or points it somewhere new, and three measurements say the approval prompt
  # cannot be relied on to ask about that:
  #
  #   * the auto-mode classifier is itself a model, so it is probabilistic — the same command string
  #     was denied in one trial and allowed in two others;
  #   * a skill can declare the prompt away. With `allowed-tools: Bash(.../install.sh)` in its
  #     frontmatter, this exact command ran with NO prompt at all;
  #   * in a non-interactive session there is no prompt, because there is no human to ask — and the
  #     measured result was a complete, unattended install of a traffic interceptor.
  #
  # The third is what this gate is really for: an unattended run now FAILS CLOSED instead of quietly
  # succeeding. What it cannot do is prove a human said yes — a caller can pass the flag unprompted.
  # It converts "we hope a prompt fires" into "this refuses without a token that only exists because
  # someone asked", which is a deterministic precondition and an auditable one, not a guarantee.
  if [ "$R_CONSENT" != 1 ]; then
    emit "result=refused"
    emit "reason=consent_required"
    emit "consent_required=true"
    emit "base_url=$(route_url)"
    emit "existing_base_url=$R_EXISTING"
    # The gate guards the ACT; it has to name the TERMS too. What the flag is spelled to gate is
    # "traffic gets intercepted", while what the user is actually asked includes which scope and which
    # cache strategy — and one of those strategies spends their own quota while nobody is at the
    # keyboard. Ask a narrower question than the command performs and the consent artefact is
    # under-specified relative to the consent. consent_question= is generated from the same resolved
    # facts as confirm_command=, so the two cannot drift.
    emit "consent_question=$(route_consent_question)"
    emit "note=nothing was installed, started or written. Ask the user to agree to exactly what \
consent_question= says, as a choice they pick, and pass --i-consent-to-traffic-interception only if \
they say yes. A silent or absent answer is a NO. Never pass it on your own judgement."
    emit "confirm_command=$(route_confirm_command)"
    exit 2
  fi

  # ---- step 1: the binary (local only) --------------------------------------------------
  # Re-invokes THIS script with no arguments rather than refactoring its linear body: the installer
  # already speaks key=value and already exits early on `result=present`, and $0 is the same
  # self-location the rest of this function uses.
  if [ "$R_MODE" = local ]; then
    local iout ires
    iout=$("$0" 2>&1); ires=$(kv "$iout" result)
    case "$ires" in
      present|installed) : ;;
      *) emit "result=error"; emit "reason=binary_install_failed"
         emit "detail=$(kv "$iout" reason)"
         emit "note=nothing else was touched. A checksum failure must never be worked around."
         exit 3 ;;
    esac
    R_ONPATH=$(kv "$iout" on_path)
    [ "$R_ONPATH" = false ] && R_BIN=$(kv "$iout" path)
  fi

  # ---- step 4b: the cache strategy, BEFORE the proxy starts -----------------------------
  # start-proxy.sh reads that file only when it STARTS a proxy, so a strategy written afterwards
  # does nothing until something restarts it. Written here, the very first proxy has it.
  local sout
  sout=$("$(route_here)/settings.py" strategy set --name "$R_STRATEGY" \
           --port "$R_PORT" --preset "$R_PRESET" 2>&1) || true
  case "$(kv "$sout" result)" in
    set|cleared|unchanged) : ;;
    # `not_ours` is genuinely non-fatal: a config we did not write is not a reason to refuse an
    # install. An unknown NAME is different in kind — it means the caller asked for something that
    # does not exist — and it is refused in route_main before the binary is touched, so it cannot
    # reach here. Anything else keeps the fail-open behaviour, which is where it belongs.
    *) emit "strategy_warning=$(kv "$sout" reason)" ;;
  esac

  # ---- step 5: start the proxy (local only), BEFORE writing any settings ----------------
  # Claude Code picks a settings `env` change up while the session is RUNNING. Writing the key
  # first once killed the installing session: its next API call went to a dead port and died with
  # Connection refused, never reaching the step that starts the proxy.
  if [ "$R_MODE" = local ]; then
    local sp=("$(route_here)/start-proxy.sh" --unrouted --port "$R_PORT"
              --preset "$R_PRESET" --idle-exit "$R_IDLE")
    [ -n "$R_UPSTREAM" ] && sp+=(--upstream "$R_UPSTREAM")
    [ -n "$R_BIN" ] && sp+=(--bin "$R_BIN")
    "${sp[@]}" || true
  fi

  # ---- step 6: prove something answers, BEFORE routing to it ----------------------------
  if ! route_health_ok; then
    emit "result=error"; emit "reason=health_check_failed"
    emit "health_url=$(route_health_url)"
    emit "note=SETTINGS WERE NOT TOUCHED. An unrouted project with no proxy is a working project; \
a routed one with no proxy is a broken one."
    # ...but "settings were not touched" is not "nothing happened", and the caller will read it as
    # the latter. Step 5 may have started a proxy and left a pidfile and a strategy config behind.
    # That matters most when a health check fails TRANSIENTLY (a slow start, a busy laptop): the user
    # is told nothing happened, and their retry meets a stale pidfile on an occupied port.
    route_report_side_effects
    exit 3
  fi

  # ---- step 7: write the one key -------------------------------------------------------
  local aout acode=0
  local add=("$(route_here)/settings.py" add --file "$R_FILE" --url "$(route_url)")
  [ -n "$R_UPSTREAM" ] && add+=(--upstream "$R_UPSTREAM")
  [ -n "$R_BIN" ] && add+=(--bin "$R_BIN")
  [ "$R_SCOPE" = user ] && add+=(--user-scope)
  [ "$R_ONCONFLICT" = replace ] && add+=(--force)
  aout=$("${add[@]}" 2>&1) || acode=$?
  local ares; ares=$(kv "$aout" result)
  case "$ares" in
    added|completed|unchanged|repointed) : ;;
    *) emit "result=error"; emit "reason=settings_write_failed"
       emit "detail=$(kv "$aout" reason)"; emit "exit=$acode"
       emit "note=no routing was written."
       route_report_side_effects        # "may be running" is knowable; say which.
       exit 3 ;;
  esac

  # ---- step 8: re-check, and UNDO the routing if nothing answers ------------------------
  # The rollback is automatic on purpose. The skill used to ask the model to OFFER removing the key
  # here, and a routed project with no proxy is the one state strictly worse than not installing —
  # too important to depend on a model choosing to offer it.
  if ! route_health_ok; then
    "$(route_here)/settings.py" remove --file "$R_FILE" --url "$(route_url)" >/dev/null 2>&1 || true
    emit "result=error"; emit "reason=health_check_failed_after_write"
    emit "rolled_back=true"
    emit "note=the routing key was REMOVED again, so the project is unrouted rather than broken."
    # The rollback undoes the ROUTING KEY and nothing else. Say so, rather than letting
    # `rolled_back=true` be read as "everything was undone".
    route_report_side_effects
    exit 3
  fi

  emit "result=routed"
  emit "settings_result=$ares"
  emit "backup=$(kv "$aout" backup)"
  emit "reset_hatch=$(kv "$aout" reset_hatch)"
  emit "replaced=$(kv "$aout" replaced)"
  route_report
}

# --- argument parsing. Unknown flags are refused rather than ignored: a silently dropped --scope
# --- would write the wrong file and report success.
if [ "${1:-}" = --route ]; then
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
      --plan)     R_PLAN=1 ;;
      --confirm)  R_CONFIRM=1 ;;
      --mode)     R_MODE="${2:?--mode needs a value}"; shift ;;
      --scope)    R_SCOPE="${2:?--scope needs a value}"; shift ;;
      --on-conflict) R_ONCONFLICT="${2:?--on-conflict needs a value}"; shift ;;
      --cache-strategy) R_STRATEGY="${2:?--cache-strategy needs a value}"; shift ;;
      --base-url) R_BASEURL="${2:?--base-url needs a value}"; shift ;;
      --health-url) R_HEALTHURL="${2:?--health-url needs a value}"; shift ;;
      --upstream) R_UPSTREAM="${2:?--upstream needs a value}"; shift ;;
      --no-health-check) R_NOHEALTH=1 ;;
      --i-understand-machine-wide) R_USERSCOPE=1 ;;
      --i-consent-to-traffic-interception) R_CONSENT=1 ;;
      *) emit "result=error"; emit "reason=unknown_flag"; emit "flag=$1"
         emit "note=refused rather than ignored: a dropped flag writes the wrong file and reports success"
         exit 2 ;;
    esac
    shift
  done
  case "$R_MODE" in local|attach) : ;; *)
    emit "result=error"; emit "reason=unknown_mode"; emit "mode=$R_MODE"; exit 2 ;;
  esac
  case "$R_ONCONFLICT" in ""|chain|replace|abort) : ;; *)
    emit "result=error"; emit "reason=unknown_on_conflict"; emit "value=$R_ONCONFLICT"; exit 2 ;;
  esac
  route_main
  exit 0
fi

# --- 1. already there, and is it the version we want? -------------------------------------
#
# This used to return `result=present` for ANY binary on PATH regardless of version, and read
# CONTEXT_GURU_VERSION only afterwards — so on the very change that creates a release channel
# there was no way to upgrade. It also reported the version by taking `head -1` of `--help`,
# which recorded "Usage of context-guru-proxy:" as the installed version.
installed_version() { # prints e.g. v0.1.2, or "" if the binary cannot say
  "$1" --version 2>/dev/null | awk '{print $2; exit}'
}

if command -v "$BIN" >/dev/null 2>&1; then
  have_path=$(command -v "$BIN")
  have=$(installed_version "$have_path")
  emit "path=${have_path}"
  emit "version=${have:-unknown}"
  # An explicit CONTEXT_GURU_VERSION means "I want that one" — honour it even when something is
  # already installed. `latest` resolves below and is compared there.
  if [ "$VERSION" != latest ] && [ "$VERSION" = "$have" ]; then
    emit "result=present"
    exit 0
  fi
  if [ "$VERSION" = latest ] && [ -n "$have" ] && [ "${CONTEXT_GURU_UPGRADE:-}" != 1 ]; then
    # Do not silently re-download on every install run; say what is there and how to move.
    emit "result=present"
    emit "note=set CONTEXT_GURU_UPGRADE=1 to check for and install a newer release"
    exit 0
  fi
  if [ -z "$have" ]; then
    emit "note=the installed binary does not support --version; it predates the release channel"
  fi
  emit "note=upgrading from ${have:-unknown}"
fi

case "$(uname -s)" in
  Darwin) OS=darwin ;;
  Linux)  OS=linux ;;
  *)      die "unsupported_os_$(uname -s): build from source, see docs/get-started/quickstart-proxy.md" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *)             die "unsupported_arch_$(uname -m)" ;;
esac
emit "platform=${OS}/${ARCH}"

command -v curl >/dev/null 2>&1 || die "no_curl"

# --- 2. release tarball -------------------------------------------------------------------
if [ "$VERSION" = latest ]; then
  # Resolve to a CONCRETE tag once, then use it for both the tarball and checksums.txt — the two
  # must come from the same release, and two independent /latest/download follows could straddle a
  # release published between them.
  #
  # The web redirect FIRST, not the API. `api.github.com` allows 60 requests/hour for unauthenticated
  # callers, counted PER IP — so the budget is shared by everyone behind the same address: a corporate
  # NAT, a CI fleet, a shared dev box. Exhausted, it answers 403, this resolution produced the empty
  # string, and the script then reported `no_release_found: no published release yet`. That message is
  # not just unhelpful, it is FALSE — it sent people off to build from source while a perfectly good
  # release sat published. Observed on a corporate IP: `{"limit":60,"remaining":0,"used":60}` with
  # v0.1.1 released and downloadable.
  #
  # The releases/latest web redirect carries no such budget and lands on /releases/tag/<tag>.
  VERSION=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
              "https://github.com/${REPO}/releases/latest" 2>/dev/null)
  VERSION="${VERSION##*/}"
  # With no releases at all the redirect lands on /releases, so guard against taking that as a tag.
  case "$VERSION" in
    ''|releases|latest) VERSION="" ;;
  esac
  if [ -z "$VERSION" ]; then
    # Only now the API, and report WHICH failure it was: "rate limited" and "no release" call for
    # completely different actions, and conflating them is what made the old message misleading.
    api=$(curl -sSL -w '\n%{http_code}' "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null)
    code=$(printf '%s' "$api" | tail -1)
    VERSION=$(printf '%s' "$api" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
    if [ -z "$VERSION" ]; then
      case "$code" in
        403|429) die "github_rate_limited: GitHub's API is rate limited for this IP (60/hour, shared with everything behind the same address), so the latest version could not be resolved. This says NOTHING about whether a release exists. Wait for the window to reset, or set CONTEXT_GURU_VERSION=vX.Y.Z to skip resolution entirely." ;;
        *)       die "no_release_found: no published release for ${REPO} (HTTP ${code}); build from source or set CONTEXT_GURU_VERSION" ;;
      esac
    fi
  fi
fi
NUM="${VERSION#v}"
TARBALL="context-guru_${NUM}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/${REPO}/releases/download/${VERSION}"
emit "version=${VERSION}"

# try_source_build is distribution option 3 from the header comment, which the first version of
# this script documented and never implemented — on a machine that had Go 1.26.4 on PATH.
#
# It is a FALLBACK, not a path anyone is steered to: it needs a toolchain, which is the gate this
# whole change exists to remove. But when there is no downloadable asset and a toolchain is right
# there, refusing to use it is worse than using it.
# report_path emits the two facts every successful install owes the caller, from BOTH install
# paths. The `go install` fallback used to return straight out of the script, so a user who landed
# on it got no `on_path` line at all — and ~/.local/bin frequently is not on PATH. The install
# looked clean, then a LATER session's hook said "the proxy binary is not on PATH", with nothing
# connecting the two. install/SKILL.md reads on_path to warn them, so the skill was silent too.
report_path() {
  emit "result=installed"
  emit "path=${DEST}/${BIN}"
  # Report — do not fix — a PATH that will not find it. Editing the user's shell rc is a bigger
  # intrusion than this script is entitled to, and the skill can tell them in context.
  case ":${PATH}:" in
    *":${DEST}:"*) emit "on_path=true" ;;
    *)             emit "on_path=false"
                   emit "note=add ${DEST} to your PATH, or the session hook will not find the proxy" ;;
  esac
}

try_source_build() {
  command -v go >/dev/null 2>&1 || return 1
  # "attempted", because this line is printed BEFORE the build runs: it appears even when the
  # build then fails, and `result=` is what says whether anything was installed.
  emit "fallback=go_install_attempted"
  # CGO off: the binary is pure Go, and requiring a C toolchain here would reintroduce the gate.
  # GOBIN does not need creating first: checked on Linux with Go 1.26.4 — `go install` creates a
  # missing GOBIN directory itself, so the tarball path's `mkdir -p` is not needed here.
  if CGO_ENABLED=0 GOBIN="$DEST" go install "github.com/${REPO}/cmd/context-guru-proxy@${VERSION}" 2>"$TMP/go.err"; then
    emit "built_from=source"
    report_path
    return 0
  fi
  emit "go_install_failed=$(tail -1 "$TMP/go.err" 2>/dev/null | tr -d '\n')"
  return 1
}

TMP=$(mktemp -d) || die "no_tmpdir"
trap 'rm -rf "$TMP"' EXIT

# The raw curl error used to reach stdout and break this script's "every fact is a key=value
# line" contract, which the calling skill parses. Keep curl quiet and report the failure as data.
if ! curl -fsSL -o "$TMP/$TARBALL" "$BASE/$TARBALL" 2>"$TMP/curl.err"; then
  emit "download_url=$BASE/$TARBALL"
  # A published tag with no assets reaches exactly here — the release exists, the artifact does
  # not — which is what a pre-release repository looks like before the first build is attached.
  if try_source_build; then
    exit 0
  fi
  die "download_failed: $BASE/$TARBALL (no asset for this platform, and no Go toolchain to build from source)"
fi

# Checksum. The download is unsigned, there is no signature anywhere yet, and this script strips
# macOS quarantine from the file below — so this is the ONLY integrity check in the path.
#
# It is therefore fail-CLOSED, in every branch. The first version of this was fail-open: a missing
# or unfetchable checksums.txt printed one advisory line and installed anyway, which meant an
# unverified binary landed on a PATH directory and ran — a binary that then handles all of the
# user's LLM traffic and holds their API key. The comment above it said "a failure here is fatal,
# never a warning" while the code did the opposite.
#
# CONTEXT_GURU_INSECURE=1 exists for the one legitimate case (a local build served from a file
# path with no checksums file) and says what it is in its name.
verify_checksum() {
  curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt" 2>/dev/null ||
    die "checksum_unavailable: could not fetch $BASE/checksums.txt, so the download cannot be verified. Set CONTEXT_GURU_INSECURE=1 to install anyway (not recommended)."
  want=$(awk -v f="$TARBALL" '$2 == f || $2 == "*"f {print $1}' "$TMP/checksums.txt" | head -1)
  [ -n "$want" ] ||
    die "checksum_absent: $TARBALL is not listed in checksums.txt, so the download cannot be verified. Set CONTEXT_GURU_INSECURE=1 to install anyway (not recommended)."
  if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$TMP/$TARBALL" | awk '{print $1}')
  else
    got=$(shasum -a 256 "$TMP/$TARBALL" | awk '{print $1}')
  fi
  [ "$want" = "$got" ] || die "checksum_mismatch: expected $want got $got"
  emit "checksum=verified"
}

if [ "${CONTEXT_GURU_INSECURE:-}" = 1 ]; then
  emit "checksum=SKIPPED_BY_CONTEXT_GURU_INSECURE"
else
  verify_checksum
fi

tar xzf "$TMP/$TARBALL" -C "$TMP" || die "untar_failed"

# FIND the binary rather than assuming where it sits.
#
# The release archive wraps its contents in a directory (goreleaser `wrap_in_directory: true`), so
# the binary is at `<archive-name>/context-guru-proxy` and not at the root. It is wrapped for a
# reason worth not undoing: a flat archive plus the documented `tar xzf` with no `-C` overwrites the
# README.md and LICENSE of whatever directory the user is standing in.
#
# Searching handles both layouts, so this script does not break the next time the packaging changes
# — and the failure it avoids is the worst-placed one there is: a stranger's first install, reporting
# `binary_not_in_tarball`, which reads as a broken release rather than a moved file.
found=$(find "$TMP" -type f -name "$BIN" 2>/dev/null | head -1)
[ -n "$found" ] || die "binary_not_in_tarball: no $BIN anywhere in $TARBALL"

mkdir -p "$DEST" || die "cannot_create_$DEST"
# Install to a temp name and RENAME into place, so the destination is never absent or partial.
#
# Not for the reason it was suggested: the review's premise was ETXTBSY on Linux when writing over
# a running binary, and that was tested on Linux and does NOT happen — coreutils `install` unlinks
# the destination first, so the upgrade succeeds. But that unlink is itself the window worth
# closing: between it and the new file appearing, a SessionStart hook firing in another project
# finds no binary and reports "not on PATH". rename(2) swaps the directory entry in one step, so
# there is no instant at which $DEST/$BIN does not exist.
install -m 755 "$found" "$DEST/$BIN.new" || die "install_failed_to_$DEST"
mv -f "$DEST/$BIN.new" "$DEST/$BIN" || die "install_failed_to_$DEST"

# macOS: without this, the first run dies with "cannot be verified" and the evaluator concludes
# the project is broken. Notarization would remove the need and requires a paid Apple account.
if [ "$OS" = darwin ] && command -v xattr >/dev/null 2>&1; then
  xattr -d com.apple.quarantine "$DEST/$BIN" 2>/dev/null || true
  emit "quarantine=cleared"
fi

report_path
