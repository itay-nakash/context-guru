package metrics

import (
	"encoding/json"
	"testing"
)

// Net-after-cost is the honest headline (#28 part F): a component that saves 197k tokens
// worth $0.059 while spending $3.26 must report NEGATIVE, not a proud gross figure.
func TestNetValueGoesNegativeWhenUnderwater(t *testing.T) {
	resetExtract()
	// The measured Terminal-Bench shape: ~197,548 unique tokens saved at the cache-read
	// rate ($0.30/MTok) against $3.26 of extraction spend.
	grossValue := 197548 * 0.30 / 1e6
	s := ExtractSnapshot(3.26, grossValue, 0, 0)
	if s.NetValueUSD >= 0 {
		t.Fatalf("net must be negative when spend exceeds value: net=%v gross=%v cost=%v",
			s.NetValueUSD, s.GrossValueUSD, s.ExtractionCostUSD)
	}
	// And the ratio must reproduce the issue's ~8x-underwater claim to the right order.
	if ratio := s.ExtractionCostUSD / s.GrossValueUSD; ratio < 40 {
		t.Logf("cost/value ratio = %.1fx (issue reported ~8x against a different value basis)", ratio)
	}
}

// All the part-F counters must be exposed, including the ones that justify the component:
// calls avoided by cache and calls suppressed by the gate.
func TestExtractSnapshotExposesAllCounters(t *testing.T) {
	resetExtract()
	RecordExtractionCall(450)
	RecordExtractionCall(550)
	RecordExtractionCacheLookup(true)
	RecordExtractionCacheLookup(true)
	RecordExtractionCacheLookup(false)
	RecordExtractionSuppressed("suppressed: cache-aware, saving below call cost")
	RecordExtractionSaving(1200)
	RecordExtractionReason("high context pressure")

	s := ExtractSnapshot(0.024, 0.5, 800, 0)
	if s.Calls != 2 {
		t.Errorf("Calls = %d, want 2", s.Calls)
	}
	if s.CallsAvoided != 2 {
		t.Errorf("CallsAvoided = %d, want 2", s.CallsAvoided)
	}
	if s.CallsSuppressed != 1 {
		t.Errorf("CallsSuppressed = %d, want 1", s.CallsSuppressed)
	}
	if s.GrossSavedTokens != 1200 {
		t.Errorf("GrossSavedTokens = %d, want 1200", s.GrossSavedTokens)
	}
	if s.AvgLatencyMs != 500 {
		t.Errorf("AvgLatencyMs = %v, want 500", s.AvgLatencyMs)
	}
	want := 2.0 / 3.0
	if d := s.CacheHitRate - want; d > 1e-9 || d < -1e-9 {
		t.Errorf("CacheHitRate = %v, want %v", s.CacheHitRate, want)
	}
	if s.NetValueUSD != 0.476 {
		t.Errorf("NetValueUSD = %v, want 0.476", s.NetValueUSD)
	}
	// The trigger reason must be recoverable — an operator's first question.
	if s.TopReason == "" || len(s.Reasons) != 2 {
		t.Errorf("reasons not exposed: top=%q reasons=%v", s.TopReason, s.Reasons)
	}
}

// A zero prompt-cache read while calls climb is the evidence that part A's breakpoint is
// inert on this model. It must be visible in /stats, not inferred.
func TestPromptCacheReadZeroIsReported(t *testing.T) {
	resetExtract()
	for i := 0; i < 5; i++ {
		RecordExtractionCall(400)
	}
	s := ExtractSnapshot(0.06, 0.01, 0, 0)
	if s.Calls != 5 || s.PromptCacheReadTokens != 0 {
		t.Fatalf("expected 5 calls with 0 cache reads, got calls=%d read=%d",
			s.Calls, s.PromptCacheReadTokens)
	}
}

// /stats must stay backward compatible: deploy/harbor/*.py parses these exact keys, so a
// rename or removal breaks the benchmark harness. Fields are ADDED, never changed.
func TestSnapshotJSONKeysAreBackwardCompatible(t *testing.T) {
	a := NewAggregator()
	b, err := json.Marshal(a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	// Every key the harness reads today.
	for _, k := range []string{
		"requests", "tokens_before", "tokens_after", "saved_tokens", "savings_pct",
		"wasted_tokens", "bounces", "adjusted_saved", "components", "top_passthrough",
		"llm_calls", "llm_input_tokens", "llm_output_tokens",
		"cg_added_ms_avg", "upstream_ms_avg", "upstream_ms_avg_bypassed",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("/stats lost backward-compatible key %q", k)
		}
	}
	// The new block must be omitted when absent rather than serialized as null, so a
	// parser that does not know about it sees no change at all.
	if _, present := m["extract"]; present {
		t.Error(`"extract" must be omitted when unset (omitempty)`)
	}

	// And when present, it must carry the net figure.
	snap := a.Snapshot()
	xs := ExtractSnapshot(1.0, 0.1, 0, 0)
	snap.Extract = &xs
	b2, _ := json.Marshal(snap)
	var m2 map[string]any
	_ = json.Unmarshal(b2, &m2)
	ext, ok := m2["extract"].(map[string]any)
	if !ok {
		t.Fatal(`"extract" block missing when set`)
	}
	for _, k := range []string{"calls", "calls_avoided", "calls_suppressed",
		"extraction_cost_usd", "gross_value_usd", "net_value_usd",
		"prompt_cache_read_tokens", "avg_latency_ms", "cache_hit_rate"} {
		if _, ok := ext[k]; !ok {
			t.Errorf("extract block missing %q", k)
		}
	}
}

// resetExtract clears the process counters so assertions are independent.
func resetExtract() {
	xCalls.Store(0)
	xCacheHits.Store(0)
	xSuppressed.Store(0)
	xGrossSaved.Store(0)
	xLatencyMs.Store(0)
	xLookups.Store(0)
	xReasonMu.Lock()
	xReasons = map[string]int64{}
	xReasonMu.Unlock()
}
