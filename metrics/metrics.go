// Package metrics turns component/run reports into telemetry. It provides
// concrete components.Emitter implementations; the pipeline depends only on the
// Emitter interface, so this package imports components and not vice versa.
//
// v1 ships: Nop (in components), Slog (structured logs in OTel gen_ai.*
// vocabulary), and Aggregator (in-process rollups behind /stats). An OTel-SDK
// emitter and honest-metrics extras (bounce-adjusted savings, waste signals)
// land in P5. Each host surfaces these natively (proxy -> bifrost Prometheus/
// OTel + /metrics; AuthBridge -> StatsSource).
package metrics

import (
	"log/slog"
	"sort"
	"sync"

	"github.com/rossoctl/context-guru/components"
)

// Tee fans a report out to several emitters.
type Tee []components.Emitter

func (t Tee) Component(r components.Report) {
	for _, e := range t {
		e.Component(r)
	}
}
func (t Tee) Run(r components.RunReport) {
	for _, e := range t {
		e.Run(r)
	}
}

// Slog logs each component and run in the GenAI semantic-convention vocabulary.
type Slog struct{ L *slog.Logger }

func (s Slog) logger() *slog.Logger {
	if s.L != nil {
		return s.L
	}
	return slog.Default()
}

func (s Slog) Component(r components.Report) {
	s.logger().Info("context_engineering.component",
		"context_engineering.component", r.Component,
		"context_engineering.kind", r.Kind,
		"context_engineering.tokens.before", r.TokensBefore,
		"context_engineering.tokens.after", r.TokensAfter,
		"context_engineering.tokens.saved", r.Saved(),
		"context_engineering.reverted", r.Reverted,
		"context_engineering.duration_ms", r.DurationMs,
	)
}

func (s Slog) Run(r components.RunReport) {
	s.logger().Info("context_engineering.run",
		"context_engineering.session", r.Session,
		"context_engineering.tokens.before", r.TokensBefore,
		"context_engineering.tokens.after", r.TokensAfter,
		"context_engineering.tokens.saved", r.Saved(),
		"context_engineering.duration_ms", r.DurationMs,
	)
}

// Aggregator keeps process-wide and per-component rollups for /stats. Savings
// are token-weighted (SUM saved / SUM before), the honest aggregate — not a
// mean of per-request percentages (rtk's lesson).
type Aggregator struct {
	mu       sync.Mutex
	requests int64
	before   int64
	after    int64
	wasted   int64 // tokens re-served via expand (offloaded then needed back)
	bounces  int64
	perComp  map[string]*compStat
	// End-to-end latency accounting (W7). addedMs is the wall time context-guru itself
	// adds per request (normalize + pipeline + writeback); upstreamMs is the provider
	// round-trip (incl. the expand loop). Split by bypass so a run can compare the
	// with-CG path against a transparent (x-context-guru-bypass) baseline.
	addedMs       float64
	addedSamples  int64
	upstreamMs    float64
	upstreamMsByp float64 // upstream latency on bypassed (baseline) requests
	upstreamN     int64
	upstreamNByp  int64
	// Provider-billed usage, summed from response bodies (W8). These are the tiers
	// that actually cost money — on a prompt-caching backend a cache write bills
	// ~11.5x a read, so content-token savings alone cannot express the economics.
	freshInput int64
	cacheRead  int64
	cacheWrite int64
	outputTok  int64
	// attempted is the tokens compaction was ALLOWED to touch (the uncached tail
	// when cache-aware); frozen is what cache safety made us leave alone. Together
	// they give /stats an honest denominator and the cost of its own safety.
	attempted int64
	frozen    int64
}

type compStat struct {
	Runs        int64 `json:"runs"`
	Acted       int64 `json:"acted"`   // runs that actually saved tokens
	Mutated     int64 `json:"mutated"` // runs that changed the request at all (may save 0 content tokens, e.g. cacheinject)
	Reverted    int64 `json:"reverted"`
	Saved       int64 `json:"saved_tokens"`        // CUMULATIVE: summed every turn the compaction re-appears
	SavedUnique int64 `json:"saved_tokens_unique"` // UNIQUE: each distinct compaction counted once (deduped by content key)
	// OvercountRatio = Saved / SavedUnique — how many times, on average, each distinct
	// compaction was re-counted (the agent re-sends history verbatim every turn). ~1.0
	// is honest; large values mean the cumulative figure is inflated by re-sends.
	OvercountRatio float64             `json:"overcount_ratio"`
	DurationMs     float64             `json:"duration_ms"` // cumulative wall time this component spent (its own latency cost on the hot path)
	seenKeys       map[string]struct{} // content keys already counted toward SavedUnique (not serialized)
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator { return &Aggregator{perComp: map[string]*compStat{}} }

func (a *Aggregator) Component(r components.Report) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cs := a.perComp[r.Component]
	if cs == nil {
		cs = &compStat{}
		a.perComp[r.Component] = cs
	}
	cs.Runs++
	cs.Saved += int64(r.Saved())
	cs.DurationMs += r.DurationMs // per-component latency cost on the hot path
	// Unique savings: dedup by the content-derived CacheKeys so the same compaction,
	// re-sent verbatim on later turns, is not re-counted. Attribute this run's saved
	// tokens proportionally to how many of its keys are NEW. Components that stash no
	// key (rare) fall back to counting the run as unique.
	if saved := int64(r.Saved()); saved > 0 && !r.Reverted && !r.Skipped {
		if len(r.CacheKeys) == 0 {
			cs.SavedUnique += saved
		} else {
			if cs.seenKeys == nil {
				cs.seenKeys = map[string]struct{}{}
			}
			newKeys := 0
			for _, k := range r.CacheKeys {
				if _, seen := cs.seenKeys[k]; !seen {
					cs.seenKeys[k] = struct{}{}
					newKeys++
				}
			}
			if newKeys > 0 {
				cs.SavedUnique += saved * int64(newKeys) / int64(len(r.CacheKeys))
			}
		}
	}
	if r.Reverted {
		cs.Reverted++
	}
	if !r.Reverted && !r.Skipped {
		cs.Mutated++ // did something, even if it saved no content tokens
	}
	if r.Saved() > 0 && !r.Reverted && !r.Skipped {
		cs.Acted++
	}
}

// RecordExpand notes that `tokens` of previously-offloaded content had to be
// re-served (the model called expand). This is the bounce signal: it means an
// offload was premature, so the honest savings figure subtracts it (lean-ctx's
// adjusted savings).
func (a *Aggregator) RecordExpand(tokens int) {
	a.mu.Lock()
	a.wasted += int64(tokens)
	a.bounces++
	a.mu.Unlock()
}

// RecordUsage adds one response's provider-billed token tiers. Called with what
// the provider reported; a response that reports nothing contributes nothing
// (never a zero-filled row that would read as "free").
func (a *Aggregator) RecordUsage(fresh, cacheRead, cacheWrite, output int64) {
	a.mu.Lock()
	a.freshInput += fresh
	a.cacheRead += cacheRead
	a.cacheWrite += cacheWrite
	a.outputTok += output
	a.mu.Unlock()
}

// RecordEligibility notes how many tokens this request's offloaders were allowed
// to touch, and how many cache-awareness froze — the numerator's honest
// denominator, and the cost of our own safety mechanism.
func (a *Aggregator) RecordEligibility(attempted, frozen int) {
	a.mu.Lock()
	a.attempted += int64(attempted)
	a.frozen += int64(frozen)
	a.mu.Unlock()
}

// RecordAddedLatency notes the wall time (ms) context-guru added to one request
// (normalize + pipeline + writeback). Only meaningful on the active path.
func (a *Aggregator) RecordAddedLatency(ms float64) {
	a.mu.Lock()
	a.addedMs += ms
	a.addedSamples++
	a.mu.Unlock()
}

// RecordUpstreamLatency notes one provider round-trip (ms), split by whether the
// request bypassed context-guru — so a run can compare with-CG vs baseline latency.
func (a *Aggregator) RecordUpstreamLatency(ms float64, bypassed bool) {
	a.mu.Lock()
	if bypassed {
		a.upstreamMsByp += ms
		a.upstreamNByp++
	} else {
		a.upstreamMs += ms
		a.upstreamN++
	}
	a.mu.Unlock()
}

func (a *Aggregator) Run(r components.RunReport) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests++
	a.before += int64(r.TokensBefore)
	a.after += int64(r.TokensAfter)
}

// Snapshot is the JSON served at /stats. It reports both gross savings and the
// honest, bounce-adjusted figure, plus quality signals naming components that
// never earned their place (rtk's top_passthrough idea).
type Snapshot struct {
	Requests      int64               `json:"requests"`
	TokensBefore  int64               `json:"tokens_before"`
	TokensAfter   int64               `json:"tokens_after"`
	SavedTokens   int64               `json:"saved_tokens"`
	SavingsPct    float64             `json:"savings_pct"`
	WastedTokens  int64               `json:"wasted_tokens"`  // re-served via expand
	Bounces       int64               `json:"bounces"`        // expand events
	AdjustedSaved int64               `json:"adjusted_saved"` // saved - wasted (may be negative)
	Components    map[string]compStat `json:"components"`
	// TopPassthrough names components that ran but never saved a token — dead
	// weight in the pipeline, candidates to drop from the config.
	TopPassthrough []string `json:"top_passthrough"`
	// LLM* report the cheap (config-source) model usage the CONTEXT-GURU components
	// themselves incurred (e.g. extract:code's Starlark-writer calls) — the CG
	// components' OWN cost, separate from the agent. Priced externally.
	LLMCalls        int64 `json:"llm_calls"`
	LLMInputTokens  int64 `json:"llm_input_tokens"`
	LLMOutputTokens int64 `json:"llm_output_tokens"`
	// End-to-end latency (W7): mean ms context-guru added per request, and mean
	// provider round-trip on the active vs bypassed (baseline) path — a with/without
	// context-guru session-latency comparison.
	AddedLatencyMsAvg     float64 `json:"cg_added_ms_avg"`
	UpstreamMsAvg         float64 `json:"upstream_ms_avg"`
	UpstreamMsAvgBypassed float64 `json:"upstream_ms_avg_bypassed"`
	// Provider-billed token tiers (W8), summed from response usage. ADDITIVE: the
	// benchmark harnesses parse this payload, so fields are only ever added here,
	// never renamed or removed (see the golden shape test).
	FreshInputTokens int64 `json:"fresh_input_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	// AttemptedTokens is what compaction was ALLOWED to touch; FrozenTokens is what
	// cache-awareness deliberately left alone. SavingsPctAttempted divides savings
	// by the former — the honest ratio, since SavingsPct's whole-request denominator
	// recounts the transcript every turn and trends to ~0% on a long session.
	AttemptedTokens int64 `json:"attempted_tokens"`
	FrozenTokens    int64 `json:"frozen_tokens"`
	// SavingsPctAttempted = saved / attempted. 0 when nothing was attempted.
	SavingsPctAttempted float64 `json:"savings_pct_attempted"`
	// SavingsPctNewInput = saved / (fresh + cache_write + saved): savings as a
	// fraction of what would have newly entered the provider. 0 (not 100) when the
	// provider reported no usage — savings must never be divided by themselves.
	SavingsPctNewInput float64 `json:"savings_pct_new_input"`
}

// Snapshot returns a point-in-time copy of the rollups.
func (a *Aggregator) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	saved := a.before - a.after
	var pct float64
	if a.before > 0 {
		pct = float64(saved) / float64(a.before) * 100
	}
	comps := make(map[string]compStat, len(a.perComp))
	var passthrough []string
	for k, v := range a.perComp {
		cs := *v
		if cs.SavedUnique > 0 {
			cs.OvercountRatio = float64(cs.Saved) / float64(cs.SavedUnique)
		}
		cs.seenKeys = nil // don't serialize the working set
		comps[k] = cs
		// Dead weight = ran but never changed the request at all (always skipped
		// or reverted). A component that mutated but saved no content tokens (e.g.
		// cacheinject adds provider cache_control) is NOT passthrough.
		if v.Runs > 0 && v.Mutated == 0 {
			passthrough = append(passthrough, k)
		}
	}
	sort.Strings(passthrough)
	addedAvg, upAvg, upAvgByp := 0.0, 0.0, 0.0
	if a.addedSamples > 0 {
		addedAvg = a.addedMs / float64(a.addedSamples)
	}
	if a.upstreamN > 0 {
		upAvg = a.upstreamMs / float64(a.upstreamN)
	}
	if a.upstreamNByp > 0 {
		upAvgByp = a.upstreamMsByp / float64(a.upstreamNByp)
	}
	attemptedPct := 0.0
	if a.attempted > 0 {
		attemptedPct = float64(saved) / float64(a.attempted) * 100
	}
	// Guard on the BILLED figure, not the sum: with no usage data the denominator
	// would be `saved` alone and the ratio would read ~100%. No data => report 0.
	newInputPct := 0.0
	if a.freshInput+a.cacheWrite > 0 {
		newInputPct = float64(saved) / float64(a.freshInput+a.cacheWrite+saved) * 100
	}
	return Snapshot{
		Requests: a.requests, TokensBefore: a.before, TokensAfter: a.after,
		SavedTokens: saved, SavingsPct: pct,
		WastedTokens: a.wasted, Bounces: a.bounces, AdjustedSaved: saved - a.wasted,
		Components: comps, TopPassthrough: passthrough,
		AddedLatencyMsAvg: addedAvg, UpstreamMsAvg: upAvg, UpstreamMsAvgBypassed: upAvgByp,
		FreshInputTokens: a.freshInput, CacheReadTokens: a.cacheRead,
		CacheWriteTokens: a.cacheWrite, OutputTokens: a.outputTok,
		AttemptedTokens: a.attempted, FrozenTokens: a.frozen,
		SavingsPctAttempted: attemptedPct, SavingsPctNewInput: newInputPct,
	}
}
