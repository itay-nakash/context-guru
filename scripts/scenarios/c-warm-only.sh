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
# READ THE EPISODE, NOT THE GROUP: group credit fields accumulate over CLOSED episodes only, so on an
# open run every bucket reads 0.00000000 and looks broken when the figures are in the episode.
pan = json.load(open(os.environ["SCEN_PANEL"]))
eps = pan.get("episodes", [])
e = eps[0] if eps else {}
print("EPISODE state=%s turns=%s new_content=%s" % (
    e.get("state"), e.get("turns"), e.get("new_content_billed")))
print("  read_credit_usd  %.8f" % e.get("read_credit_usd", 0))
print("  cold_credit_usd  %.8f" % e.get("cold_credit_usd", 0))
print("  summarizer_cost  %.8f" % e.get("summarizer_cost_usd", 0))
print("  invalidation     %.8f" % e.get("invalidation_debit_usd", 0))
print("  net_usd          %.8f" % e.get("net_usd", 0))

# Scoped to the turns the panel counted, and stated as pass/fail rather than as prose a reader has to
# adjudicate — an arm whose verdict needs interpreting is an arm that will be read as passing.
turns = int((e.get("turns") or 0))
span = rows[t0:t0 + turns] if turns else []
sh = [r for r in span[1:] if r[1] == 'hit']
gross, usdsum = sum(r[2] for r in sh), sum(r[3] for r in sh)
cg = sum(r[5] for r in span)
READ = float(os.environ.get("CG_SCEN_READ_RATE", "1e-7"))
print()
print("HAND, over exactly the %d turns the panel counted:" % len(span))
print("  sum(saved_gross) on hit turns in span   %d" % gross)
print("  x the cache-READ rate                   %.8f   <-- read_credit must equal this" % (gross * READ))
print("  sum(saved_usd)   on hit turns in span   %.8f   <-- and must NOT equal this" % usdsum)
print("  sum(cg_llm_cost) over the span          %.8f" % cg)
print()
rc = e.get("read_credit_usd", 0)
def verdict(ok):
    return "PASS" if ok else "FAIL"
print("  read credit at the READ rate      %s" % verdict(abs(rc - gross * READ) < 1e-9))
print("  read credit != stored saved_usd   %s" % verdict(abs(rc - usdsum) >= 1e-9))
print("  cold credit is zero               %s" % verdict(e.get("cold_credit_usd", 0) == 0))
print("  summarizer cost is charged        %s" % verdict(e.get("summarizer_cost_usd", 0) > 0))
PY
echo "=== SCENARIO C done $(date -u +%H:%M:%S) ==="
