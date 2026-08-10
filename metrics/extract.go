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
// These are process-global counters, matching cheapmodel.Usage's existing scope.
var (
	xCalls      atomic.Int64 // extraction LLM calls actually made
	xCacheHits  atomic.Int64 // calls avoided by the global result cache
	xSuppressed atomic.Int64 // calls suppressed by the economic gate
	xGrossSaved atomic.Int64 // tokens removed (unique, first application only)
	xLatencyMs  atomic.Int64 // cumulative wall time in extraction calls
	xLookups    atomic.Int64 // result-cache lookups (hits + misses), for the hit rate

	xReasonMu sync.Mutex
	xReasons  = map[string]int64{} // trigger/suppression reason -> count
)

// RecordExtractionCall notes one extraction LLM call and its wall time.
func RecordExtractionCall(latencyMs float64) {
	xCalls.Add(1)
	xLatencyMs.Add(int64(latencyMs))
}

// RecordExtractionCacheLookup notes one global result-cache lookup and whether it hit.
// A hit is a call AVOIDED — the cheapest possible outcome, and the source of ~93% of the
// component's realized value in the Terminal-Bench measurement.
func RecordExtractionCacheLookup(hit bool) {
	xLookups.Add(1)
	if hit {
		xCacheHits.Add(1)
	}
}

// RecordExtractionSuppressed notes that the economic gate declined a call, with its reason.
func RecordExtractionSuppressed(reason string) {
	xSuppressed.Add(1)
	RecordExtractionReason(reason)
}

// RecordExtractionReason counts one trigger/suppression reason.
func RecordExtractionReason(reason string) {
	if reason == "" {
		return
	}
	xReasonMu.Lock()
	xReasons[reason]++
	xReasonMu.Unlock()
}

// RecordExtractionSaving notes tokens removed by an accepted extraction (count each
// distinct compaction once — the caller dedups by content key).
func RecordExtractionSaving(tokens int) {
	if tokens > 0 {
		xGrossSaved.Add(int64(tokens))
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
}

// ExtractSnapshot builds the extraction stats. cost is the component's own LLM spend and
// grossValue the dollar value of its saved tokens; both are computed by the caller, which
// is the layer that knows the model pricing and the cache-awareness of the traffic.
func ExtractSnapshot(cost, grossValue float64, cacheWrite, cacheRead int64) ExtractStats {
	calls := xCalls.Load()
	lookups := xLookups.Load()
	hits := xCacheHits.Load()

	s := ExtractStats{
		Calls: calls, CallsAvoided: hits, CallsSuppressed: xSuppressed.Load(),
		CacheLookups: lookups, GrossSavedTokens: xGrossSaved.Load(),
		PromptCacheReadTokens: cacheRead, PromptCacheWriteTokens: cacheWrite,
		ExtractionCostUSD: round4(cost), GrossValueUSD: round4(grossValue),
		NetValueUSD: round4(grossValue - cost),
	}
	if lookups > 0 {
		s.CacheHitRate = float64(hits) / float64(lookups)
	}
	if calls > 0 {
		s.AvgLatencyMs = float64(xLatencyMs.Load()) / float64(calls)
	}

	xReasonMu.Lock()
	if len(xReasons) > 0 {
		s.Reasons = make(map[string]int64, len(xReasons))
		keys := make([]string, 0, len(xReasons))
		for k, v := range xReasons {
			s.Reasons[k] = v
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if xReasons[keys[i]] != xReasons[keys[j]] {
				return xReasons[keys[i]] > xReasons[keys[j]]
			}
			return keys[i] < keys[j] // stable output for equal counts
		})
		s.TopReason = keys[0]
	}
	xReasonMu.Unlock()
	return s
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
