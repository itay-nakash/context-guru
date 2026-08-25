package dash

import (
	"database/sql"
	"net/http"
)

// The manager-controlled keep-alive strategy tab's read side: one ledger, grouped by
// tenant, for one strategy — see proxy/keepalivestrategy.go for how a strategy is
// resolved and audited, and docs/superpowers/specs/2026-08-25-keepalive-strategies-design.md
// for the design.

// StrategyLedgerRow is one tenant's economics under a strategy.
type StrategyLedgerRow struct {
	TenantID string  `json:"tenant_id"`
	Pings    int64   `json:"pings"`
	PingUSD  float64 `json:"ping_usd"`
	SavedUSD float64 `json:"saved_usd"`
	NetUSD   float64 `json:"net_usd"`
}

// StrategyLedgerView is one strategy's whole answer: the totals, and the per-tenant
// breakdown behind them, costliest first.
type StrategyLedgerView struct {
	StrategyID string              `json:"strategy_id"`
	Pings      int64               `json:"pings"`
	PingUSD    float64             `json:"ping_usd"`
	SavedUSD   float64             `json:"saved_usd"`
	NetUSD     float64             `json:"net_usd"`
	Tenants    []StrategyLedgerRow `json:"tenants"`
}

// StrategyLedger computes one strategy's economics, built the same way KeepAliveSessions
// already is (dash/keepalive.go), filtered by keepalive_strategy_id instead of by
// Filter.Tenant.
//
// Pings and PingUSD are EXACT: every ping row this strategy caused carries its id (see
// proxy's record1). SavedUSD is the SAME ceiling the account-wide ledger reports
// (KeepAliveLedger) and carries the same caveat plus one more: it is the tenant's WHOLE
// keep-alive credit, not only the share this strategy's own pings produced. The credit
// lands on the real request a ping rescued, which carries no strategy id — only the ping
// itself is attributed (see the design doc's "Attribution for stats") — so on a tenant
// running more than one strategy, or a per-session override, alongside this one, this
// number is an upper bound on this strategy's own contribution, not an exact share of it.
func (d *DB) StrategyLedger(strategyID string) (*StrategyLedgerView, error) {
	out := &StrategyLedgerView{StrategyID: strategyID, Tenants: []StrategyLedgerRow{}}
	rows, err := d.sql.Query(`SELECT tenant_id, COUNT(*), COALESCE(SUM(cost_usd),0)
		FROM requests WHERE keepalive = 1 AND keepalive_strategy_id = ?
		GROUP BY tenant_id ORDER BY 3 DESC`, strategyID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var row StrategyLedgerRow
		if err := rows.Scan(&row.TenantID, &row.Pings, &row.PingUSD); err != nil {
			rows.Close()
			return nil, err
		}
		out.Tenants = append(out.Tenants, row)
		out.Pings += row.Pings
		out.PingUSD += row.PingUSD
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out.Tenants {
		row := &out.Tenants[i]
		var saved sql.NullFloat64
		if err := d.sql.QueryRow(`SELECT SUM(keepalive_saved_usd) FROM requests
			WHERE tenant_id = ? AND keepalive_saved_usd > 0`, row.TenantID).Scan(&saved); err != nil {
			return nil, err
		}
		row.SavedUSD = saved.Float64
		row.NetUSD = row.SavedUSD - row.PingUSD
		out.SavedUSD += row.SavedUSD
	}
	out.NetUSD = out.SavedUSD - out.PingUSD
	return out, nil
}

// keepAliveStrategyRoutes is this feature's one read route, appended to routes in
// api.go for the same reason every other feature's are: that table is what both
// scoping tests walk.
func (a *API) keepAliveStrategyRoutes() []route {
	return []route{
		{"GET /api/keepalive/strategies/{id}/ledger", scopeManager, a.keepAliveStrategyLedger},
	}
}

// keepAliveStrategyLedger serves one strategy's ledger. Not tenant-scoped by a.scope —
// like /api/config and /api/benchmarks, this is a server-wide view spanning every
// tenant a strategy touched, not one tenant's own data.
func (a *API) keepAliveStrategyLedger(w http.ResponseWriter, r *http.Request) {
	if !a.requireManager(w, r, "a strategy's ledger") {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpErr(w, http.StatusBadRequest, "name the strategy")
		return
	}
	led, err := a.rec.DB().StrategyLedger(id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, led)
}
