package tenant

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Strategy campaigns: a manager-created bundle that turns a batch of KV-cache suggest
// cells into real keep-alive strategies in one shot, and freezes what each cell
// predicted so it can be checked against reality later — see proxy/campaign.go for how
// a suggest cell becomes a strategy (the enforceable-arm mapping, the model-honoring
// gate for the 1h tier, the window-tiling coalescer) and dash/campaignsavings.go for how
// the REAL half of that comparison is read, live, never stored here.
//
// Persisted in THIS registry, the same durability class as a keep-alive strategy
// (keepalivestrategy.go) — a campaign is a standing record of what was created and why,
// not a per-session grant. Only the PREDICTED side is ever written; the real side is
// computed on read from dash's request-metrics database, following this codebase's own
// convention against storing anything recomputable (see dash/cachehistory.go).

const (
	CampaignSourceLive   = "suggest-live"
	CampaignSourceUpload = "upload"

	CampaignStatusActive   = "active"
	CampaignStatusArchived = "archived"
)

// Campaign is one bundle: the suggest run it came from (frozen parameters, so a reader
// of a cell's predicted numbers knows what population they were backtested against),
// when it went live, and whether it is still in force.
type Campaign struct {
	ID          string
	Name        string
	Source      string // CampaignSourceLive | CampaignSourceUpload
	Baseline    string
	MinRequests int
	Weekdays    []string
	Status      string // CampaignStatusActive | CampaignStatusArchived
	CreatedBy   string
	CreatedAt   time.Time
	// ActivatedAt is the floor every real-saving read is bounded by (see
	// dash.DB.CampaignRealSavings) — never backfilled, for the same reason a keep-alive
	// strategy's own pings are not: there is no way to know what a strategy that did
	// not exist yet would have done with older traffic.
	ActivatedAt time.Time
}

// CampaignCell is one (tenant, hour-of-day) suggest cell, frozen at campaign-creation
// time. This IS the "historical/predicted saving" half of the drill-down; there is no
// corresponding "real" field on this type on purpose — see the file doc comment.
type CampaignCell struct {
	CampaignID string
	TenantID   string
	HourUTC    int
	// Requests is the suggest cell's own Requests: how many Sunday-Thursday requests in
	// this hour the backtest that produced PredictedUSD was actually computed from.
	Requests int64
	// Arm is the suggest cell's best_strategy, verbatim.
	Arm              string
	PredictedUSD     float64
	BaselineUSD      float64
	InsufficientData bool
	// Activatable is false for a simulation-only arm, or a 1h-tier arm on a tenant
	// whose model does not honor it — see proxy/campaign.go's arm-mapping table. A
	// non-activatable cell is still recorded, with SkipReason saying why, never hidden.
	Activatable bool
	SkipReason  string
	// StrategyID is which keepalive_strategies row now serves this cell, "" when
	// Activatable is false. Multiple cells commonly share one StrategyID — see the
	// coalescing rule in proxy/campaign.go.
	StrategyID string
}

// ErrNoCampaign names no strategy campaign.
var ErrNoCampaign = errors.New("tenant: no such strategy campaign")

// CreateCampaign inserts a campaign and every one of its cells in one transaction: a
// campaign with no cells, or cells with no campaign, is not a state this registry will
// produce. The caller (proxy/campaign.go) has already resolved every cell's arm,
// created the underlying keepalive_strategies rows, and validated the suggest payload
// this came from — this method only persists the result.
func (r *Registry) CreateCampaign(actorID string, c Campaign, cells []CampaignCell) (Campaign, error) {
	if c.Name == "" {
		return Campaign{}, fmt.Errorf("tenant: a campaign needs a name")
	}
	if len(cells) == 0 {
		return Campaign{}, fmt.Errorf("tenant: a campaign needs at least one cell")
	}
	weekdaysJSON, err := json.Marshal(c.Weekdays)
	if err != nil {
		return Campaign{}, err
	}
	now := time.Now()
	c.ID = newID()
	c.CreatedBy = actorID
	c.CreatedAt = now
	if c.ActivatedAt.IsZero() {
		c.ActivatedAt = now
	}
	if c.Status == "" {
		c.Status = CampaignStatusActive
	}

	tx, err := r.db.Begin()
	if err != nil {
		return Campaign{}, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO strategy_campaigns
	  (id,name,source,baseline,min_requests,weekdays_json,status,created_by,created_at,
	   activated_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.Source, c.Baseline, c.MinRequests, string(weekdaysJSON), c.Status,
		c.CreatedBy, c.CreatedAt.UnixMilli(), c.ActivatedAt.UnixMilli()); err != nil {
		return Campaign{}, err
	}
	for _, cell := range cells {
		cell.CampaignID = c.ID
		if _, err := tx.Exec(`INSERT INTO campaign_cells
		  (campaign_id,tenant_id,hour_utc,requests,arm,predicted_usd,baseline_usd,
		   insufficient_data,activatable,skip_reason,strategy_id)
		  VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			cell.CampaignID, cell.TenantID, cell.HourUTC, cell.Requests, cell.Arm,
			cell.PredictedUSD, cell.BaselineUSD, boolInt(cell.InsufficientData),
			boolInt(cell.Activatable), cell.SkipReason, nullableString(cell.StrategyID)); err != nil {
			return Campaign{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Campaign{}, err
	}
	return c, nil
}

// ListCampaigns returns every campaign, newest first — no cells; call CampaignCells for
// one campaign's own, the same list/detail split ListStrategies/StrategyByID uses.
func (r *Registry) ListCampaigns() ([]Campaign, error) {
	rows, err := r.db.Query(`SELECT ` + campaignCols + ` FROM strategy_campaigns ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CampaignByID reads one campaign.
func (r *Registry) CampaignByID(id string) (Campaign, error) {
	c, err := scanCampaign(r.db.QueryRow(
		`SELECT `+campaignCols+` FROM strategy_campaigns WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Campaign{}, ErrNoCampaign
	}
	return c, err
}

// ArchiveCampaign marks a campaign archived. It does not touch the strategies it
// created — those stop being managed as a group, but they keep matching live traffic
// until someone deletes or deactivates them individually through the strategy routes,
// exactly as DeleteStrategy does not retroactively un-ping anything.
func (r *Registry) ArchiveCampaign(id string) error {
	res, err := r.db.Exec(`UPDATE strategy_campaigns SET status = ? WHERE id = ?`,
		CampaignStatusArchived, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoCampaign
	}
	return nil
}

// CampaignCells returns every frozen cell for one campaign, ordered by tenant then
// hour — the same order the suggest payload it came from already used.
func (r *Registry) CampaignCells(campaignID string) ([]CampaignCell, error) {
	rows, err := r.db.Query(`SELECT campaign_id,tenant_id,hour_utc,requests,arm,predicted_usd,
	  baseline_usd,insufficient_data,activatable,skip_reason,strategy_id
	  FROM campaign_cells WHERE campaign_id = ? ORDER BY tenant_id, hour_utc`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CampaignCell{}
	for rows.Next() {
		var cell CampaignCell
		var insufficientData, activatable int
		var skipReason, strategyID sql.NullString
		if err := rows.Scan(&cell.CampaignID, &cell.TenantID, &cell.HourUTC, &cell.Requests,
			&cell.Arm, &cell.PredictedUSD, &cell.BaselineUSD, &insufficientData, &activatable,
			&skipReason, &strategyID); err != nil {
			return nil, err
		}
		cell.InsufficientData = insufficientData != 0
		cell.Activatable = activatable != 0
		cell.SkipReason = skipReason.String
		cell.StrategyID = strategyID.String
		out = append(out, cell)
	}
	return out, rows.Err()
}

const campaignCols = `id,name,source,baseline,min_requests,weekdays_json,status,created_by,
	created_at,activated_at`

func scanCampaign(sc scanner) (Campaign, error) {
	var out Campaign
	var weekdaysJSON string
	var createdAt, activatedAt int64
	if err := sc.Scan(&out.ID, &out.Name, &out.Source, &out.Baseline, &out.MinRequests,
		&weekdaysJSON, &out.Status, &out.CreatedBy, &createdAt, &activatedAt); err != nil {
		return Campaign{}, err
	}
	if weekdaysJSON != "" {
		if err := json.Unmarshal([]byte(weekdaysJSON), &out.Weekdays); err != nil {
			return Campaign{}, err
		}
	}
	out.CreatedAt, out.ActivatedAt = msTime(createdAt), msTime(activatedAt)
	return out, nil
}

// nullableString is "" written as SQL NULL rather than the empty string, matching how
// tenant.Strategy's own optional references (e.g. predictor_id) are told apart from a
// pre-feature row — here, a non-activatable cell's absent strategy id.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
