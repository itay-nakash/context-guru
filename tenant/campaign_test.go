package tenant

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// Create/List/Get/Archive round trip, including that the whole cell set persists and
// survives a reload, the same durability class as a keep-alive strategy.
func TestCampaignCRUD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tenant.db")
	r1, err := Open(path, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c, err := r1.CreateCampaign("mgr-1", Campaign{
		Name: "rollout-1", Source: CampaignSourceLive, Baseline: "fixed-5m",
		MinRequests: 5, Weekdays: []string{"Sunday", "Monday"},
	}, []CampaignCell{
		{TenantID: "t1", HourUTC: 9, Requests: 42, Arm: "keepalive-5m",
			PredictedUSD: 1.23, BaselineUSD: 4.56, Activatable: true, StrategyID: "strat-1"},
		{TenantID: "t1", HourUTC: 10, Requests: 3, Arm: "sticky-session-1h",
			InsufficientData: true, Activatable: false, SkipReason: "simulation-only arm"},
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if c.ID == "" || c.CreatedBy != "mgr-1" || c.Status != CampaignStatusActive {
		t.Errorf("created campaign looks wrong: %+v", c)
	}
	if c.ActivatedAt.IsZero() {
		t.Error("ActivatedAt was not stamped")
	}

	list, err := r1.ListCampaigns()
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	if len(list) != 1 || list[0].ID != c.ID {
		t.Fatalf("got %+v, want exactly the one campaign created", list)
	}

	got, err := r1.CampaignByID(c.ID)
	if err != nil {
		t.Fatalf("CampaignByID: %v", err)
	}
	if got.Name != "rollout-1" || len(got.Weekdays) != 2 || got.Weekdays[1] != "Monday" {
		t.Errorf("CampaignByID = %+v, want the created campaign", got)
	}

	cells, err := r1.CampaignCells(c.ID)
	if err != nil {
		t.Fatalf("CampaignCells: %v", err)
	}
	if len(cells) != 2 {
		t.Fatalf("got %d cells, want 2", len(cells))
	}
	if cells[0].HourUTC != 9 || cells[0].Arm != "keepalive-5m" || !cells[0].Activatable ||
		cells[0].StrategyID != "strat-1" {
		t.Errorf("cell 0 = %+v", cells[0])
	}
	if cells[1].HourUTC != 10 || cells[1].Activatable || cells[1].StrategyID != "" ||
		cells[1].SkipReason != "simulation-only arm" || !cells[1].InsufficientData {
		t.Errorf("cell 1 = %+v, want a non-activatable cell with its skip reason and no strategy id", cells[1])
	}

	if err := r1.ArchiveCampaign(c.ID); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	archived, err := r1.CampaignByID(c.ID)
	if err != nil {
		t.Fatalf("CampaignByID after archive: %v", err)
	}
	if archived.Status != CampaignStatusArchived {
		t.Errorf("Status after archive = %q, want %q", archived.Status, CampaignStatusArchived)
	}
	if err := r1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r2, err := Open(path, Options{})
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer r2.Close()
	reloadedCells, err := r2.CampaignCells(c.ID)
	if err != nil {
		t.Fatalf("CampaignCells after reload: %v", err)
	}
	if len(reloadedCells) != 2 {
		t.Errorf("got %d cells after reload, want 2", len(reloadedCells))
	}
}

func TestCampaignByIDOnUnknownID(t *testing.T) {
	r := open(t, Options{})
	if _, err := r.CampaignByID("nope"); err != ErrNoCampaign {
		t.Errorf("CampaignByID on an unknown id = %v, want ErrNoCampaign", err)
	}
	if err := r.ArchiveCampaign("nope"); err != ErrNoCampaign {
		t.Errorf("ArchiveCampaign on an unknown id = %v, want ErrNoCampaign", err)
	}
}

func TestCreateCampaignRequiresANameAndAtLeastOneCell(t *testing.T) {
	r := open(t, Options{})
	if _, err := r.CreateCampaign("mgr-1", Campaign{},
		[]CampaignCell{{TenantID: "t1", HourUTC: 9, Arm: "keepalive-5m"}}); err == nil {
		t.Error("a campaign with no name was accepted")
	}
	if _, err := r.CreateCampaign("mgr-1", Campaign{Name: "x"}, nil); err == nil {
		t.Error("a campaign with no cells was accepted")
	}
	list, err := r.ListCampaigns()
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("a failed create left %d campaign rows behind, want 0 (the transaction "+
			"must have rolled back)", len(list))
	}
}

// Two cells sharing (TenantID, HourUTC) collide on campaign_cells' own primary key,
// since every cell in one CreateCampaign call shares the same about-to-be-generated
// campaign_id. This must be refused up front with a clear error naming the duplicate,
// not left to surface later as a raw SQL constraint failure — and refused before the
// transaction ever starts, so nothing is left half-written.
func TestCreateCampaignRejectsDuplicateTenantHourCells(t *testing.T) {
	r := open(t, Options{})
	_, err := r.CreateCampaign("mgr-1", Campaign{Name: "dup"}, []CampaignCell{
		{TenantID: "t1", HourUTC: 9, Arm: "keepalive-5m", Activatable: true},
		{TenantID: "t1", HourUTC: 9, Arm: "keepalive-5m-once", Activatable: true},
	})
	if err == nil {
		t.Fatal("a campaign with two cells at the same (tenant, hour) was accepted")
	}
	if !strings.Contains(err.Error(), "t1") || !strings.Contains(err.Error(), "9") {
		t.Errorf("error %q does not name the offending tenant/hour", err)
	}
	list, lerr := r.ListCampaigns()
	if lerr != nil {
		t.Fatalf("ListCampaigns: %v", lerr)
	}
	if len(list) != 0 {
		t.Errorf("a rejected create left %d campaign rows behind, want 0", len(list))
	}
}

// A cell with no skip reason must store SQL NULL, the same as StrategyID already does
// for a non-activatable cell — not the literal empty string. Both columns are declared
// as bare nullable TEXT in the same migration; storing "" instead of NULL on one of them
// would make a direct `WHERE skip_reason IS NULL` query (the natural one, given the
// schema and the sibling column's real NULL semantics) match zero rows even though most
// cells in practice have no skip reason. Checked at the raw SQL level, since the Go API
// (CampaignCells' sql.NullString scan) normalizes NULL and "" back to the same "" either
// way and would not catch this on its own.
func TestCreateCampaignStoresAnUnsetSkipReasonAsSQLNullNotEmptyString(t *testing.T) {
	r := open(t, Options{})
	c, err := r.CreateCampaign("mgr-1", Campaign{Name: "null-check"}, []CampaignCell{
		{TenantID: "t1", HourUTC: 9, Arm: "fixed-5m", Activatable: true}, // baseline: no skip reason
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	var skipReason sql.NullString
	if err := r.db.QueryRow(`SELECT skip_reason FROM campaign_cells WHERE campaign_id = ?`,
		c.ID).Scan(&skipReason); err != nil {
		t.Fatalf("raw query: %v", err)
	}
	if skipReason.Valid {
		t.Errorf("skip_reason = %q (valid=%v), want SQL NULL for an unset skip reason",
			skipReason.String, skipReason.Valid)
	}
}
