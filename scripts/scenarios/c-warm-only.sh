#!/bin/bash
# SCENARIO C — WARM ONLY AFTER THE SUMMARY. The rate the review found wrong.
#
# The defect that inverted the panel's sign: the credit on a turn whose cache HIT was priced at the
# cache-WRITE rate, 12.5x too high, because the async stash key first reaches rep.CacheKeys on a
# credited replay turn and MarkUnique calls the whole removal "new content".
#
# So this arm fires once and then takes ONLY warm turns. Every credit it produces must land in
# read_credit_usd at the cache-READ rate, cold_credit_usd must be exactly zero, and the panel's
# figure must equal sum(saved_gross) x read rate — not the sum of the stored saved_usd, which is
# what it used to be.
set -u
. "$(dirname "$0")/lib.sh"

N=warmonly
PORT=4213
# THE GAP MUST EXCEED THE TTL PLUS THE CLOCK-SKEW MARGIN, not just the TTL.
#
# 310s looked right — past a 5-minute entry — and the gate declined every time. The provider HAD
# already dropped the entry (the turn billed a full 183,660-token rewrite with zero cache read), but
# components.CertainlyColdByClock requires ColdMargin (60s) PAST nominal expiry before it will make
# the positive claim that an entry is gone, mirroring apply.cacheIsCold over the same timestamps. At
# 310s idle the arithmetic says "-10s remaining", which is inside the allowance, so the gate refuses.
#
# That is the intended conservatism — forgoing an opportunity beats rewriting a prefix that may still
# be live — and it is worth knowing it costs real firing opportunities: a session that returns between
# 300s and 360s of idle finds the entry gone AND the gate shut.
GAP=380   # > TTL (300) + components.ColdMargin (60)

echo "=== SCENARIO C: warm turns only after the summary (the read rate) ==="
scen_build
scen_start  "$N" "$PORT" 0.5 pre_expiry_or_cold
scen_home   "$N" "$PORT"
scen_work   "$N"

scen_turn "$N" g01 fresh    "Read a.go and list every exported function with a one-line summary."
scen_turn "$N" g02 continue "Read b.go in full. Summarise how the economic gate skips a candidate."
scen_turn "$N" g03 continue "Read c.go in full. Explain how the conversation partition is chosen."
scen_turn "$N" g04 continue "Read d.go in full. Explain the episode span axis."
scen_turn "$N" g05 continue "Read e.go in full. List every place usage is attributed to a request."
scen_turn "$N" g06 continue "Read f.go in full. Explain how the boundary is computed."
scen_turn "$N" g07 continue "Read g.go in full. Explain baselineDeltaUSD and repeatRate."

scen_sleep "$GAP" "past the TTL so the gate fires"
scen_turn "$N" f01 continue "In one sentence, what is the most important invariant in a.go?"

# Warm from here on: every turn within seconds of the last.
for k in 01 02 03 04 05 06 07 08; do
  scen_turn "$N" "w$k" continue "In one sentence, name one thing h.go decides."
done

echo
echo "=== SCENARIO C: all rows ==="
scen_tail "$N" 60
scen_panel "$N" "$PORT"
# A tiny span too, so the same rows also produce a CLOSED episode: the settled total is the figure a
# reader trusts, and an arm that only ever reports an open one cannot check it.
scen_panel "$N" "$PORT" 0.002

echo
echo "=== SCENARIO C: the read rate, hand-derived ==="
SCEN_DB="$SCEN_ROOT/$N/dash.db" SCEN_PANEL="$SCEN_ROOT/$N/panel.json" python3 - <<'PY'
import os, sqlite3, json
c = sqlite3.connect("file:%s?mode=ro" % os.environ["SCEN_DB"], uri=True)
rows = list(c.execute("""
  SELECT r.id, r.cache_miss_reason, COALESCE(c.saved_gross,0), COALESCE(c.saved_usd,0),
         COALESCE(c.events,''), r.cg_llm_cost_usd
  FROM requests r LEFT JOIN request_components c
    ON c.request_id=r.id AND c.component='summarize'
  WHERE r.keepalive=0 ORDER BY r.ts"""))
t0 = next((i for i, r in enumerate(rows)
           if '"summary_started"' in r[4] or '"fresh_summary"' in r[4]), None)
if t0 is None:
    print("NO SUMMARY WAS COMMISSIONED — precondition not met."); raise SystemExit
after = rows[t0+1:]
hits  = [r for r in after if r[1] == 'hit']
colds = [r for r in after if r[1] == 'ttl_expiry']
print("turns after t0   %d  (hit=%d, ttl_expiry=%d)" % (len(after), len(hits), len(colds)))
print("sum(saved_gross) on hit turns   %d" % sum(r[2] for r in hits))
print("sum(saved_usd)   on hit turns   %.8f   <-- what the panel USED to report" % sum(r[3] for r in hits))
print()
pan = json.load(open(os.environ["SCEN_PANEL"]))
for g in pan.get("by_provenance", []):
    print("panel read_credit_usd  %.8f" % g.get("read_credit_usd", 0))
    print("panel cold_credit_usd  %.8f   (must be 0.0: no turn after t0 went cold)" % g.get("cold_credit_usd", 0))
    print("panel open_net_usd     %.8f" % g.get("open_net_usd", 0))
    print("panel summarizer_cost  %.8f   (must be > 0: the async call's cost lands one turn late)"
          % g.get("summarizer_cost_usd", 0))
print()
print("THE CHECK: panel read_credit_usd must equal sum(saved_gross) x the 5m cache-READ rate,")
print("and must NOT equal sum(saved_usd) above. If it equals the latter, the write-rate defect is back.")
PY
echo "=== SCENARIO C done $(date -u +%H:%M:%S) ==="
