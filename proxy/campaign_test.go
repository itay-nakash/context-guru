package proxy

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rossoctl/context-guru/dash"
	"github.com/rossoctl/context-guru/kvcache"
	"github.com/rossoctl/context-guru/tenant"
)

// Every activatable, non-baseline arm's own live parameters must actually clear
// validStrategyBounds — a static safety net for the hardcoded constants in
// campaignArmFor, so a future edit that pushes one out of band fails a test instead of
// failing a manager's create call.
func TestCampaignArmForStaysWithinValidStrategyBounds(t *testing.T) {
	arms := []string{
		kvcache.StrategyKeepAlive5m, kvcache.StrategyKeepAlive5mOnce,
		kvcache.StrategyStopReasonGated, kvcache.StrategyKeepAlive1h, kvcache.StrategyKeepAlive1hOnce,
	}
	for _, arm := range arms {
		a := campaignArmFor(arm)
		if !a.activatable || a.baseline {
			t.Fatalf("%s: expected activatable, non-baseline", arm)
		}
		idle := time.Duration(a.idleSeconds) * time.Second
		if err := validStrategyBounds(idle, a.maxPings, campaignDefaultMinPrefixTokens, 0,
			a.headTTL1h, 0); err != nil {
			t.Errorf("%s: %+v fails validStrategyBounds: %v", arm, a, err)
		}
	}
}

// Baseline arms mean "change nothing" and create no strategy; unknown arms are refused
// with a reason, never silently treated as a baseline.
func TestCampaignArmForBaselineAndUnknown(t *testing.T) {
	for _, arm := range []string{kvcache.StrategyNoCache, kvcache.StrategyFixed5m, kvcache.StrategyFixed1h} {
		a := campaignArmFor(arm)
		if !a.activatable || !a.baseline {
			t.Errorf("%s: got %+v, want activatable baseline", arm, a)
		}
	}
	for _, arm := range []string{
		kvcache.StrategyHistorical, kvcache.StrategyStickySession1h, kvcache.StrategyObserved,
		kvcache.StrategyExtend1h, kvcache.StrategyOptimal, kvcache.StrategyReplay, "nonsense-arm",
	} {
		a := campaignArmFor(arm)
		if a.activatable || a.reason == "" {
			t.Errorf("%s: got %+v, want not activatable with a reason", arm, a)
		}
	}
}

func TestResolveCampaignCellUserExclusionIsTheCallersJob(t *testing.T) {
	// resolveCampaignCell itself does not special-case an empty user — the create
	// handler filters those out before ever calling it. This test documents that the
	// function still resolves such a cell like any other, so a reviewer does not go
	// looking for a guard inside it that was deliberately placed one level up.
	cell := dash.KVCacheSuggestion{User: "", HourUTC: 9, BestStrategy: kvcache.StrategyKeepAlive5m}
	got := resolveCampaignCell(cell, func(string) bool { return true })
	if got.tenantID != "" || !got.activatable {
		t.Errorf("got %+v, want an activatable cell for the empty tenant (exclusion happens elsewhere)", got)
	}
}

func TestResolveCampaignCellNonActivatableArmRecordsAReason(t *testing.T) {
	cell := dash.KVCacheSuggestion{User: "t1", HourUTC: 9, BestStrategy: kvcache.StrategyHistorical}
	got := resolveCampaignCell(cell, func(string) bool { return true })
	if got.activatable || got.skipReason == "" {
		t.Errorf("got %+v, want not activatable with a reason", got)
	}
}

func TestResolveCampaignCellGatesThe1hTierPerTenantModel(t *testing.T) {
	cell := dash.KVCacheSuggestion{User: "t1", HourUTC: 9, BestStrategy: kvcache.StrategyKeepAlive1h}
	honored := resolveCampaignCell(cell, func(tenantID string) bool { return tenantID == "t1" })
	if !honored.activatable {
		t.Errorf("got %+v, want activatable when the tenant's model honors the 1h tier", honored)
	}
	notHonored := resolveCampaignCell(cell, func(tenantID string) bool { return false })
	if notHonored.activatable || notHonored.skipReason == "" {
		t.Errorf("got %+v, want not activatable with a reason when the model gate fails", notHonored)
	}
}

func TestResolveCampaignCellBaselineArmIsActivatableWithNoConfig(t *testing.T) {
	cell := dash.KVCacheSuggestion{User: "t1", HourUTC: 9, BestStrategy: kvcache.StrategyFixed5m}
	got := resolveCampaignCell(cell, func(string) bool { return true })
	if !got.activatable || !got.config.baseline {
		t.Errorf("got %+v, want an activatable baseline cell", got)
	}
}

// tileHours must cover exactly the given hours — no gap inside a merged run, no
// overlap or false coverage between separate runs, and hour 23 must never merge past
// midnight into hour 0 (tenant.Window has no overnight span).
func TestTileHoursCoversExactlyItsHoursNoGapNoOverlap(t *testing.T) {
	days := []time.Weekday{time.Sunday}
	windows := tileHours([]int{9, 10, 14, 23}, days)
	if len(windows) != 3 {
		t.Fatalf("got %d windows, want 3 (9-10 merged, 14 alone, 23 alone): %+v", len(windows), windows)
	}
	byStart := map[string]struct{ start, end string }{}
	for _, w := range windows {
		byStart[w.Start] = struct{ start, end string }{w.Start, w.End}
	}
	if w, ok := byStart["09:00"]; !ok || w.end != "11:00" {
		t.Errorf("hours 9-10 = %+v, want [09:00,11:00)", byStart["09:00"])
	}
	if w, ok := byStart["14:00"]; !ok || w.end != "15:00" {
		t.Errorf("hour 14 = %+v, want [14:00,15:00)", byStart["14:00"])
	}
	w23, ok := byStart["23:00"]
	if !ok || w23.end != "23:59" {
		t.Fatalf("hour 23 = %+v, want [23:00,23:59) (never \"24:00\")", w23)
	}

	// Every window must itself be valid, and coverage must match exactly: every
	// minute inside a tiled hour is covered, the hole at 11:00-13:59 is covered by
	// NEITHER window, and no window reaches past its own hour into the next.
	loc, _ := time.LoadLocation("UTC")
	for _, w := range windows {
		w.TZ = "UTC"
		if err := w.Validate(); err != nil {
			t.Errorf("generated window %+v is invalid: %v", w, err)
		}
	}
	// Window.contains is unexported; go through Strategy.InWindow (exported) instead,
	// wrapping each generated window on its own in an always-active, all-target
	// strategy so only the window's OWN time span is under test.
	check := func(hour, minute int, wantCovered bool) {
		now := time.Date(2026, 6, 7, hour, minute, 0, 0, loc) // a Sunday
		covered := false
		for _, w := range windows {
			w.TZ, w.Days = "UTC", nil // isolate the time check from the day check
			s := tenant.Strategy{Active: true, Windows: []tenant.Window{w},
				Target: tenant.Target{Mode: tenant.TargetAll}}
			if s.InWindow(now) {
				covered = true
			}
		}
		if covered != wantCovered {
			t.Errorf("%02d:%02d covered=%v, want %v", hour, minute, covered, wantCovered)
		}
	}
	check(9, 0, true)
	check(10, 59, true)
	check(11, 0, false) // the hole between the merged run and hour 14
	check(13, 59, false)
	check(14, 0, true)
	check(14, 59, true)
	check(15, 0, false)
	check(23, 0, true)
	check(23, 59, false) // the one minute no window can ever cover
	check(0, 0, false)   // hour 23 must not wrap into hour 0
}

func TestTileHoursSingleHourEndingTheDay(t *testing.T) {
	windows := tileHours([]int{23}, nil)
	if len(windows) != 1 || windows[0].Start != "23:00" || windows[0].End != "23:59" {
		t.Errorf("got %+v, want a single [23:00,23:59) window", windows)
	}
}

func TestWeekdaysFromNamesDropsUnrecognizedNamesRatherThanErroring(t *testing.T) {
	got := weekdaysFromNames([]string{"Sunday", "Monday", "Someday"})
	if len(got) != 2 || got[0] != time.Sunday || got[1] != time.Monday {
		t.Errorf("got %v, want [Sunday, Monday]", got)
	}
}

// Two tenants sharing the same arm and the SAME hour set coalesce into one group; two
// tenants sharing the same arm but a DIFFERENT hour set must NOT be merged — a
// strategy's Target and Windows both apply to the whole strategy, so merging them
// would activate an hour for a tenant that was never recommended for it.
func TestCoalesceCampaignCellsGroupsOnlyIdenticalHourSets(t *testing.T) {
	arm5m := campaignArmFor(kvcache.StrategyKeepAlive5m)
	mk := func(tenantID string, hour int) *resolvedCampaignCell {
		return &resolvedCampaignCell{tenantID: tenantID, hourUTC: hour, arm: kvcache.StrategyKeepAlive5m,
			activatable: true, config: arm5m}
	}
	cells := []*resolvedCampaignCell{
		mk("t1", 9), mk("t2", 9), // t1 and t2 share the exact same hour set {9}
		mk("t3", 9), mk("t3", 14), // t3 has a DIFFERENT hour set {9,14}
	}
	groups := coalesceCampaignCells(cells)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2 (t1+t2 together, t3 alone): %+v", len(groups), groups)
	}
	var sharedGroup, soloGroup *campaignGroup
	for _, g := range groups {
		if len(g.tenantIDs) == 2 {
			sharedGroup = g
		} else {
			soloGroup = g
		}
	}
	if sharedGroup == nil || sharedGroup.tenantIDs[0] != "t1" || sharedGroup.tenantIDs[1] != "t2" {
		t.Errorf("shared group = %+v, want t1 and t2", sharedGroup)
	}
	if soloGroup == nil || len(soloGroup.tenantIDs) != 1 || soloGroup.tenantIDs[0] != "t3" ||
		len(soloGroup.hourSet) != 2 {
		t.Errorf("solo group = %+v, want t3 alone with 2 hours", soloGroup)
	}
}

// The full create flow, end to end, through the real HTTP route: an uploaded suggest
// payload with two tenants sharing an arm at the same hour, an empty-tenant cell that
// must be excluded, and a simulation-only arm that must be recorded but not activated.
func TestCtlCreateCampaignEndToEnd(t *testing.T) {
	f := newMgrFixture(t)
	mgrJar, _ := f.signUpJar(t, "boss@ibm.com")
	_, tenantA := f.signUpJar(t, "a@ibm.com")
	_, tenantB := f.signUpJar(t, "b@ibm.com")

	suggest := dash.KVCacheSuggestions{
		Baseline: "fixed-5m",
		Weekdays: []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday"},
		Cells: []dash.KVCacheSuggestion{
			{User: tenantA, HourUTC: 9, Requests: 42, BestStrategy: kvcache.StrategyKeepAlive5m,
				SavingUSD: 1.23, BaselineUSD: 4.56},
			{User: tenantB, HourUTC: 9, Requests: 12, BestStrategy: kvcache.StrategyKeepAlive5m,
				SavingUSD: 0.50, BaselineUSD: 2.00},
			{User: tenantA, HourUTC: 10, Requests: 3, BestStrategy: kvcache.StrategyStickySession1h,
				InsufficientData: true},
			// Must be excluded entirely: an ambiguous, unsafe campaign target.
			{User: "", HourUTC: 11, Requests: 999, BestStrategy: kvcache.StrategyKeepAlive5m},
		},
	}
	body, _ := json.Marshal(map[string]any{"name": "rollout-1", "source": "upload", "suggest": suggest})
	w, out := f.do(t, "POST", "/api/keepalive/campaigns", string(body), mgrJar)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	campaignID, _ := out["id"].(string)
	if campaignID == "" {
		t.Fatal("no campaign id returned")
	}
	cells, _ := out["cells"].([]any)
	if len(cells) != 3 {
		t.Fatalf("got %d cells, want 3 (the empty-user cell must be excluded): %v", len(cells), cells)
	}

	// tenantA and tenantB share the same arm at the same hour -> one coalesced
	// strategy targeting both, at keepalive-5m's live parameters.
	strategies, err := f.reg.ListStrategies()
	if err != nil {
		t.Fatal(err)
	}
	if len(strategies) != 1 {
		t.Fatalf("got %d strategies, want 1 (coalesced): %+v", len(strategies), strategies)
	}
	s := strategies[0]
	if s.IdleSeconds != 280 || len(s.Target.TenantIDs) != 2 || s.Target.Mode != tenant.TargetList {
		t.Errorf("strategy = %+v, want idle 280s targeting both tenants", s)
	}

	if w, _ := f.do(t, "GET", "/api/keepalive/campaigns", "", mgrJar); w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body)
	}

	w, out = f.do(t, "GET", "/api/keepalive/campaigns/"+campaignID, "", mgrJar)
	if w.Code != http.StatusOK {
		t.Fatalf("get = %d %s", w.Code, w.Body)
	}
	detailCells, _ := out["cells"].([]any)
	if len(detailCells) != 3 {
		t.Fatalf("got %d cells, want 3", len(detailCells))
	}
	foundSkipped := false
	for _, c := range detailCells {
		m := c.(map[string]any)
		if m["arm"] == kvcache.StrategyStickySession1h {
			foundSkipped = true
			if activatable, _ := m["activatable"].(bool); activatable {
				t.Errorf("sticky-session-1h cell = %v, want not activatable", m)
			}
			if reason, _ := m["skip_reason"].(string); reason == "" {
				t.Errorf("sticky-session-1h cell = %v, want a skip reason", m)
			}
		}
	}
	if !foundSkipped {
		t.Error("the sticky-session-1h cell was not recorded at all")
	}

	w, out = f.do(t, "GET", "/api/keepalive/campaigns/"+campaignID+"/tenants/"+tenantA, "", mgrJar)
	if w.Code != http.StatusOK {
		t.Fatalf("tenant drilldown = %d %s", w.Code, w.Body)
	}
	tCells, _ := out["cells"].([]any)
	if len(tCells) != 2 {
		t.Fatalf("got %d cells for tenant A, want 2 (hour 9 and hour 10)", len(tCells))
	}
}

// An empty-user suggest payload creates nothing to campaign over — a clear 400, not a
// campaign with zero cells.
func TestCtlCreateCampaignAllCellsExcludedIsRefused(t *testing.T) {
	f := newMgrFixture(t)
	mgrJar, _ := f.signUpJar(t, "boss@ibm.com")
	suggest := dash.KVCacheSuggestions{
		Cells: []dash.KVCacheSuggestion{{User: "", HourUTC: 9, BestStrategy: kvcache.StrategyKeepAlive5m}},
	}
	body, _ := json.Marshal(map[string]any{"name": "empty", "source": "upload", "suggest": suggest})
	w, _ := f.do(t, "POST", "/api/keepalive/campaigns", string(body), mgrJar)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("create = %d %s, want 400", w.Code, w.Body)
	}
}

// The campaign detail route's per-tenant aggregate sums frozen predicted $ from the
// cells alongside real $ read live from dash — the wiring TestCampaignRealSavings*
// (dash package) already covers at the query level; this covers that ctlGetCampaign
// actually reaches it and sums it correctly across tenants.
func TestCtlGetCampaignAggregatesPredictedAndRealPerTenant(t *testing.T) {
	f := newMgrFixture(t)
	mgrJar, _ := f.signUpJar(t, "boss@ibm.com")
	_, tenantA := f.signUpJar(t, "a@ibm.com")

	suggest := dash.KVCacheSuggestions{
		Cells: []dash.KVCacheSuggestion{
			{User: tenantA, HourUTC: 9, Requests: 10, BestStrategy: kvcache.StrategyKeepAlive5m,
				SavingUSD: 1.50, BaselineUSD: 3.00},
		},
	}
	body, _ := json.Marshal(map[string]any{"name": "agg-test", "source": "upload", "suggest": suggest})
	w, created := f.do(t, "POST", "/api/keepalive/campaigns", string(body), mgrJar)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	campaignID, _ := created["id"].(string)
	createdCells, _ := created["cells"].([]any)
	strategyID, _ := createdCells[0].(map[string]any)["strategy_id"].(string)
	if strategyID == "" {
		t.Fatalf("created cell has no strategy id: %v", createdCells)
	}

	f.record(t, tenantA, "s1", &dash.Event{
		KeepAlive: true, KeepAliveStrategyID: strategyID, CostUSD: 0.02,
		CacheRead: 40_000, Model: "aws/claude-sonnet-5", TokenAccounting: dash.AccountingComplete,
	})
	f.record(t, tenantA, "s1", &dash.Event{
		KeepAliveSavedUSD: 0.10, Model: "aws/claude-sonnet-5", TokenAccounting: dash.AccountingComplete,
	})

	w, out := f.do(t, "GET", "/api/keepalive/campaigns/"+campaignID, "", mgrJar)
	if w.Code != http.StatusOK {
		t.Fatalf("get = %d %s", w.Code, w.Body)
	}
	tenants, _ := out["tenants"].([]any)
	if len(tenants) != 1 {
		t.Fatalf("got %d tenant summaries, want 1: %v", len(tenants), tenants)
	}
	row := tenants[0].(map[string]any)
	if row["tenant_id"] != tenantA {
		t.Errorf("tenant_id = %v, want %v", row["tenant_id"], tenantA)
	}
	if p, _ := row["predicted_usd"].(float64); p < 1.49 || p > 1.51 {
		t.Errorf("predicted_usd = %v, want ~1.50", row["predicted_usd"])
	}
	if s, _ := row["real_saved_usd"].(float64); s < 0.099 || s > 0.101 {
		t.Errorf("real_saved_usd = %v, want ~0.10", row["real_saved_usd"])
	}
	if total, _ := out["total_predicted_usd"].(float64); total < 1.49 || total > 1.51 {
		t.Errorf("total_predicted_usd = %v, want ~1.50", out["total_predicted_usd"])
	}
	if out["caveat"] == "" || out["caveat"] == nil {
		t.Error("the ceiling caveat was not carried onto the response")
	}
}

func TestCoalesceCampaignCellsExcludesBaselineAndNonActivatable(t *testing.T) {
	cells := []*resolvedCampaignCell{
		{tenantID: "t1", hourUTC: 9, arm: kvcache.StrategyFixed5m, activatable: true,
			config: campaignArm{baseline: true}},
		{tenantID: "t1", hourUTC: 10, arm: kvcache.StrategyHistorical, activatable: false},
	}
	groups := coalesceCampaignCells(cells)
	if len(groups) != 0 {
		t.Errorf("got %d groups, want 0 (nothing here is activatable-and-non-baseline)", len(groups))
	}
}
