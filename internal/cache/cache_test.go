package cache

import (
	"testing"

	"github.com/kagenti/lab-context-engineering/canon"
)

func TestInjectAddsBreakpointsAndRespectsCap(t *testing.T) {
	req, _ := canon.Decode([]byte(`{
		"system":"you are helpful",
		"tools":[{"name":"read"}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"a"}]},
			{"role":"assistant","content":[{"type":"text","text":"b"}]},
			{"role":"user","content":[{"type":"text","text":"c"}]},
			{"role":"assistant","content":[{"type":"text","text":"d"}]}
		]
	}`))
	Inject(req.Root, 4, 2, -1, true)

	if got := countBreakpoints(req.Root); got == 0 || got > maxBreakpoints {
		t.Fatalf("breakpoints = %d, want 1..%d", got, maxBreakpoints)
	}
	// tools and system should each be marked.
	if !hasCacheControl(req.Root["tools"].([]any)[0]) {
		t.Fatal("tools breakpoint missing")
	}
	if _, ok := req.Root["system"].([]any); !ok {
		t.Fatal("string system should be normalised to a marked block list")
	}

	// Re-injecting must not exceed the cap (idempotent-ish, never over 4).
	Inject(req.Root, 4, 2, -1, true)
	if got := countBreakpoints(req.Root); got > maxBreakpoints {
		t.Fatalf("breakpoints = %d exceeds cap", got)
	}
}

func TestFloorIndex(t *testing.T) {
	req, _ := canon.Decode([]byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]},
		{"role":"assistant","content":[{"type":"text","text":"b"}]}
	]}`))
	if got := FloorIndex(req.Root); got != 0 {
		t.Fatalf("FloorIndex = %d, want 0", got)
	}
	empty, _ := canon.Decode([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`))
	if got := FloorIndex(empty.Root); got != -1 {
		t.Fatalf("FloorIndex (no cache) = %d, want -1", got)
	}
}
