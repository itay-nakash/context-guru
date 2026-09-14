#!/bin/bash
# SCENARIO D — A CLIENT THAT COMPACTS EARLY. The case the C denominator exists for.
#
# `Trigger.Fires` now measures the fill fraction against C (the conversation's own compaction point)
# rather than against the model window. The whole value of that change is a client that compacts
# EARLY: against the window such a client resets the transcript before billed input ever reaches
# `frac x window`, so the component never fires, silently.
#
# On stock Claude Code the two denominators are nearly identical (C = 0.996 x W on haiku), so no live
# run on stock settings can distinguish them. This arm tries to create the divergence by lowering the
# client's own auto-compact threshold.
#
# THE OUTCOME IS INFORMATIVE EITHER WAY, and that is why the arm is worth running:
#
#   - if the setting takes effect, we see where the client really compacts, and whether OUR gate fires
#     there. With C coming from a static per-model table it should NOT — the table cannot know a
#     tenant's configured threshold — which is the concrete argument for learning C per deployment
#     (#239) rather than shipping a table.
#   - if the setting does nothing, the early-compacting client is not reachable by configuration from
#     here, and the Go-level test is the honest limit of what can be shown. Say so rather than
#     implying a live demonstration exists.
set -u
. "$(dirname "$0")/lib.sh"

N=earlycompact
PORT=4214

echo "=== SCENARIO D: a client that compacts early ==="
scen_build
# Shipped trigger fraction, because the question is whether the SHIPPED gate fires against a client
# that caps its own context.
scen_start  "$N" "$PORT" 0.9 pre_expiry_or_cold
scen_home   "$N" "$PORT"
scen_work   "$N"

# Lower the client's own auto-compact threshold, if it honours the key. Written into the SCRATCH
# config only — this must not touch the real one.
SCEN_HOME="$SCEN_ROOT/$N/claude-home" python3 - <<'PY'
import json, os
p = os.environ["SCEN_HOME"] + "/settings.json"
d = json.load(open(p))
# Both spellings, since which one (if either) is honoured is exactly what this arm is testing.
d["autoCompactThreshold"] = 0.5
d.setdefault("env", {})["MAX_AUTO_COMPACT_THRESHOLD"] = "0.5"
json.dump(d, open(p, "w"), indent=2)
print("  set autoCompactThreshold=0.5 in the scratch config only")
PY

scen_turn "$N" g01 fresh    "Read a.go and list every exported function with a one-line summary."
for i in 02 03 04 05 06 07 08 09 10 11 12; do
  case $i in
    02) P="Read b.go in full. Summarise how the economic gate skips a candidate.";;
    03) P="Read c.go in full. Explain how the conversation partition is chosen.";;
    04) P="Read d.go in full. Explain the episode span axis.";;
    05) P="Read e.go in full. List every place usage is attributed to a request.";;
    06) P="Read f.go in full. Explain how the boundary is computed.";;
    07) P="Read g.go in full. Explain baselineDeltaUSD and repeatRate.";;
    08) P="Read h.go in full. Explain every conjunct of Fires and CacheAllows.";;
    09) P="Compare a.go and d.go: which facts does each own?";;
    10) P="Re-read c.go and g.go. List every hardcoded cache TTL.";;
    11) P="Re-read b.go and e.go. List every counter and where it is exported.";;
    12) P="Summarise everything you have learned in fifteen bullets.";;
  esac
  scen_turn "$N" "t$i" continue "$P"
done

echo
echo "=== SCENARIO D: all rows ==="
scen_tail "$N" 60

echo
echo "=== SCENARIO D: where did the client compact, and did we fire? ==="
SCEN_DB="$SCEN_ROOT/$N/dash.db" CG_SCEN_WINDOW="${CG_SCEN_WINDOW:-200000}" python3 - <<'PY'
import os, sqlite3, json
W = int(os.environ["CG_SCEN_WINDOW"])
c = sqlite3.connect("file:%s?mode=ro" % os.environ["SCEN_DB"], uri=True)
rows = list(c.execute("""
  SELECT r.id, r.fresh_input+r.cache_read+r.cache_write AS billed, r.tokens_before, r.bypassed,
         r.max_tokens, r.agent, COALESCE(cp.events,''), COALESCE(cp.gates,'')
  FROM requests r LEFT JOIN request_components cp
    ON cp.request_id=r.id AND cp.component='summarize'
  WHERE r.keepalive=0 ORDER BY r.ts"""))
if not rows:
    print("NO ROWS"); raise SystemExit

peak = max(r[1] for r in rows)
print("turns              %d" % len(rows))
print("agent              %s" % sorted({r[5] for r in rows}))
print("max_tokens         %s" % sorted({r[4] for r in rows}))
print("peak billed        %d  = %.3f of the window" % (peak, peak / W))
print("the SHIPPED gate wants 0.9 x C; C from the table for haiku is 0.996 x %d = %d, so %d billed"
      % (W, int(0.996 * W), int(0.9 * 0.996 * W)))

# WHERE THE CLIENT COMPACTED: the flagged request's billed input is C, by definition.
flagged = [r for r in rows if r[3]]
drops = [(rows[i-1][0], rows[i-1][1]) for i in range(1, len(rows))
         if rows[i][2] and rows[i-1][2] and rows[i][2] < rows[i-1][2] * 0.7]
print()
print("MARKER 1 (agent-compaction detector): %s"
      % ([(r[0], r[1]) for r in flagged] or "none"))
print("MARKER 2 (tokens_before drop):        %s" % (drops or "none"))
obs = sorted({r[1] for r in flagged} | {b for _, b in drops})
if obs:
    print("OBSERVED C (billed): %s  => %s of the window"
          % (obs, ["%.3f" % (x / W) for x in obs]))
    print("  the table says 0.996. If the observed value is meaningfully lower, the SHIPPED table is")
    print("  wrong for this deployment and the gate is measuring against the wrong C — which is the")
    print("  argument for learning it per deployment (#239) rather than shipping a table.")
else:
    print("THE CLIENT DID NOT COMPACT in this run.")
    print("  Either the threshold setting is not honoured, or the session did not grow far enough.")
    print("  Peak fill above says which.")

fired = [r for r in rows if '"summary_started"' in r[6] or '"fresh_summary"' in r[6]]
print()
print("TURNS THAT FIRED   %d of %d" % (len(fired), len(rows)))
g = {}
for r in rows:
    if not r[7]:
        continue
    try:
        for k, n in json.loads(r[7]).items():
            g[k] = g.get(k, 0) + n
    except Exception:
        pass
print("gates (summed)     %s" % g)
PY
echo "=== SCENARIO D done $(date -u +%H:%M:%S) ==="
