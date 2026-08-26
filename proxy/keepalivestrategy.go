package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/rossoctl/context-guru/tenant"
)

// Manager-controlled keep-alive strategies: a durable, manager-authored rule that runs a
// schedule ("only during Israel business hours") or a service-wide policy, above every
// tenant's own account config and below a per-session override — see
// docs/superpowers/specs/2026-08-25-keepalive-strategies-design.md for the full design.
//
// This file is the resolution-chain generalization (applyStrategy, alongside
// keepaliveoverride.go's overrideFor) and the control routes. Persistence lives in
// tenant/keepalivestrategy.go, because a strategy is account/control-plane data and,
// unlike a per-session override, is meant to survive a restart.

// setStrategies replaces the keeper's in-memory strategy list wholesale, under lock —
// the same swap-not-mutate pattern k.overrides documents, so a request resolving a
// policy mid-write never sees a half-built list.
func (k *keeper) setStrategies(list []tenant.Strategy) {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.strategies = list
	k.mu.Unlock()
}

// loadStrategies re-reads the strategy table from the registry: at process start
// (newKeeper) and after every create/update/delete through the control routes below, so
// "no re-push needed, since matching is live" is actually true.
//
// Fails open, per this project's hard boundary: a read error is logged and the
// in-memory list is left as it was, rather than resolving every request as if no
// strategy exists on a deployment that plainly has some.
func (k *keeper) loadStrategies() {
	if k == nil || k.h == nil || k.h.opts.Tenants == nil {
		return
	}
	list, err := k.h.registry().ListStrategies()
	if err != nil {
		slog.Warn("context-guru: could not load keep-alive strategies", "err", err)
		return
	}
	k.setStrategies(list)
}

// applyStrategy resolves the highest-priority ACTIVE strategy matching tenantID at
// `now`, if any, and returns the policy with its fields replaced plus the matched
// strategy's id ("" when none matched).
//
// Called from record, between account config and the session override — see the design
// doc's resolution chain. Evaluated once, at record time, and then fixed on the entry
// for its whole lifetime, exactly as overrideFor's own resolution already is: a strategy
// whose window closes mid-hold does not retroactively un-arm a ping already scheduled.
func (k *keeper) applyStrategy(tenantID string, pol CachePolicy, now time.Time) (CachePolicy, string) {
	k.mu.Lock()
	strategies := k.strategies
	k.mu.Unlock()
	var best *tenant.Strategy
	for i := range strategies {
		s := &strategies[i]
		if !s.Matches(now, tenantID) {
			continue
		}
		if best == nil || betterStrategy(s, best) {
			best = s
		}
	}
	if best == nil {
		return pol, ""
	}
	pol.KeepAlive = true
	pol.Idle = time.Duration(best.IdleSeconds) * time.Second
	pol.MaxPings = best.MaxPings
	pol.MinPrefixTokens = best.MinPrefixTokens
	pol.MaxUSDPerPing = best.MaxUSDPerPing
	return pol, best.ID
}

// betterStrategy reports whether candidate beats current under the design doc's fixed
// precedence: a list-target strategy beats an all-target one (more specific wins);
// among equally specific matches, the most recently updated wins. Not configurable —
// "no priority field to expose in the UI, one less knob a manager can get wrong."
func betterStrategy(candidate, current *tenant.Strategy) bool {
	cSpecific := candidate.Target.Mode == tenant.TargetList
	curSpecific := current.Target.Mode == tenant.TargetList
	if cSpecific != curSpecific {
		return cSpecific
	}
	return candidate.UpdatedAt.After(current.UpdatedAt)
}

// validStrategyBounds checks the numeric fields against the SAME bounds validOverride
// already enforces for Idle/MaxPings/MinPrefixTokens — a strategy is a
// broader-blast-radius version of the same spend authorization, so it gets at least as
// tight a check, not a looser one.
//
// MaxUSDPerPing is the one field an override does not expose at all; a strategy may set
// it because this is an audited manager action rather than an ephemeral grant (see the
// design doc's "Model"). Checked on its own terms: non-negative, with 0 left to mean
// "use the package default" exactly like account config does (CachePolicy.Ceiling()).
func validStrategyBounds(idle time.Duration, pings, minPrefix int, maxUSDPerPing float64) error {
	if idle < minOverrideIdle || idle > maxOverrideIdle {
		return fmt.Errorf(
			"the idle interval must be between %d and %d seconds; past %d the first ping "+
				"arrives after the provider's 5-minute lifetime has already lapsed and pays "+
				"a cache WRITE instead of a read",
			int(minOverrideIdle.Seconds()), int(maxOverrideIdle.Seconds()), int(maxOverrideIdle.Seconds()))
	}
	if pings < minOverridePings || pings > maxOverridePings {
		return fmt.Errorf("the ping count must be between %d and %d", minOverridePings, maxOverridePings)
	}
	if minPrefix < 0 {
		return fmt.Errorf("the prefix floor cannot be negative")
	}
	if maxUSDPerPing < 0 {
		return fmt.Errorf("the per-ping cost ceiling cannot be negative")
	}
	return nil
}

// knownPredictorIDs is the set of predictor names a strategy is allowed to reference.
//
// Empty for now, DELIBERATELY: the runtime predictor-gating hook (kvcache.Predictor
// wired into due()/pingable()) does not exist yet, so accepting an arbitrary
// predictor_id here would let a manager create a strategy that references something
// that can never resolve to anything — silently inert, and indistinguishable from a
// typo. The data model (tenant.Strategy.PredictorID/PredictorThreshold) is ready ahead
// of the hook on purpose; this map is what turns it on, one name at a time, once each
// predictor is actually wired into the ping decision. See docs/results/kv-ttl-predictor-*.md.
var knownPredictorIDs = map[string]bool{}

// validPredictorRef checks predictorID against knownPredictorIDs (empty is always
// valid — "no predictor gate") and the threshold's own shape.
func validPredictorRef(predictorID string, threshold float64) error {
	s := tenant.Strategy{PredictorID: predictorID, PredictorThreshold: threshold}
	if err := s.ValidatePredictor(); err != nil {
		return err
	}
	if predictorID != "" && !knownPredictorIDs[predictorID] {
		return fmt.Errorf("%q is not a registered predictor; no predictor-gated strategies "+
			"can be created yet", predictorID)
	}
	return nil
}

// validWindows checks that a strategy has at least one window (one with no schedule can
// never fire, which is never what a manager who created it meant) and that each one is
// individually valid.
func validWindows(windows []tenant.Window) error {
	if len(windows) == 0 {
		return fmt.Errorf("a strategy needs at least one window; one with no schedule can never fire")
	}
	for _, w := range windows {
		if err := w.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// keepAliveStrategyCtlRoutes is this feature's control-plane table, appended to
// ctlRoutes in control.go. Every route is ctlManager and audited, table-driven exactly
// like keepAliveCtlRoutes' own table.
func (h *Handler) keepAliveStrategyCtlRoutes() []ctlRoute {
	return []ctlRoute{
		{"GET /api/keepalive/strategies", ctlManager, h.ctlListKeepAliveStrategies},
		{"POST /api/keepalive/strategies", ctlManager, h.ctlCreateKeepAliveStrategy},
		{"PATCH /api/keepalive/strategies/{id}", ctlManager, h.ctlPatchKeepAliveStrategy},
		{"DELETE /api/keepalive/strategies/{id}", ctlManager, h.ctlDeleteKeepAliveStrategy},
	}
}

// strategyView is the wire shape for one strategy, plus InWindow — the live-resolved
// "currently in a matching window: yes/no" the design doc asks the list route for, so the
// UI's at-a-glance state needs no arithmetic of its own.
type strategyView struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	IdleSeconds        int             `json:"idle_seconds"`
	MaxPings           int             `json:"max_pings"`
	MinPrefixTokens    int             `json:"min_prefix_tokens"`
	MaxUSDPerPing      float64         `json:"max_usd_per_ping"`
	Windows            []tenant.Window `json:"windows"`
	Target             tenant.Target   `json:"target"`
	Active             bool            `json:"active"`
	PredictorID        string          `json:"predictor_id"`
	PredictorThreshold float64         `json:"predictor_threshold"`
	CreatedBy          string          `json:"created_by"`
	CreatedAt          int64           `json:"created_at"`
	UpdatedBy          string          `json:"updated_by"`
	UpdatedAt          int64           `json:"updated_at"`
	InWindow           bool            `json:"in_window"`
}

func viewStrategy(s tenant.Strategy, now time.Time) strategyView {
	return strategyView{
		ID: s.ID, Name: s.Name, IdleSeconds: s.IdleSeconds, MaxPings: s.MaxPings,
		MinPrefixTokens: s.MinPrefixTokens, MaxUSDPerPing: s.MaxUSDPerPing,
		Windows: s.Windows, Target: s.Target, Active: s.Active,
		PredictorID: s.PredictorID, PredictorThreshold: s.PredictorThreshold,
		CreatedBy: s.CreatedBy, CreatedAt: msOrZero(s.CreatedAt),
		UpdatedBy: s.UpdatedBy, UpdatedAt: msOrZero(s.UpdatedAt),
		InWindow: s.InWindow(now),
	}
}

// ctlListKeepAliveStrategies lists every strategy.
func (h *Handler) ctlListKeepAliveStrategies(w http.ResponseWriter, r *http.Request) {
	actor, err := h.webPrincipal(r)
	if err != nil {
		code, msg := statusOf(err)
		ctlErr(w, code, msg)
		return
	}
	if !actor.IsManager() {
		ctlErr(w, http.StatusForbidden, "manager only")
		return
	}
	list, err := h.registry().ListStrategies()
	if err != nil {
		ctlErr(w, http.StatusInternalServerError, "could not list keep-alive strategies")
		return
	}
	now := time.Now()
	out := make([]strategyView, 0, len(list))
	for _, s := range list {
		out = append(out, viewStrategy(s, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"strategies": out})
}

// strategyIn is a create request's body.
type strategyIn struct {
	Name               string          `json:"name"`
	IdleSeconds        int             `json:"idle_seconds"`
	MaxPings           int             `json:"max_pings"`
	MinPrefixTokens    int             `json:"min_prefix_tokens"`
	MaxUSDPerPing      float64         `json:"max_usd_per_ping"`
	Windows            []tenant.Window `json:"windows"`
	Target             tenant.Target   `json:"target"`
	Active             bool            `json:"active"`
	PredictorID        string          `json:"predictor_id"`
	PredictorThreshold float64         `json:"predictor_threshold"`
}

// ctlCreateKeepAliveStrategy creates a strategy: validated at least as strictly as an
// override, audited, and loaded into the keeper before the response is written — so the
// very next request may match it, with no restart.
func (h *Handler) ctlCreateKeepAliveStrategy(w http.ResponseWriter, r *http.Request) {
	actor, err := h.webPrincipal(r)
	if err != nil {
		code, msg := statusOf(err)
		ctlErr(w, code, msg)
		return
	}
	if !actor.IsManager() {
		ctlErr(w, http.StatusForbidden, "manager only")
		return
	}
	var in strategyIn
	if err := readJSON(w, r, &in); err != nil {
		readErr(w, err)
		return
	}
	idle := time.Duration(in.IdleSeconds) * time.Second
	if err := validStrategyBounds(idle, in.MaxPings, in.MinPrefixTokens, in.MaxUSDPerPing); err != nil {
		ctlErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.Target.Validate(); err != nil {
		ctlErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validWindows(in.Windows); err != nil {
		ctlErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validPredictorRef(in.PredictorID, in.PredictorThreshold); err != nil {
		ctlErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s, err := h.registry().CreateStrategy(actor.ID, tenant.Strategy{
		Name: in.Name, IdleSeconds: in.IdleSeconds, MaxPings: in.MaxPings,
		MinPrefixTokens: in.MinPrefixTokens, MaxUSDPerPing: in.MaxUSDPerPing,
		Windows: in.Windows, Target: in.Target, Active: in.Active,
		PredictorID: in.PredictorID, PredictorThreshold: in.PredictorThreshold,
	})
	if err != nil {
		ctlErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The audited object is the strategy itself, named by its id in the field column —
	// there is no single target tenant for an all-target strategy, so actor and target
	// are both the manager's own id, unlike every other audited action in this codebase.
	if err := h.registry().AuditWrite(actor.ID, actor.ID, s.ID, "", "created: "+s.Name); err != nil {
		// A strategy we cannot account for is a strategy we do not keep — the same refusal
		// ctlKeepAliveArm makes when its own audit write fails.
		_ = h.registry().DeleteStrategy(s.ID)
		ctlErr(w, http.StatusInternalServerError,
			"could not record this in the audit log, so it was not created")
		return
	}
	h.keeper.loadStrategies()
	writeJSON(w, http.StatusCreated, viewStrategy(s, time.Now()))
}

// strategyPatchIn is an update request's body: pointers, so "not sent" and "set to
// zero/empty" are different things, matching tenant.Patch's own convention.
type strategyPatchIn struct {
	Name               *string          `json:"name"`
	IdleSeconds        *int             `json:"idle_seconds"`
	MaxPings           *int             `json:"max_pings"`
	MinPrefixTokens    *int             `json:"min_prefix_tokens"`
	MaxUSDPerPing      *float64         `json:"max_usd_per_ping"`
	Windows            *[]tenant.Window `json:"windows"`
	Target             *tenant.Target   `json:"target"`
	Active             *bool            `json:"active"`
	PredictorID        *string          `json:"predictor_id"`
	PredictorThreshold *float64         `json:"predictor_threshold"`
}

// ctlPatchKeepAliveStrategy updates any field; takes effect on the next request that
// would match, since matching is live.
func (h *Handler) ctlPatchKeepAliveStrategy(w http.ResponseWriter, r *http.Request) {
	actor, err := h.webPrincipal(r)
	if err != nil {
		code, msg := statusOf(err)
		ctlErr(w, code, msg)
		return
	}
	if !actor.IsManager() {
		ctlErr(w, http.StatusForbidden, "manager only")
		return
	}
	id := r.PathValue("id")
	cur, err := h.registry().StrategyByID(id)
	if err != nil {
		ctlErr(w, http.StatusNotFound, "no such strategy")
		return
	}
	var in strategyPatchIn
	if err := readJSON(w, r, &in); err != nil {
		readErr(w, err)
		return
	}
	// Validated against the RESOLVED strategy, not only the fields this call sent: a
	// patch that only touches MaxPings must not let a previously-stored Idle drift out of
	// bounds unchecked.
	next := cur
	if in.Name != nil {
		next.Name = *in.Name
	}
	if in.IdleSeconds != nil {
		next.IdleSeconds = *in.IdleSeconds
	}
	if in.MaxPings != nil {
		next.MaxPings = *in.MaxPings
	}
	if in.MinPrefixTokens != nil {
		next.MinPrefixTokens = *in.MinPrefixTokens
	}
	if in.MaxUSDPerPing != nil {
		next.MaxUSDPerPing = *in.MaxUSDPerPing
	}
	if in.Windows != nil {
		next.Windows = *in.Windows
	}
	if in.Target != nil {
		next.Target = *in.Target
	}
	if in.Active != nil {
		next.Active = *in.Active
	}
	if in.PredictorID != nil {
		next.PredictorID = *in.PredictorID
	}
	if in.PredictorThreshold != nil {
		next.PredictorThreshold = *in.PredictorThreshold
	}
	idle := time.Duration(next.IdleSeconds) * time.Second
	if err := validStrategyBounds(idle, next.MaxPings, next.MinPrefixTokens, next.MaxUSDPerPing); err != nil {
		ctlErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := next.Target.Validate(); err != nil {
		ctlErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validWindows(next.Windows); err != nil {
		ctlErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validPredictorRef(next.PredictorID, next.PredictorThreshold); err != nil {
		ctlErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s, err := h.registry().UpdateStrategy(actor.ID, id, tenant.StrategyPatch{
		Name: in.Name, IdleSeconds: in.IdleSeconds, MaxPings: in.MaxPings,
		MinPrefixTokens: in.MinPrefixTokens, MaxUSDPerPing: in.MaxUSDPerPing,
		Windows: in.Windows, Target: in.Target, Active: in.Active,
		PredictorID: in.PredictorID, PredictorThreshold: in.PredictorThreshold,
	})
	if err != nil {
		ctlErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.registry().AuditWrite(actor.ID, actor.ID, id, "updated", "updated: "+s.Name); err != nil {
		// An update we cannot account for is an update we do not keep — the same refusal
		// ctlCreateKeepAliveStrategy makes when its own audit write fails. Reverted to the
		// pre-patch values fetched above, rather than left standing with no audit row.
		_, _ = h.registry().UpdateStrategy(actor.ID, id, tenant.StrategyPatch{
			Name: &cur.Name, IdleSeconds: &cur.IdleSeconds, MaxPings: &cur.MaxPings,
			MinPrefixTokens: &cur.MinPrefixTokens, MaxUSDPerPing: &cur.MaxUSDPerPing,
			Windows: &cur.Windows, Target: &cur.Target, Active: &cur.Active,
			PredictorID: &cur.PredictorID, PredictorThreshold: &cur.PredictorThreshold,
		})
		ctlErr(w, http.StatusInternalServerError,
			"could not record this in the audit log, so the update was not applied")
		return
	}
	h.keeper.loadStrategies()
	writeJSON(w, http.StatusOK, viewStrategy(s, time.Now()))
}

// ctlDeleteKeepAliveStrategy deletes a strategy. Anything currently held under it is not
// retroactively un-pinged — a ping already sent already happened — but it stops
// matching new requests immediately.
func (h *Handler) ctlDeleteKeepAliveStrategy(w http.ResponseWriter, r *http.Request) {
	actor, err := h.webPrincipal(r)
	if err != nil {
		code, msg := statusOf(err)
		ctlErr(w, code, msg)
		return
	}
	if !actor.IsManager() {
		ctlErr(w, http.StatusForbidden, "manager only")
		return
	}
	// No body to read, so readJSON's cross-site guard does not cover it: check directly,
	// as ctlLogout and ctlManagerReset do for the same reason.
	if err := checkOrigin(r); err != nil {
		readErr(w, err)
		return
	}
	id := r.PathValue("id")
	s, err := h.registry().StrategyByID(id)
	if err != nil {
		ctlErr(w, http.StatusNotFound, "no such strategy")
		return
	}
	if err := h.registry().DeleteStrategy(id); err != nil {
		ctlErr(w, http.StatusNotFound, "no such strategy")
		return
	}
	if err := h.registry().AuditWrite(actor.ID, actor.ID, id, s.Name, "deleted"); err != nil {
		ctlErr(w, http.StatusInternalServerError, "deleted, but the audit log could not be written")
		return
	}
	h.keeper.loadStrategies()
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}
