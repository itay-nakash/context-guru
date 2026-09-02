package metrics

import (
	"encoding/json"
	"testing"

	"github.com/rossoctl/context-guru/components"
)

// REGRESSION (#176). The extraction counters were process-global, so the `extract` block in
// /stats was the SUM of extract_llm and extract_llm_sweep under a name that reads as one of
// them. MEASURED (iteration 023, arm B): 101 `calls` at 59,009 ms mean latency and a net value
// of -$1.162 were read as extract_llm's, while extract_llm's own debug record reported zero
// surviving candidates on all 374 requests and the sweep had made 96 asks. A per-component
// latency and cost claim from a pipeline containing both was unsafe, and an experiment's
// conclusion was drawn from one.
//
// The reproduction below is that shape in miniature: the sweep makes the expensive calls, the
// tail pass makes none and only replays. If the two are pooled, extract_llm shows the sweep's
// calls and the sweep's latency.
func TestExtractStatsAreScopedPerComponent(t *testing.T) {
	resetExtract()
	// extract_llm: no calls at all. Only frozen replays, which cost nothing and are worth
	// something, so it records value without ever recording a call.
	RecordExtractionValue("extract_llm", 0.004)
	RecordExtractionCacheLookup("extract_llm", true)
	// extract_llm_sweep: the expensive component. Two very slow asks.
	RecordExtractionCall("extract_llm_sweep", 58_000)
	RecordExtractionCall("extract_llm_sweep", 60_018)
	RecordExtractionSpend("extract_llm_sweep", 1.166)
	RecordExtractionSaving("extract_llm_sweep", 900)

	s := ExtractSnapshot(0, 0.30/1e6, 0, 0)
	if s.ByComponent == nil {
		t.Fatal("no per-component breakdown: the pooled figure is the only one available again")
	}
	tail, ok := s.ByComponent["extract_llm"]
	if !ok {
		t.Fatal("extract_llm missing from the breakdown")
	}
	sweep, ok := s.ByComponent["extract_llm_sweep"]
	if !ok {
		t.Fatal("extract_llm_sweep missing from the breakdown")
	}
	// THE DEFECT: extract_llm made no call, so nothing may credit it with one, and its mean
	// latency is not 59 seconds — it has no latency at all.
	if tail.Calls != 0 {
		t.Errorf("extract_llm credited with %d calls; it made none (this is #176)", tail.Calls)
	}
	if tail.AvgLatencyMs != 0 {
		t.Errorf("extract_llm avg_latency_ms = %v; it made no calls to be slow",
			tail.AvgLatencyMs)
	}
	if sweep.Calls != 2 {
		t.Errorf("extract_llm_sweep calls = %d, want 2", sweep.Calls)
	}
	if sweep.AvgLatencyMs != 59_009 {
		t.Errorf("extract_llm_sweep avg_latency_ms = %v, want 59009", sweep.AvgLatencyMs)
	}
	// And the money lands on the component that spent it. extract_llm is net POSITIVE (free
	// replays), the sweep net NEGATIVE — the pooled block reported one negative figure and
	// attributed it to the wrong one.
	if tail.NetValueUSD <= 0 {
		t.Errorf("extract_llm net = %v; free replays worth $0.004 cannot be underwater",
			tail.NetValueUSD)
	}
	if sweep.NetValueUSD >= 0 {
		t.Errorf("extract_llm_sweep net = %v; it spent $1.166 to save 900 tokens",
			sweep.NetValueUSD)
	}
	// The enclosing block stays the SUM — deploy/harbor/*.py parses it — so the fix must be
	// additive, not a re-scoping of the existing keys.
	if s.Calls != 2 {
		t.Errorf("pooled calls = %d, want 2 (the block stays the sum for the harness)", s.Calls)
	}
}

// The latency BRAKE reads these counters to decide whether speculative calls are still worth
// their wall clock (offload.tooSlowToExplore). Pooled, the sweep's ~59-second frontier-model
// asks braked extract_llm's cheap-model exploration on evidence from a different component and
// a different model — a decision path, not just a display.
func TestLatencyAccessorsAreScopedPerComponent(t *testing.T) {
	resetExtract()
	RecordExtractionCall("extract_llm_sweep", 59_000)
	RecordExtractionCall("extract_llm_sweep", 59_000)
	RecordExtractionCall("extract_llm", 900)

	if p50, n := ExtractionP50LatencyMs("extract_llm"); p50 != 900 || n != 1 {
		t.Errorf("extract_llm p50 = (%v,%d), want (900,1) — it must not see the sweep's asks",
			p50, n)
	}
	if avg, n := ExtractionAvgLatencyMs("extract_llm"); avg != 900 || n != 1 {
		t.Errorf("extract_llm mean = (%v,%d), want (900,1)", avg, n)
	}
	if p50, n := ExtractionP50LatencyMs("extract_llm_sweep"); p50 != 59_000 || n != 2 {
		t.Errorf("extract_llm_sweep p50 = (%v,%d), want (59000,2)", p50, n)
	}
}

// Extraction spend must come from the component that priced it, not from cheapmodel's
// process-global token totals through one rate card. Those totals include `summarize` and
// `agentdiet`, and price the sweep's frontier-model asks at the cheap model's rates.
func TestRecordedSpendBeatsTheHostsGlobalFigure(t *testing.T) {
	resetExtract()
	RecordExtractionSpend("extract_llm", 0.02)
	RecordExtractionSpend("extract_llm_sweep", 0.30)
	// The host offers $9.99 — every cheap-model call in the process, extraction or not.
	s := ExtractSnapshot(9.99, 0.30/1e6, 0, 0)
	if s.ExtractionCostUSD != 0.32 {
		t.Errorf("extraction_cost_usd = %v, want 0.32 (the components' own priced spend)",
			s.ExtractionCostUSD)
	}
	if s.ByComponent["extract_llm"].ExtractionCostUSD != 0.02 {
		t.Errorf("extract_llm cost = %v, want 0.02",
			s.ByComponent["extract_llm"].ExtractionCostUSD)
	}
	// A host that records nothing (a library embedding, /compact) still gets a figure.
	resetExtract()
	RecordExtractionCall("extract_llm", 100)
	if s := ExtractSnapshot(9.99, 0.30/1e6, 0, 0); s.ExtractionCostUSD != 9.99 {
		t.Errorf("with no recorded spend the host's figure must stand, got %v",
			s.ExtractionCostUSD)
	}
}

// The breakdown has to survive JSON, because /stats is the only place anyone reads it and a
// field can be computed correctly and dropped by the encoder.
func TestByComponentSurvivesJSON(t *testing.T) {
	resetExtract()
	RecordExtractionCall("extract_llm_sweep", 59_000)
	b, err := json.Marshal(ExtractSnapshot(0.1, 0.30/1e6, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	by, ok := m["by_component"].(map[string]any)
	if !ok {
		t.Fatalf("by_component missing from the encoded block: %s", b)
	}
	row, ok := by["extract_llm_sweep"].(map[string]any)
	if !ok {
		t.Fatalf("extract_llm_sweep row missing: %s", b)
	}
	if row["avg_latency_ms"] != 59_000.0 {
		t.Errorf("avg_latency_ms = %v in the encoded row, want 59000", row["avg_latency_ms"])
	}
	// It must not recurse: a nested row carrying its own breakdown would be an infinite
	// document, and the omitempty is what prevents it.
	if _, nested := row["by_component"]; nested {
		t.Error("the breakdown nests inside itself")
	}
	// And it must be OMITTED when nothing recorded, so a parser that does not know the key
	// sees no change at all.
	resetExtract()
	b2, _ := json.Marshal(ExtractSnapshot(0, 0.30/1e6, 0, 0))
	var m2 map[string]any
	_ = json.Unmarshal(b2, &m2)
	if _, present := m2["by_component"]; present {
		t.Errorf("by_component must be omitted when empty: %s", b2)
	}
}

// REGRESSION (#176, second half). `acted` is `Saved() > 0`, and a frozen decision replayed on a
// later turn saves tokens for free — so 2,291 replays and a handful of paid extractions landed
// in one counter. The measured snapshot showed `acted: 239` on a component whose own record
// proved it made no call, and that number was read as 239 paid extractions.
func TestActedSeparatesFreeReplaysFromPaidWork(t *testing.T) {
	a := NewAggregator()
	// Three requests that only REPLAYED a decision frozen on an earlier turn: tokens saved,
	// nothing spent, no model call.
	for i := 0; i < 3; i++ {
		r := components.Report{Component: "extract_llm", Kind: "offload",
			TokensBefore: 10_000, TokensAfter: 6_000}
		r.Replay("reapplied_same_session")
		a.Component(r)
	}
	// One request that actually paid for a call.
	a.Component(components.Report{Component: "extract_llm", Kind: "offload",
		TokensBefore: 10_000, TokensAfter: 6_000,
		Calls: []components.ModelCall{{Component: "extract_llm", Model: "haiku", CostUSD: 0.01}}})

	snap := a.Snapshot()
	cs, ok := snap.Components["extract_llm"]
	if !ok {
		t.Fatal("extract_llm missing from the rollup")
	}
	if cs.Acted != 4 {
		t.Fatalf("acted = %d, want 4 (the pre-existing key keeps its meaning)", cs.Acted)
	}
	if cs.ActedReplay != 3 {
		t.Errorf("acted_replay = %d, want 3 — free replays counted as paid work (this is #176)",
			cs.ActedReplay)
	}
	if cs.ActedFresh != 1 {
		t.Errorf("acted_fresh = %d, want 1", cs.ActedFresh)
	}
	if cs.ActedFresh+cs.ActedReplay != cs.Acted {
		t.Errorf("the split must partition acted: %d + %d != %d",
			cs.ActedFresh, cs.ActedReplay, cs.Acted)
	}
	// A deterministic component does fresh work every run and pays no model for it; it must
	// not read as replaying.
	a.Component(components.Report{Component: "dedup", Kind: "offload",
		TokensBefore: 100, TokensAfter: 40})
	if d := a.Snapshot().Components["dedup"]; d.ActedFresh != 1 || d.ActedReplay != 0 {
		t.Errorf("dedup: fresh=%d replay=%d, want 1/0", d.ActedFresh, d.ActedReplay)
	}
}
