package metrics

import (
	"sort"
	"sync"
	"sync/atomic"
)

// Extraction metrics (issue #28 part F). The existing per-component stats answer "how
// many tokens did it save?", which for extract_llm is the wrong headline: it is the only
// component that spends money, so gross savings can look great while the component is
// underwater. /stats reported the tool's LLM cost in a SEPARATE field from savings, so
// nothing anywhere showed the component net-negative — the ~8x loss was invisible until
// someone divided two numbers by hand.
//
// NET-AFTER-COST is therefore the headline here, and the trigger reason is recorded per
// activation because an operator's first question about an expensive component is always
// "why did this run?".
//
// SCOPED PER COMPONENT (issue #176). These were process-global counters, and the comment
// here said so as a design statement — matching cheapmodel.Usage's scope, on the premise
// that "the LLM component in a config is extract". That premise stopped being true when
// the cold-transcript sweep became its own component: extract_llm and extract_llm_sweep
// both write these counters, so the `extract` block in /stats was the SUM of two
// components with opposite economics — one call on the request's own frontier model
// against per-output calls on a cheap one — presented under a name that reads as one of
// them. MEASURED (iteration 023, arm B): `calls: 101` and `avg_latency_ms: 59,009` were
// attributed to extract_llm while its own debug record reported `cands: 0` on all 374
// requests, i.e. it made no call at all; the 101 were very nearly the sweep's 96 asks.
// A latency and a net value were charged to the wrong component and an experiment's
// conclusion was drawn from it.
//
// So every accessor and every recorder is keyed by component name now, and the aggregate
// is DERIVED by summing them rather than maintained alongside them — two totals kept in
// parallel is how the second one drifts from the first.
//
// The latency accessors are keyed too, and that is a behaviour change, deliberately: the
// exploration brake in offload.tooSlowToExplore read the global p50, so the sweep's
// ~59-second asks braked extract_llm's exploration on evidence from a different component
// and a different model. A brake must read the latency of the calls it is braking.

// xCounters is one component's extraction accounting.
type xCounters struct {
	calls      atomic.Int64 // extraction LLM calls actually made
	cacheHits  atomic.Int64 // calls avoided by the global result cache
	suppressed atomic.Int64 // calls suppressed by the economic gate
	grossSaved atomic.Int64 // tokens removed (unique, first application only)
	latencyMs  atomic.Int64 // cumulative wall time in extraction calls
	lookups    atomic.Int64 // result-cache lookups (hits + misses), for the hit rate
	// valueNano is the realized dollar value of what was removed, in nanodollars, recorded
	// BY THE COMPONENT at the rate each removal was actually worth.
	//
	// It exists because the alternative — tokens x a constant rate chosen from the cache MODE
	// — was wrong by an order of magnitude in the one regime this component runs in. /stats
	// priced every saved token at the cache-READ rate and counted it once, while the component
	// itself values a cold-turn token at the cache-WRITE rate (12.5x more) and collects it
	// again on each replay. MEASURED: 44,073 tokens reported as $0.0132 of value against
	// $0.8291 of spend — a 63x loss — where the honest figure over the same data is between
	// 1.4x and 7.5x underwater. A metric that overstates a loss by 12x is as unusable as one
	// that hides it: both make the fix unattributable.
	//
	// Nanodollars so the accumulator can stay atomic; a call's value is ~1e-5 USD, so an
	// int64 of nanodollars holds ~9e9 USD of headroom.
	valueNano atomic.Int64
	// spendNano is what THIS component paid, summed from each call's own ModelCall.CostUSD —
	// which each component prices with the rates of the model it actually called.
	//
	// The alternative, and what /stats did before #176, was cheapmodel's process-global token
	// totals priced through one rate card. That figure is not this component's spend and is
	// not even extraction's: every cheap-model call in the process lands in it, `summarize`
	// and `agentdiet` included, and the sweep's calls go to the request's own frontier model
	// while the card is haiku's. So it over-attributed non-extraction spend to extraction and
	// mispriced the half of extraction that does not use the cheap model. Recorded per call
	// here, at the price the component itself computed.
	spendNano atomic.Int64

	reasonMu sync.Mutex
	reasons  map[string]int64 // trigger/suppression reason -> count

	// A ring of recent per-call latencies, for the MEDIAN. The mean cannot answer "are
	// calls slow?" on this workload: measured n=8 on one gateway, p50 3,748 ms against a
	// max of 11,663, and an 8-token no-op call spanned 1,490-15,800 ms. Latency here is
	// queue time, so one tail sample moves the mean past a brake the typical call is
	// nowhere near. A ring rather than a histogram because the only consumer is one
	// threshold comparison and 64 samples is already more evidence than the gate needs.
	latMu   sync.Mutex
	latRing [64]float64
	latN    int
}

// xReg holds one xCounters per component name. A map under a mutex rather than a sync.Map
// because the write path is one pointer lookup per recorded event and the read path is
// /stats; neither is hot enough to want the extra indirection.
var (
	xRegMu sync.RWMutex
	xReg   = map[string]*xCounters{}
)

// xFor returns (creating if needed) the counters for one component. An empty name is
// accepted and bucketed under "" rather than dropped: losing an event is worse than
// showing it under an unhelpful key, and the key itself then names the caller to fix.
func xFor(component string) *xCounters {
	xRegMu.RLock()
	c := xReg[component]
	xRegMu.RUnlock()
	if c != nil {
		return c
	}
	xRegMu.Lock()
	defer xRegMu.Unlock()
	if c = xReg[component]; c == nil {
		c = &xCounters{}
		xReg[component] = c
	}
	return c
}

// xComponents returns the registered component names, sorted, so snapshots are stable.
func xComponents() []string {
	xRegMu.RLock()
	defer xRegMu.RUnlock()
	names := make([]string, 0, len(xReg))
	for k := range xReg {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// latencySamples copies the retained latency ring.
func (x *xCounters) latencySamples() []float64 {
	x.latMu.Lock()
	defer x.latMu.Unlock()
	n := x.latN
	if n > len(x.latRing) {
		n = len(x.latRing)
	}
	cp := make([]float64, n)
	copy(cp, x.latRing[:n])
	return cp
}

// latencyP50 returns the median of the retained samples, and how many there are.
func latencyP50(samples []float64) (float64, int) {
	if len(samples) == 0 {
		return 0, 0
	}
	sort.Float64s(samples)
	return samples[len(samples)/2], len(samples)
}

// RecordExtractionCall notes one extraction LLM call and its wall time, for `component`.
func RecordExtractionCall(component string, latencyMs float64) {
	x := xFor(component)
	x.calls.Add(1)
	x.latencyMs.Add(int64(latencyMs))
	x.latMu.Lock()
	x.latRing[x.latN%len(x.latRing)] = latencyMs
	x.latN++
	x.latMu.Unlock()
}

// ExtractionP50LatencyMs returns the MEDIAN observed wall time per extraction call made by
// `component`, and the number of samples behind it. The gate reads this rather than the mean
// to decide whether speculative calls have become too slow to be worth their wall clock —
// see offload.tooSlowToExplore for the measurement that made the mean unusable here.
//
// PER COMPONENT (#176): reading the process-global median made one component's brake fire on
// another component's latency. extract_llm's per-output cheap-model calls and the sweep's
// single frontier-model ask differ by more than an order of magnitude, so the pooled median
// is not a fact about either.
func ExtractionP50LatencyMs(component string) (float64, int64) {
	p50, n := latencyP50(xFor(component).latencySamples())
	return p50, int64(n)
}

// RecordExtractionCacheLookup notes one global result-cache lookup and whether it hit.
// A hit is a call AVOIDED — the cheapest possible outcome, and the source of ~93% of the
// component's realized value in the Terminal-Bench measurement.
func RecordExtractionCacheLookup(component string, hit bool) {
	x := xFor(component)
	x.lookups.Add(1)
	if hit {
		x.cacheHits.Add(1)
	}
}

// ExtractionAvgLatencyMs returns the observed mean wall time per extraction call made by
// `component` and the number of calls it averages. The gate reads this to stop SPECULATIVE
// calls once they are observed to be slow — exploration spends wall clock as well as money,
// and an agent with a task deadline feels the former more (PR #37: 17.8s across 2 calls that
// saved 0 tokens).
func ExtractionAvgLatencyMs(component string) (float64, int64) {
	x := xFor(component)
	calls := x.calls.Load()
	if calls == 0 {
		return 0, 0
	}
	return float64(x.latencyMs.Load()) / float64(calls), calls
}

// RecordExtractionSuppressed notes that the economic gate declined a call, with its reason.
func RecordExtractionSuppressed(component, reason string) {
	xFor(component).suppressed.Add(1)
	RecordExtractionReason(component, reason)
}

// RecordExtractionReason counts one trigger/suppression reason.
func RecordExtractionReason(component, reason string) {
	if reason == "" {
		return
	}
	x := xFor(component)
	x.reasonMu.Lock()
	if x.reasons == nil {
		x.reasons = map[string]int64{}
	}
	x.reasons[reason]++
	x.reasonMu.Unlock()
}

// RecordExtractionSaving notes tokens removed by an accepted extraction (count each
// distinct compaction once — the caller dedups by content key).
func RecordExtractionSaving(component string, tokens int) {
	if tokens > 0 {
		xFor(component).grossSaved.Add(int64(tokens))
	}
}

// RecordExtractionValue notes the dollars one removal was worth, priced by the component at
// the rate that removal was actually billed at — the cache-write rate on a cold turn for the
// first application, the cache-read rate for each later replay of the frozen result.
//
// Called at BOTH sites deliberately: crediting only the first application under-reports by
// however much the replay is worth, and crediting the replays at the first application's rate
// over-reports it by 12.5x. The component is the only layer that knows which regime a request
// was in, so it is the layer that prices it.
func RecordExtractionValue(component string, usd float64) {
	if usd > 0 {
		xFor(component).valueNano.Add(int64(usd * 1e9))
	}
}

// RecordExtractionSpend notes what ONE call cost, priced by the component that made it with
// the rates of the model it actually addressed. See xCounters.spendNano for why /stats cannot
// derive this from cheapmodel's process totals.
func RecordExtractionSpend(component string, usd float64) {
	if usd > 0 {
		xFor(component).spendNano.Add(int64(usd * 1e9))
	}
}

// ExtractStats is the extraction economics block served inside /stats. It is ADDITIVE:
// every pre-existing /stats field keeps its name and meaning, because deploy/harbor/*.py
// parses them.
type ExtractStats struct {
	Calls            int64 `json:"calls"`            // extraction LLM calls made
	CallsAvoided     int64 `json:"calls_avoided"`    // global result-cache hits
	CallsSuppressed  int64 `json:"calls_suppressed"` // declined by the economic gate
	CacheLookups     int64 `json:"cache_lookups"`    //
	GrossSavedTokens int64 `json:"gross_saved_tokens"`

	// CacheHitRate is calls_avoided / cache_lookups.
	CacheHitRate float64 `json:"cache_hit_rate"`
	// AvgLatencyMs is mean wall time per extraction call — the component's latency cost.
	AvgLatencyMs float64 `json:"avg_latency_ms"`

	// PromptCacheReadTokens is the evidence for issue #28 part A. If this stays 0 while
	// calls climbs, the preamble's cache_control breakpoint is being SILENTLY IGNORED
	// (the prefix is below the model's minimum cacheable length) and the split is buying
	// nothing. Do not infer a cache win from the fact that a breakpoint was placed.
	PromptCacheReadTokens  int64 `json:"prompt_cache_read_tokens"`
	PromptCacheWriteTokens int64 `json:"prompt_cache_write_tokens"`

	// ExtractionCostUSD is what the component SPENT; GrossValueUSD is what its saved
	// tokens are WORTH at the rate they would actually have been billed; NetValueUSD is
	// the honest headline. Negative means the component is underwater and should be off.
	ExtractionCostUSD float64 `json:"extraction_cost_usd"`
	GrossValueUSD     float64 `json:"gross_value_usd"`
	NetValueUSD       float64 `json:"net_value_usd"`

	// Reasons counts why extraction ran or was suppressed, most frequent first.
	Reasons map[string]int64 `json:"reasons,omitempty"`
	// TopReason is the single most common reason — the one-line operator answer.
	TopReason string `json:"top_reason,omitempty"`

	// ByComponent breaks every field above down by the component that recorded it (#176).
	// The enclosing block stays the SUM, because deploy/harbor/*.py reads it and a rename or
	// a change of meaning there invalidates the reproduction path — but the sum is the figure
	// that misattributed a 59-second latency and a negative net value to a component that
	// made no calls, so it must never again be the only figure available. Nil inside each
	// nested entry: the breakdown does not nest.
	ByComponent map[string]*ExtractStats `json:"by_component,omitempty"`
}

// ExtractSnapshot builds the extraction stats.
//
// cost is the host's fallback figure for extraction spend, used only for components that
// recorded none of their own (a library embedding that never fills ModelCall.CostUSD).
// perSavedTokenUSD is the value of ONE saved token at the rate it would actually have been
// billed (cache-read vs fresh — the caller knows the traffic's cache-awareness); the value
// side is computed HERE, against each component's own GrossSavedTokens.
//
// Taking a RATE rather than a pre-computed total is deliberate. The obvious signature
// (grossValue float64) invites the caller to pass the pipeline-wide savings figure, which
// prices every other component's work (format, dedup, cmdfilter, extract, …) against
// extract_llm's cost and reports the component as POSITIVE when its own arithmetic says
// otherwise. That is the single number this whole issue exists to get right, so the
// signature makes the mistake impossible to express.
func ExtractSnapshot(cost, perSavedTokenUSD float64, cacheWrite, cacheRead int64) ExtractStats {
	names := xComponents()
	total := ExtractStats{
		PromptCacheReadTokens: cacheRead, PromptCacheWriteTokens: cacheWrite,
	}
	var (
		perComp   = make(map[string]*ExtractStats, len(names))
		grossVal  float64
		spend     float64
		totLatMs  int64
		totReason = map[string]int64{}
		anySpend  bool
	)
	for _, name := range names {
		x := xFor(name)
		s, gv, sp, recorded, touched, reasons := x.snapshot(perSavedTokenUSD)
		if !touched {
			continue // an entry created only by a counter LOOKUP; it recorded nothing
		}
		cp := s
		perComp[name] = &cp
		total.Calls += s.Calls
		total.CallsAvoided += s.CallsAvoided
		total.CallsSuppressed += s.CallsSuppressed
		total.CacheLookups += s.CacheLookups
		total.GrossSavedTokens += s.GrossSavedTokens
		grossVal += gv
		spend += sp
		anySpend = anySpend || recorded
		totLatMs += x.latencyMs.Load()
		for k, v := range reasons {
			totReason[k] += v
		}
	}
	// The components' own priced spend where any of them recorded some; the host's
	// process-global figure only as a fallback. See xCounters.spendNano.
	if !anySpend {
		spend = cost
	}
	total.ExtractionCostUSD = round4(spend)
	total.GrossValueUSD = round4(grossVal)
	total.NetValueUSD = round4(grossVal - spend)
	if total.CacheLookups > 0 {
		total.CacheHitRate = float64(total.CallsAvoided) / float64(total.CacheLookups)
	}
	if total.Calls > 0 {
		total.AvgLatencyMs = float64(totLatMs) / float64(total.Calls)
	}
	total.Reasons, total.TopReason = sortedReasons(totReason)
	if len(perComp) > 0 {
		total.ByComponent = perComp
	}
	return total
}

// snapshot renders one component's counters, plus the raw pieces the aggregate needs (its
// gross value and spend in dollars, whether it priced its own spend, whether it recorded
// anything at all, and its reason histogram) — returned rather than re-read, so the total and
// the per-component row are computed from ONE read of each atomic.
func (x *xCounters) snapshot(perSavedTokenUSD float64) (
	s ExtractStats, grossValue, spend float64, spendRecorded, touched bool, reasons map[string]int64,
) {
	calls := x.calls.Load()
	lookups := x.lookups.Load()
	hits := x.cacheHits.Load()
	gross := x.grossSaved.Load()
	// The component's own realized valuation wins where it has one: it knows each removal's
	// regime and counts the replays. perSavedTokenUSD stays the fallback for a host that
	// records nothing (library users, /compact), where one flat rate is all there is.
	grossValue = float64(gross) * perSavedTokenUSD
	if v := x.valueNano.Load(); v > 0 {
		grossValue = float64(v) / 1e9
	}
	if sp := x.spendNano.Load(); sp > 0 {
		spend, spendRecorded = float64(sp)/1e9, true
	}
	s = ExtractStats{
		Calls: calls, CallsAvoided: hits, CallsSuppressed: x.suppressed.Load(),
		CacheLookups: lookups, GrossSavedTokens: gross,
		ExtractionCostUSD: round4(spend), GrossValueUSD: round4(grossValue),
		NetValueUSD: round4(grossValue - spend),
	}
	if lookups > 0 {
		s.CacheHitRate = float64(hits) / float64(lookups)
	}
	if calls > 0 {
		s.AvgLatencyMs = float64(x.latencyMs.Load()) / float64(calls)
	}
	x.reasonMu.Lock()
	reasons = make(map[string]int64, len(x.reasons))
	for k, v := range x.reasons {
		reasons[k] = v
	}
	x.reasonMu.Unlock()
	s.Reasons, s.TopReason = sortedReasons(reasons)
	touched = calls > 0 || lookups > 0 || gross > 0 || s.CallsSuppressed > 0 ||
		len(reasons) > 0 || x.valueNano.Load() > 0 || x.spendNano.Load() > 0
	return s, grossValue, spend, spendRecorded, touched, reasons
}

// sortedReasons copies a reason histogram and names its mode. Nil map in, nil map out, so the
// omitempty on Reasons still elides the field.
func sortedReasons(in map[string]int64) (map[string]int64, string) {
	if len(in) == 0 {
		return nil, ""
	}
	out := make(map[string]int64, len(in))
	keys := make([]string, 0, len(in))
	for k, v := range in {
		out[k] = v
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if in[keys[i]] != in[keys[j]] {
			return in[keys[i]] > in[keys[j]]
		}
		return keys[i] < keys[j] // stable output for equal counts
	})
	return out, keys[0]
}

func round4(f float64) float64 {
	return float64(int64(f*10000+sign(f)*0.5)) / 10000
}

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}
