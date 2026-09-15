#!/bin/bash
# iteration 028: does iteration 024's reward result appear when the band is actually 64k?
#
# ONE SEED PER INVOCATION, AND THEN IT STOPS. There is no loop over seeds in this file, deliberately and
# at the operator's instruction: each seed is read out and the decision to continue is made by a person.
# A script that ran all five would spend the whole budget before the first number was looked at, which is
# how iteration 026 spent a full run to discover a zero.
#
#   ./run028.sh preflight    the four conditions from PREREGISTRATION.md section 2. No reward pass, ~$8.
#   ./run028.sh seed 1       baseline then arm A for seed 1, then STOP.
#   ./run028.sh readout      the cumulative mean difference and the futility check, over whatever exists.
#
# BASELINE FIRST WITHIN EACH SEED, so that if a seed is interrupted halfway what survives is the arm that
# costs nothing to repeat rather than the arm that carries the ask spend.
#
# ONE BINARY, BOTH CONFIGS, HASH CHECKED BEFORE EVERY PASS. Not once at launch: a rebuild landing on this
# path between seed 2 and seed 3 leaves some passes on code X and others on code Y with every counter
# still healthy, and the comparison is silently void. The expected hash must be filled in from the built
# binary BEFORE the first pass and not edited afterwards.
set -uo pipefail
H="$HOME/cg-loca"
BIN="${CG_I028_BIN:-$HOME/cg-bin/cg-i028-proxy}"
PORT="${CG_I028_PORT:-6872}"
# PINNED HERE, not left to the environment, so the preregistration is auditable in the repo rather than
# in someone's shell history. Built from the tree at commit a9b8586 with
# `CGO_ENABLED=1 go build -o ~/cg-bin/cg-i028-proxy ./cmd/context-guru-proxy` on 2026-09-15.
# DO NOT EDIT once a pass has completed: passes already run used this binary, and changing the pin to
# match a rebuild is the same as comparing two different programs while every counter still looks healthy.
EXPECT="${CG_I028_SHA:-b9a69b15e8644bb479bead7e537cf7bf}"
BAND=64
STEP="${1:-readout}"

die() { echo "REFUSING: $*" >&2; exit 1; }

check_binary() {
  [ -x "$BIN" ] || die "no binary at $BIN"
  [ -n "$EXPECT" ] || die "CG_I028_SHA is unset. Pin the binary hash before the first pass, or a rebuild
  mid-run will void the comparison without changing a single counter."
  local got; got=$(sha256sum "$BIN" | cut -c1-32)
  [ "$got" = "$EXPECT" ] || die "binary is $got, preregistered $EXPECT.
  Passes already completed ran a DIFFERENT binary. Do not compare them."
}

# CONDITION 1 of the pre-flight, applied to every pass and not only the probe. This is the check whose
# absence voided iterations 008-024: the window resolved to 1,000,000 and every threshold in the config
# was measured against the wrong number, silently.
verify_window() {
  local tag="$1" unres
  unres=$(grep -o '"model_info_unresolved":[0-9]*' "$H/st-i022-$tag.json" 2>/dev/null | grep -o '[0-9]*$')
  [ -n "$unres" ] || { echo "  !! $tag: no model_info_unresolved in the stats -- cannot verify the band"; return 1; }
  [ "$unres" = "0" ] || { echo "  !! $tag: model_info_unresolved=$unres -- thresholds used the wrong window. DISCARD."; return 1; }
  echo "  $tag: model_info_unresolved=0"
}

# CONDITION 4: the baseline must not act, and arm A must ask. Either failure means the arms are not what
# the configs claim, and a null result would be indistinguishable from two identical arms -- exactly the
# thing iteration 026 could not tell apart.
verify_arm() {
  local tag="$1" arm="$2"
  python3 - "$H/st-i022-$tag.json" "$arm" <<'PY'
import json,sys
d=json.load(open(sys.argv[1])); arm=sys.argv[2]
c=(d.get("components") or {}).get("extract_llm_sweep") or {}
e=c.get("events") or {}
acted=c.get("acted") or 0
asks=e.get("sweep_prefix_cache_read_ok",0)+e.get("sweep_adjudicated",0)
print("  %s: acted=%d offered=%d adjudicated=%d"%(arm,acted,e.get("sweep_offered",0),e.get("sweep_adjudicated",0)))
if arm=="baseline" and acted!=0:
    sys.exit("  !! BASELINE ACTED (%d). The arms are not what the configs claim; nothing is comparable."%acted)
if arm=="A" and e.get("sweep_adjudicated",0)<5:
    sys.exit("  !! ARM A produced %d verdicts (<5). This pass is INVALID, not informative. Re-run it;\n"
             "  !! do not pool it and do not read it as futility."%e.get("sweep_adjudicated",0))
PY
}

# RESUMABILITY CUTS BOTH WAYS. Skipping a tag whose stats exist is what lets an interrupted run continue
# without repeating a paid pass -- and it is also how a pass that must be REDONE gets silently skipped.
# Measured: the first pre-flight attempt died on a rotated gateway credential (HTTP 401 on every request),
# left a stats file behind, and the retry would have reported "SKIP" and moved on with a pass that
# adjudicated nothing. Move the artifact aside rather than deleting it -- `mv st-i022-<tag>.json{,.bad}` --
# so the reason it was rejected stays on disk next to the run it belongs to.
run_pass() {
  local tag="$1" cfg="$2" arm="$3" taskcfg="$4"
  if [ -f "$H/st-i022-$tag.json" ]; then
    echo "SKIP $tag (stats already on disk)."
    echo "  If that pass was INVALID -- wrong window, baseline acted, <5 verdicts, accuracy 0.000, or an"
    echo "  auth failure -- move it aside first or this run silently reuses it:"
    echo "    mv $H/st-i022-$tag.json $H/st-i022-$tag.json.bad"
    return 0
  fi
  echo "===== $tag  arm=$arm ====="; date -u
  check_binary
  bash "$H/stage022.sh" "$tag" "$BIN" "$H/$cfg" "$PORT" "$taskcfg" "$BAND" || die "$tag failed to run"
  verify_window "$tag" || die "$tag: window check failed"
  verify_arm "$tag" "$arm" || die "$tag: arm check failed"
  sleep 15
}

case "$STEP" in
preflight)
  # Arm A only, two tasks. Answers ONLY: is the band real, and does it fire. Neither needs a full pass,
  # and both invalidate everything after them.
  echo "PRE-FLIGHT: band real? fires at all? baseline inert? Reward is NOT measured here."
  run_pass "i028pf" "cfg-iter028-A.yaml" "A" "$H/task-configs/i028-preflight.json"
  echo
  echo "Pre-flight passed. Nothing about reward is known. Next: ./run028.sh seed 1"
  ;;
seed)
  S="${2:?usage: run028.sh seed N}"
  TC="$H/task-configs/i024-64k-s$S.json"
  [ -f "$TC" ] || die "no task config $TC -- seed $S must reuse iteration 024's own instances"
  run_pass "i028-s$S-base" "cfg-iter028-baseline.yaml" "baseline" "$TC"
  run_pass "i028-s$S-A"    "cfg-iter028-A.yaml"        "A"        "$TC"
  echo
  bash "$0" readout
  echo
  echo "=============================================================================="
  echo "SEED $S COMPLETE. STOPPING, BY DESIGN."
  echo "Do not run the next seed until a person has read the numbers above and said so."
  echo "=============================================================================="
  ;;
readout)
  python3 "$H/readout028.py"
  ;;
*) die "unknown step '$STEP' (preflight | seed N | readout)" ;;
esac
