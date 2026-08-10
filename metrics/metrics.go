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

// FilterAct / FilterMiss forward cmdfilter's ledger to whichever tee'd emitters
// record it (so a Tee still satisfies components.FilterStatsSink).
func (t Tee) FilterAct(family, filter, contentKey string, saved int) {
	for _, e := range t {
		if s, ok := e.(components.FilterStatsSink); ok {
			s.FilterAct(family, filter, contentKey, saved)
		}
	}
}

func (t Tee) FilterMiss(selector string) {
	for _, e := range t {
		if s, ok := e.(components.FilterStatsSink); ok {
			s.FilterMiss(selector)
		}
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
	// SSE time-to-first-byte accounting: how long after the upstream call started the
	// client got its first response byte, split by whether we had to buffer the whole
	// stream to inspect it for an expand call. Buffering is the only thing that stops a
	// stream being a stream, so counting it makes that cost visible instead of inferred
	// (it used to be unconditional and unmeasured — issue #26).
	sseTTFBMs    float64
	sseTTFBMsBuf float64
	sseStreamed  int64
	sseBuffered  int64
	// cmdfilter's per-family / per-filter ledger, plus the selector-miss ledger that
	// makes the next filter to write data instead of guesswork.
	filterFam  map[string]*filterStat
	filterName map[string]*filterStat
	filterMiss map[string]int64
}

// filterStat is one cmdfilter family's or filter's ledger. SavedUnique dedups by
// content key exactly as compStat does — the agent re-sends history verbatim every
// turn, so the cumulative figure double-counts the same compaction.
type filterStat struct {
	Acts        int64 `json:"acts"`
	Saved       int64 `json:"saved_tokens"`
	SavedUnique int64 `json:"saved_tokens_unique"`

	seenKeys map[string]struct{} // content keys already counted (not serialized)
}

// maxMissKeys bounds the selector-miss ledger; output shapes are unbounded in
// principle. Once full we only keep counting selectors already tracked.
// ponytail: fixed cap, no eviction — first-seen wins. Swap for a count-min sketch if
// the ledger ever gets dominated by whatever arrived first.
const maxMissKeys = 200

// FilterAct implements components.FilterStatsSink: one applied cmdfilter filter.
func (a *Aggregator) FilterAct(family, filter, contentKey string, saved int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.filterFam == nil {
		a.filterFam, a.filterName = map[string]*filterStat{}, map[string]*filterStat{}
	}
	bump(a.filterFam, family, contentKey, saved)
	bump(a.filterName, filter, contentKey, saved)
}

func bump(m map[string]*filterStat, key, contentKey string, saved int) {
	fs := m[key]
	if fs == nil {
		fs = &filterStat{seenKeys: map[string]struct{}{}}
		m[key] = fs
	}
	fs.Acts++
	fs.Saved += int64(saved)
	if _, seen := fs.seenKeys[contentKey]; !seen {
		fs.seenKeys[contentKey] = struct{}{}
		fs.SavedUnique += int64(saved)
	}
}

// FilterMiss implements components.FilterStatsSink: a selector that matched nothing.
func (a *Aggregator) FilterMiss(selector string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.filterMiss == nil {
		a.filterMiss = map[string]int64{}
	}
	if _, known := a.filterMiss[selector]; !known && len(a.filterMiss) >= maxMissKeys {
		return
	}
	a.filterMiss[selector]++
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

// RecordSSE notes one streaming response: ms from issuing the upstream request to
// the first byte handed to the client, and whether the stream had to be fully
// buffered first (which turns TTFB into total-response time).
func (a *Aggregator) RecordSSE(ttfbMs float64, buffered bool) {
	a.mu.Lock()
	if buffered {
		a.sseTTFBMsBuf += ttfbMs
		a.sseBuffered++
	} else {
		a.sseTTFBMs += ttfbMs
		a.sseStreamed++
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
	// SSE streaming health (#26). SSEBuffered counts streams context-guru had to read
	// in full before the client saw a byte (to look for an expand tool call); those
	// requests lose streaming entirely, so their TTFB is reported separately. A high
	// buffered share on traffic that never expands is the regression to watch. All four
	// count once per CLIENT REQUEST, not per upstream round: a request that drove
	// several expand rounds waited for all of them.
	SSEStreamed  int64   `json:"sse_streamed"`
	SSEBuffered  int64   `json:"sse_buffered"`
	SSETTFBMsAvg float64 `json:"sse_ttfb_ms_avg"` // streamed-through requests: a real TTFB
	// SSETTFBMsAvgBuf is time-to-LAST-byte by construction, not a comparable TTFB: a
	// buffered response is read in full before the client is written to, so its first
	// byte cannot precede the buffer completing. Read it as "what buffering cost these
	// requests", not as a latency to compare against sse_ttfb_ms_avg.
	SSETTFBMsAvgBuf float64 `json:"sse_ttfb_ms_avg_buffered"`
	SSEBufferedPct  float64 `json:"sse_buffered_pct"`
	// cmdfilter attribution: which command FAMILIES pay off (builds/tests/iac/pkg/net),
	// which individual filters fire, and which output shapes matched no filter (the
	// backlog of filters worth writing). Additive fields — nothing above is renamed.
	CmdfilterFamilies map[string]filterStat `json:"cmdfilter_families,omitempty"`
	CmdfilterFilters  map[string]filterStat `json:"cmdfilter_filters,omitempty"`
	CmdfilterMisses   []SelectorMiss        `json:"cmdfilter_selector_misses,omitempty"`
}

// SelectorMiss is one output shape that matched no filter, with how often it appeared.
type SelectorMiss struct {
	Selector string `json:"selector"`
	Count    int64  `json:"count"`
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
	ttfb, ttfbBuf, bufPct := 0.0, 0.0, 0.0
	if a.sseStreamed > 0 {
		ttfb = a.sseTTFBMs / float64(a.sseStreamed)
	}
	if a.sseBuffered > 0 {
		ttfbBuf = a.sseTTFBMsBuf / float64(a.sseBuffered)
	}
	if n := a.sseStreamed + a.sseBuffered; n > 0 {
		bufPct = float64(a.sseBuffered) / float64(n) * 100
	}
	return Snapshot{
		Requests: a.requests, TokensBefore: a.before, TokensAfter: a.after,
		SavedTokens: saved, SavingsPct: pct,
		WastedTokens: a.wasted, Bounces: a.bounces, AdjustedSaved: saved - a.wasted,
		Components: comps, TopPassthrough: passthrough,
		AddedLatencyMsAvg: addedAvg, UpstreamMsAvg: upAvg, UpstreamMsAvgBypassed: upAvgByp,
		SSEStreamed: a.sseStreamed, SSEBuffered: a.sseBuffered,
		SSETTFBMsAvg: ttfb, SSETTFBMsAvgBuf: ttfbBuf, SSEBufferedPct: bufPct,
		CmdfilterFamilies: copyFilterStats(a.filterFam),
		CmdfilterFilters:  copyFilterStats(a.filterName),
		CmdfilterMisses:   topMisses(a.filterMiss, 20),
	}
}

func copyFilterStats(src map[string]*filterStat) map[string]filterStat {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]filterStat, len(src))
	for k, v := range src {
		fs := *v
		fs.seenKeys = nil // don't serialize the working set
		out[k] = fs
	}
	return out
}

// topMisses returns the n most frequent unmatched selectors, descending (ties by
// selector, so the output is deterministic).
func topMisses(src map[string]int64, n int) []SelectorMiss {
	if len(src) == 0 {
		return nil
	}
	out := make([]SelectorMiss, 0, len(src))
	for s, c := range src {
		out = append(out, SelectorMiss{Selector: s, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Selector < out[j].Selector
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
