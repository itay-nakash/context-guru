package dash

import (
	"sort"
	"strings"
)

// The real-saving half of a strategy campaign's drill-down — see
// proxy/campaign.go for how a campaign turns suggest cells into strategies and
// dash/keepalivestrategy.go's StrategyLedger, whose two-disjoint-query shape this
// generalizes: a ping's cost carries its strategy id, but the real request it later
// rescues does not (see the design doc's "Attribution for stats") — so cost and saving
// can never come from one query, and every number here is a ceiling on this campaign's
// own contribution, not an exact share of it, exactly as StrategyLedgerView already is.
//
// Unlike StrategyLedger, every read here is bounded by `since` (a campaign's own
// activated_at): comparing a strategy's predicted saving against traffic that predates
// the campaign would credit it for a period it never ran in, which the design doc's
// "Out of scope" explicitly refuses to do for pings and which this refuses too, for the
// same reason.

// CampaignSavingCell is one (tenant, hour-of-day) cell's real economics since a
// campaign went live.
type CampaignSavingCell struct {
	TenantID string `json:"tenant_id"`
	HourUTC  int    `json:"hour_utc"`
	// Requests is real (non-ping) traffic in this cell since `since` — the denominator
	// for a $-per-1k-requests normalization, deliberately NOT the ping count, since a
	// ping is housekeeping this service issued, not traffic the tenant sent.
	Requests int64 `json:"requests"`
	// ActiveDays is how many distinct calendar days (UTC) this cell saw any real
	// request since `since` — the denominator for a $-per-active-day normalization.
	ActiveDays int64 `json:"active_days"`
	// Pings and PingUSD are EXACT, the same guarantee StrategyLedgerRow makes: every
	// ping row one of this campaign's own strategies caused carries that strategy's id.
	Pings   int64   `json:"pings"`
	PingUSD float64 `json:"ping_usd"`
	// SavedUSD is a CEILING — see the file doc comment and StrategyLedgerView's own.
	SavedUSD float64 `json:"saved_usd"`
	NetUSD   float64 `json:"net_usd"`
}

// CampaignRealSavings computes real, time-bounded economics per (tenant, hour-of-day)
// cell for a set of strategies (this campaign's own, so the cost half is exact) and the
// tenants they target (so the saving half's ceiling is at least scoped to the right
// accounts). since is a campaign's own activated_at, in epoch milliseconds — never 0
// meaning "no bound", because that would silently fold in pre-campaign history.
//
// Returns one cell per (tenant, hour) that had ANY ping or any real credited request
// since `since` — a cell absent from the result had neither, not a cell this query
// forgot to report.
func (d *DB) CampaignRealSavings(strategyIDs, tenantIDs []string, since int64) ([]CampaignSavingCell, error) {
	if len(strategyIDs) == 0 && len(tenantIDs) == 0 {
		return []CampaignSavingCell{}, nil
	}
	type cellKey struct {
		tenantID string
		hour     int
	}
	cells := map[cellKey]*CampaignSavingCell{}
	get := func(tenantID string, hour int) *CampaignSavingCell {
		key := cellKey{tenantID, hour}
		c := cells[key]
		if c == nil {
			c = &CampaignSavingCell{TenantID: tenantID, HourUTC: hour}
			cells[key] = c
		}
		return c
	}

	// Cost half: every ping row one of this campaign's strategies caused, grouped by
	// the tenant it pinged and the UTC hour it landed in. %H is a Go format verb as
	// much as a strftime one — this string is a query parameter, never passed through
	// fmt.Sprintf (see dash/kvcache.go's own warning about exactly this trap).
	//
	// Gated on strategyIDs alone (not tenantIDs too): this half doesn't touch
	// tenant_id at all, so a caller with strategies but no tenant list (or vice versa
	// for the saving half below) must still get its own half's real answer, not an
	// empty result — an empty answer here must mean "no matching rows," never "the
	// caller happened to pass an empty other list."
	if len(strategyIDs) > 0 {
		costArgs := make([]any, 0, len(strategyIDs)+1)
		for _, id := range strategyIDs {
			costArgs = append(costArgs, id)
		}
		costArgs = append(costArgs, since)
		costRows, err := d.sql.Query(`SELECT tenant_id,
				CAST(strftime('%H', ts/1000, 'unixepoch') AS INTEGER) h,
				COUNT(*), COALESCE(SUM(cost_usd),0)
			FROM requests WHERE keepalive = 1 AND keepalive_strategy_id IN (`+
			placeholders(len(strategyIDs))+`) AND ts >= ?
			GROUP BY tenant_id, h`, costArgs...)
		if err != nil {
			return nil, err
		}
		for costRows.Next() {
			var tenantID string
			var hour int
			var pings int64
			var pingUSD float64
			if err := costRows.Scan(&tenantID, &hour, &pings, &pingUSD); err != nil {
				costRows.Close()
				return nil, err
			}
			c := get(tenantID, hour)
			c.Pings, c.PingUSD = pings, pingUSD
		}
		costRows.Close()
		if err := costRows.Err(); err != nil {
			return nil, err
		}
	}

	// Saving half: every REAL (non-ping) request for one of this campaign's tenants,
	// grouped the same way — total volume and active-day count for normalization, plus
	// the credited saving. Not filtered by keepalive_strategy_id: the credited row
	// carries none (only the ping does), so this is the tenant's whole credit in this
	// cell, the same ceiling StrategyLedger already declares.
	if len(tenantIDs) > 0 {
		savingArgs := make([]any, 0, len(tenantIDs)+1)
		for _, id := range tenantIDs {
			savingArgs = append(savingArgs, id)
		}
		savingArgs = append(savingArgs, since)
		savingRows, err := d.sql.Query(`SELECT tenant_id,
				CAST(strftime('%H', ts/1000, 'unixepoch') AS INTEGER) h,
				COUNT(*), COUNT(DISTINCT ts/86400000),
				COALESCE(SUM(CASE WHEN keepalive_saved_usd > 0 THEN keepalive_saved_usd ELSE 0 END),0)
			FROM requests WHERE keepalive = 0 AND tenant_id IN (`+
			placeholders(len(tenantIDs))+`) AND ts >= ?
			GROUP BY tenant_id, h`, savingArgs...)
		if err != nil {
			return nil, err
		}
		for savingRows.Next() {
			var tenantID string
			var hour int
			var requests, activeDays int64
			var savedUSD float64
			if err := savingRows.Scan(&tenantID, &hour, &requests, &activeDays, &savedUSD); err != nil {
				savingRows.Close()
				return nil, err
			}
			c := get(tenantID, hour)
			c.Requests, c.ActiveDays, c.SavedUSD = requests, activeDays, savedUSD
		}
		savingRows.Close()
		if err := savingRows.Err(); err != nil {
			return nil, err
		}
	}

	out := make([]CampaignSavingCell, 0, len(cells))
	for _, c := range cells {
		c.NetUSD = c.SavedUSD - c.PingUSD
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].HourUTC < out[j].HourUTC
	})
	return out, nil
}

// placeholders builds "?,?,...", n times — SQLite has no native array bind, so an IN
// clause over a caller-supplied id list needs one placeholder per id.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
