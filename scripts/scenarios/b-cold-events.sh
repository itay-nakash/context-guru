#!/bin/bash
# SCENARIO B — THREE COLD EVENTS INSIDE THE SPAN. The headline bucket, accumulating.
#
# The claim the panel's ColdCreditUSD bucket exists to measure: a cache expiry that happens AFTER a
# summary re-creates the COMPACTED prefix instead of the full one, and the difference is a prevented
# rewrite. My acceptance run observed exactly one such event. One event cannot distinguish "the
# credit is computed" from "the credit ACCUMULATES per event", and it is the accumulation that says
# the amortisation model is right.
#
# So: fire once, then force the cache cold three separate times inside the same 10% span, and check
# the bucket is three prevented rewrites rather than one.
#
# WHY THREE COLDS FIT INSIDE ONE SPAN, which is itself a property worth demonstrating: the span axis
# is cumulative NEW content, and cache_write on a MISS is re-creation rather than new content, so a
# cold turn advances the span by only its few tokens of fresh input. If the axis had been cumulative
# spend (the version this PR fixed), the first cold turn would have closed the span on its own.
#
# min_request_frac is 0.5, NOT the shipped 0.9, and Scenario A is the arm that measures why.
set -u
. "$(dirname "$0")/lib.sh"

N=cold3
PORT=4212
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

echo "=== SCENARIO B: three cold events inside one span ==="
scen_build
scen_start  "$N" "$PORT" 0.5 pre_expiry_or_cold
scen_home   "$N" "$PORT"
scen_work   "$N"

# --- Grow the transcript past 0.5 of haiku's 200k window.
scen_turn "$N" g01 fresh    "Read a.go and list every exported function with a one-line summary."
scen_turn "$N" g02 continue "Read b.go in full. Summarise how the economic gate decides to skip a candidate."
scen_turn "$N" g03 continue "Read c.go in full. Explain how the conversation partition is chosen."
scen_turn "$N" g04 continue "Read d.go in full. Explain the episode span axis."
scen_turn "$N" g05 continue "Read e.go in full. List every place usage is attributed to a request."
scen_turn "$N" g06 continue "Read f.go in full. Explain how the boundary is computed."
scen_turn "$N" g07 continue "Read g.go in full. Explain baselineDeltaUSD and repeatRate."

echo "--- fill before forcing the gate:"
scen_tail "$N" 3

# --- FIRE: one gap past the TTL, with the transcript full. This turn commissions the summary.
scen_sleep "$GAP" "past the TTL so the gate sees a cold cache; this turn fires"
scen_turn "$N" f01 continue "In one sentence, what is the single most important invariant in a.go?"

# --- The splice lands on the next turn (the summary is produced off the hot path).
scen_turn "$N" s01 continue "In one sentence, name one risk in d.go."
echo "--- after the summary landed (billed should have COLLAPSED):"
scen_tail "$N" 4

# --- THREE COLD EVENTS, each inside the span. Each must re-create the COMPACTED prefix.
for k in 1 2 3; do
  scen_sleep "$GAP" "cold event $k of 3, inside the span"
  scen_turn "$N" "c0$k" continue "In one sentence, name one thing c.go owns."
done

echo
echo "=== SCENARIO B: all rows ==="
scen_tail "$N" 60
scen_panel "$N" "$PORT"

echo
echo "=== SCENARIO B: hand-derived against the panel ==="
SCEN_DB="$SCEN_ROOT/$N/dash.db" SCEN_PANEL="$SCEN_ROOT/$N/panel.json" python3 - <<'PY'
import os, sqlite3, json
c = sqlite3.connect("file:%s?mode=ro" % os.environ["SCEN_DB"], uri=True)
rows = list(c.execute("""
  SELECT r.id, r.fresh_input+r.cache_read+r.cache_write, r.cache_read, r.cache_write,
         r.cache_write_1h, r.cache_miss_reason, COALESCE(c.saved_gross,0),
         COALESCE(c.saved_usd,0), COALESCE(c.events,''), r.cg_llm_cost_usd, r.model
  FROM requests r LEFT JOIN request_components c
    ON c.request_id=r.id AND c.component='summarize'
  WHERE r.keepalive=0 ORDER BY r.ts"""))

# t0 = the turn that commissioned the summary.
t0 = next((i for i, r in enumerate(rows)
           if '"summary_started"' in r[8] or '"fresh_summary"' in r[8]), None)
if t0 is None:
    print("NO SUMMARY WAS COMMISSIONED — the scenario did not reach its own precondition.")
    raise SystemExit

print("t0 is row #%d (billed=%d write=%d %s)" % (rows[t0][0], rows[t0][1], rows[t0][3], rows[t0][5]))
after = rows[t0+1:]
colds = [r for r in after if r[5] == 'ttl_expiry']
hits  = [r for r in after if r[5] == 'hit']
print("turns after t0           %d   (ttl_expiry=%d, hit=%d)" % (len(after), len(colds), len(hits)))
print()
print("THE COLD EVENTS INSIDE THE SPAN")
for r in colds:
    print("  #%-4d wrote %-7d tokens of prefix, removed %-7d (our count)" % (r[0], r[3], r[6]))

# The counterfactual: what an UNCOMPACTED prefix would have written on each cold turn. The best
# evidence on hand is the last pre-summary full prefix, which t0 itself re-created.
full = rows[t0][3] or max((r[3] for r in rows[:t0+1]), default=0)
print()
print("counterfactual full prefix   %d tokens (t0's own re-creation)" % full)
if colds:
    comp = sum(r[3] for r in colds) / len(colds)
    print("actual compacted prefix      %.0f tokens (mean over the %d cold turns)" % (comp, len(colds)))
    print("prevented per cold event     %.0f tokens" % (full - comp))
    print("prevented over %d events      %.0f tokens" % (len(colds), len(colds)*(full-comp)))

# Rates, straight off the row's own model, as the panel prices them.
rate = list(c.execute("SELECT DISTINCT model FROM requests LIMIT 1"))
print()
print("PANEL vs HAND")
pan = json.load(open(os.environ["SCEN_PANEL"]))
for g in pan.get("by_provenance", []):
    print("  provenance=%s closed=%s open=%s" % (g.get("provenance"), g.get("closed"), g.get("open")))
    print("    cold_credit_usd   %.8f" % g.get("cold_credit_usd", 0))
    print("    read_credit_usd   %.8f" % g.get("read_credit_usd", 0))
    print("    invalidation      %.8f" % g.get("invalidation_debit_usd", 0))
    print("    summarizer_cost   %.8f" % g.get("summarizer_cost_usd", 0))
    print("    net_usd           %.8f" % g.get("net_usd", 0))
    print("    open_net_usd      %.8f" % g.get("open_net_usd", 0))
for e in pan.get("episodes", [])[:3]:
    print("  episode state=%s turns=%s new_content=%s cold=%.8f read=%.8f net=%.8f" % (
        e.get("state"), e.get("turns"), e.get("new_content_billed"),
        e.get("cold_credit_usd", 0), e.get("read_credit_usd", 0), e.get("net_usd", 0)))
print()
print("  hand: sum(saved_gross) on ttl_expiry turns after t0 = %d" % sum(r[6] for r in colds))
print("  hand: sum(saved_gross) on hit turns after t0        = %d" % sum(r[6] for r in hits))
print("  (multiply the first by the 5m cache-WRITE rate and the second by the cache-READ rate;")
print("   the panel's two buckets must equal those two products)")
PY
echo "=== SCENARIO B done $(date -u +%H:%M:%S) ==="
