package metrics

import (
	"testing"

	"github.com/kagenti/context-guru/components"
)

func TestAggregatorHonestMetrics(t *testing.T) {
	a := NewAggregator()
	a.Component(components.Report{Component: "dedup", TokensBefore: 100, TokensAfter: 60}) // saved 40, acted
	a.Component(components.Report{Component: "format", TokensBefore: 50, TokensAfter: 50, Skipped: true})
	a.Run(components.RunReport{TokensBefore: 100, TokensAfter: 60})
	a.RecordExpand(25) // 25 tokens had to be re-served

	s := a.Snapshot()
	if s.SavedTokens != 40 {
		t.Fatalf("saved=%d want 40", s.SavedTokens)
	}
	if s.WastedTokens != 25 || s.Bounces != 1 {
		t.Fatalf("wasted=%d bounces=%d want 25/1", s.WastedTokens, s.Bounces)
	}
	if s.AdjustedSaved != 15 {
		t.Fatalf("adjusted=%d want 15 (40-25)", s.AdjustedSaved)
	}
	// format never saved a token -> flagged; dedup did -> not flagged.
	if len(s.TopPassthrough) != 1 || s.TopPassthrough[0] != "format" {
		t.Fatalf("top_passthrough=%v want [format]", s.TopPassthrough)
	}
	if s.Components["dedup"].Acted != 1 {
		t.Fatalf("dedup should have acted once, got %+v", s.Components["dedup"])
	}
}

// TestMutatedZeroSavingsNotPassthrough locks the fix for cacheinject-style
// components: they change the request (add cache_control) but save no content
// tokens, so they must NOT be flagged as dead weight in top_passthrough.
func TestMutatedZeroSavingsNotPassthrough(t *testing.T) {
	a := NewAggregator()
	// cacheinject: ran, not skipped, not reverted, but 0 content tokens saved.
	a.Component(components.Report{Component: "cacheinject", Kind: "reformat", TokensBefore: 100, TokensAfter: 100})
	// skeleton: always skipped this workload -> genuine dead weight.
	a.Component(components.Report{Component: "skeleton", Kind: "offload", TokensBefore: 100, TokensAfter: 100, Skipped: true})

	s := a.Snapshot()
	if len(s.TopPassthrough) != 1 || s.TopPassthrough[0] != "skeleton" {
		t.Fatalf("top_passthrough=%v want [skeleton] (cacheinject mutated, not dead weight)", s.TopPassthrough)
	}
	if s.Components["cacheinject"].Mutated != 1 {
		t.Fatalf("cacheinject should record a mutation, got %+v", s.Components["cacheinject"])
	}
}
