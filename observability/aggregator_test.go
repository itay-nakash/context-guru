package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestAggregatorSnapshot(t *testing.T) {
	// A cost model with a known input rate for the model under test: $1 per MTok.
	a := NewAggregator(map[string]CostRate{
		"claude-haiku-4-5": {InputPerMTok: 1.0, OutputPerMTok: 5.0},
	})

	ctx := context.Background()
	a.Emit(ctx, Event{
		RequestModel: "claude-haiku-4-5",
		TokensBefore: 1000, TokensAfter: 600, TokensSaved: 400, Ratio: 0.6,
		CacheInject: true, Extracted: true, StageErrors: 0, LatencyMillis: 10,
	})
	a.Emit(ctx, Event{
		RequestModel: "claude-haiku-4-5",
		TokensBefore: 2000, TokensAfter: 1400, TokensSaved: 600, Ratio: 0.7,
		CacheInject: false, Extracted: true, StageErrors: 1, LatencyMillis: 30,
	})
	a.Emit(ctx, Event{
		RequestModel: "claude-haiku-4-5",
		TokensBefore: 1000, TokensAfter: 500, TokensSaved: 500, Ratio: 0.5,
		CacheInject: true, Extracted: false, StageErrors: 0, LatencyMillis: 20,
	})

	s := a.Snapshot()

	if s.Requests != 3 {
		t.Fatalf("Requests = %d, want 3", s.Requests)
	}
	if s.TokensBefore != 4000 {
		t.Fatalf("TokensBefore = %d, want 4000", s.TokensBefore)
	}
	if s.TokensAfter != 2500 {
		t.Fatalf("TokensAfter = %d, want 2500", s.TokensAfter)
	}
	if s.TokensSaved != 1500 {
		t.Fatalf("TokensSaved = %d, want 1500", s.TokensSaved)
	}
	// Reduction ratio = saved/before = 1500/4000 = 0.375 (fraction of input removed).
	if s.Ratio < 0.374 || s.Ratio > 0.376 {
		t.Fatalf("Ratio = %v, want ~0.375 (saved/before)", s.Ratio)
	}
	if s.CacheInjected != 2 {
		t.Fatalf("CacheInjected = %d, want 2", s.CacheInjected)
	}
	if s.Extracted != 2 {
		t.Fatalf("Extracted = %d, want 2", s.Extracted)
	}
	if s.StageErrors != 1 {
		t.Fatalf("StageErrors = %d, want 1", s.StageErrors)
	}
	// cost_saved = tokens_saved/1e6 * inputRate = 1500/1e6 * 1.0 = 0.0015
	if s.CostSavedUSD < 0.00149 || s.CostSavedUSD > 0.00151 {
		t.Fatalf("CostSavedUSD = %v, want ~0.0015", s.CostSavedUSD)
	}
	// p50 of {10,20,30} is 20; p95 should be >= p50 and <= max.
	if s.AddedLatencyP50Millis < 10 || s.AddedLatencyP50Millis > 30 {
		t.Fatalf("AddedLatencyP50Millis = %d, want in [10,30]", s.AddedLatencyP50Millis)
	}
	if s.AddedLatencyP95Millis < s.AddedLatencyP50Millis {
		t.Fatalf("p95 (%d) < p50 (%d)", s.AddedLatencyP95Millis, s.AddedLatencyP50Millis)
	}
}

func TestAggregatorWriteJSONAndSummary(t *testing.T) {
	a := NewAggregator(nil)
	a.Emit(context.Background(), Event{
		RequestModel: "x", TokensBefore: 100, TokensAfter: 60, TokensSaved: 40, Ratio: 0.6, LatencyMillis: 5,
	})

	var buf bytes.Buffer
	if err := a.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var s Snapshot
	if err := json.Unmarshal(buf.Bytes(), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.TokensSaved != 40 {
		t.Fatalf("round-trip TokensSaved = %d, want 40", s.TokensSaved)
	}
	if a.Summary() == "" {
		t.Fatal("Summary() empty")
	}
}

// Snapshot uses the default cost rate when a model is unknown.
func TestAggregatorDefaultCostRate(t *testing.T) {
	a := NewAggregator(map[string]CostRate{
		DefaultCostKey: {InputPerMTok: 2.0},
	})
	a.Emit(context.Background(), Event{
		RequestModel: "unknown-model", TokensBefore: 1_000_000, TokensAfter: 0, TokensSaved: 1_000_000, Ratio: 0,
	})
	s := a.Snapshot()
	// 1e6/1e6 * 2.0 = 2.0
	if s.CostSavedUSD < 1.99 || s.CostSavedUSD > 2.01 {
		t.Fatalf("CostSavedUSD = %v, want ~2.0", s.CostSavedUSD)
	}
}
