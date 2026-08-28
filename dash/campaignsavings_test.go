package dash

import "testing"

// hourUTC returns an epoch-ms timestamp that lands in the given UTC hour on an
// arbitrary but fixed day, so cells for different hours never collide.
func hourUTC(day, hour int) int64 {
	return int64(day)*86_400_000 + int64(hour)*3_600_000
}

// CampaignRealSavings groups the cost half (ping rows carrying one of this campaign's
// strategy ids) and the saving half (real, non-ping rows for one of this campaign's
// tenants) by (tenant, hour-of-day), the same way StrategyLedger's two disjoint queries
// do, generalized to a grid instead of one strategy's totals.
func TestCampaignRealSavingsGroupsByTenantAndHour(t *testing.T) {
	db := openTestDB(t)

	t1ping9 := kaPing(hourUTC(1, 9), "s1", 0.02, 40_000, 0)
	t1ping9.TenantID, t1ping9.KeepAliveStrategyID = "t1", "camp-strat-1"
	t1credit9 := kaCredit(hourUTC(1, 9), "s1", 0.10)
	t1credit9.TenantID = "t1"

	t1ping10 := kaPing(hourUTC(1, 10), "s1", 0.03, 40_000, 0)
	t1ping10.TenantID, t1ping10.KeepAliveStrategyID = "t1", "camp-strat-1"
	t1credit10 := kaCredit(hourUTC(1, 10), "s1", 0.07)
	t1credit10.TenantID = "t1"

	// A ping under a strategy this campaign does NOT own — must not be counted.
	otherStrategyPing := kaPing(hourUTC(1, 9), "s2", 0.09, 90_000, 0)
	otherStrategyPing.TenantID, otherStrategyPing.KeepAliveStrategyID = "t1", "not-this-campaign"

	// A credited row for a tenant this campaign does NOT target — must not be counted.
	otherTenantCredit := kaCredit(hourUTC(1, 9), "s3", 5.00)
	otherTenantCredit.TenantID = "t2"

	if err := db.insertBatch([]*Event{
		t1ping9, t1credit9, t1ping10, t1credit10, otherStrategyPing, otherTenantCredit,
	}); err != nil {
		t.Fatal(err)
	}

	cells, err := db.CampaignRealSavings([]string{"camp-strat-1"}, []string{"t1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 2 {
		t.Fatalf("got %d cells, want 2 (hour 9 and hour 10): %+v", len(cells), cells)
	}
	byHour := map[int]CampaignSavingCell{}
	for _, c := range cells {
		byHour[c.HourUTC] = c
	}
	h9 := byHour[9]
	if h9.TenantID != "t1" || h9.Pings != 1 || h9.PingUSD < 0.019 || h9.PingUSD > 0.021 {
		t.Errorf("hour 9 = %+v, want t1 with 1 ping at ~$0.02", h9)
	}
	if h9.Requests != 1 || h9.SavedUSD < 0.099 || h9.SavedUSD > 0.101 {
		t.Errorf("hour 9 saving side = %+v, want 1 request at ~$0.10 saved", h9)
	}
	if h9.NetUSD != h9.SavedUSD-h9.PingUSD {
		t.Errorf("hour 9 NetUSD %v != SavedUSD %v - PingUSD %v", h9.NetUSD, h9.SavedUSD, h9.PingUSD)
	}
	h10 := byHour[10]
	if h10.Pings != 1 || h10.PingUSD < 0.029 || h10.PingUSD > 0.031 {
		t.Errorf("hour 10 = %+v, want 1 ping at ~$0.03", h10)
	}
	if h10.SavedUSD < 0.069 || h10.SavedUSD > 0.071 {
		t.Errorf("hour 10 SavedUSD = %v, want ~0.07", h10.SavedUSD)
	}
}

// A `since` bound excludes traffic before a campaign's own activated_at, on both the
// cost and the saving side — comparing a prediction against pre-campaign history would
// credit it for a period it never ran in.
func TestCampaignRealSavingsRespectsTheSinceBound(t *testing.T) {
	db := openTestDB(t)

	before := kaPing(hourUTC(1, 9), "s1", 0.02, 40_000, 0)
	before.TenantID, before.KeepAliveStrategyID = "t1", "camp-strat-1"
	beforeCredit := kaCredit(hourUTC(1, 9), "s1", 0.10)
	beforeCredit.TenantID = "t1"

	after := kaPing(hourUTC(2, 9), "s2", 0.05, 40_000, 0)
	after.TenantID, after.KeepAliveStrategyID = "t1", "camp-strat-1"
	afterCredit := kaCredit(hourUTC(2, 9), "s2", 0.20)
	afterCredit.TenantID = "t1"

	if err := db.insertBatch([]*Event{before, beforeCredit, after, afterCredit}); err != nil {
		t.Fatal(err)
	}

	cells, err := db.CampaignRealSavings([]string{"camp-strat-1"}, []string{"t1"},
		hourUTC(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 1 {
		t.Fatalf("got %d cells, want 1 (only the day-2 traffic)", len(cells))
	}
	if cells[0].Pings != 1 || cells[0].PingUSD < 0.049 || cells[0].PingUSD > 0.051 {
		t.Errorf("cell = %+v, want only the after-since ping (~$0.05)", cells[0])
	}
	if cells[0].SavedUSD < 0.199 || cells[0].SavedUSD > 0.201 {
		t.Errorf("cell SavedUSD = %v, want only the after-since credit (~0.20)", cells[0].SavedUSD)
	}
}

// ActiveDays counts distinct UTC calendar days, not rows — the denominator a $-per-
// active-day normalization needs.
func TestCampaignRealSavingsCountsDistinctActiveDays(t *testing.T) {
	db := openTestDB(t)

	day1 := kaCredit(hourUTC(1, 9), "s1", 0.01)
	day1.TenantID = "t1"
	day2 := kaCredit(hourUTC(2, 9), "s2", 0.02)
	day2.TenantID = "t1"
	day2Again := kaCredit(hourUTC(2, 9), "s3", 0.03)
	day2Again.TenantID = "t1"

	if err := db.insertBatch([]*Event{day1, day2, day2Again}); err != nil {
		t.Fatal(err)
	}

	// No strategy pinged this tenant, so pass a strategy id that matches nothing — the
	// saving side must still report, since it is keyed by tenant, not by strategy.
	cells, err := db.CampaignRealSavings([]string{"unused-strategy"}, []string{"t1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 1 {
		t.Fatalf("got %d cells, want 1", len(cells))
	}
	if cells[0].Requests != 3 || cells[0].ActiveDays != 2 {
		t.Errorf("cell = %+v, want 3 requests across 2 active days", cells[0])
	}
}

// Empty strategy or tenant lists answer with no cells rather than an invalid "IN ()".
func TestCampaignRealSavingsOnEmptyInputs(t *testing.T) {
	db := openTestDB(t)
	if cells, err := db.CampaignRealSavings(nil, []string{"t1"}, 0); err != nil || len(cells) != 0 {
		t.Errorf("empty strategyIDs: got %v, %v; want no cells, no error", cells, err)
	}
	if cells, err := db.CampaignRealSavings([]string{"s1"}, nil, 0); err != nil || len(cells) != 0 {
		t.Errorf("empty tenantIDs: got %v, %v; want no cells, no error", cells, err)
	}
}
