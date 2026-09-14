#!/bin/bash
# Run the three scenario arms in order, into one log.
#
# SEQUENTIAL ON PURPOSE: the arms share a box and a gateway, and one arm's traffic would perturb
# another's cache timing — which is the one thing every arm here is measuring.
#
# Arm B and arm C each sleep past the cache TTL several times, so the whole run is dominated by
# wall-clock idle rather than by tokens. Budget roughly an hour, and run it detached (tmux) — an
# ssh session dropping has killed a run at the 40-minute mark before.
#
# Requires CG_SCEN_UPSTREAM. See lib.sh for the rest.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
L="${CG_SCEN_ROOT:-$PWD/.cg-scen}/run.log"
mkdir -p "$(dirname "$L")"
: > "$L"
echo "=== SCENARIO RUN START $(date -u) ===" >> "$L"
echo "=== source: ${CG_SCEN_SRC:-$PWD} ===" >> "$L"
for s in a-firing-rate b-cold-events c-warm-only; do
  echo >> "$L"
  "$here/$s.sh" >> "$L" 2>&1
  echo "--- arm $s exited $? at $(date -u +%H:%M:%S)" >> "$L"
done
echo "=== SCENARIO RUN DONE $(date -u) ===" >> "$L"
echo "log: $L"
