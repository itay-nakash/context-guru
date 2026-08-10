package apply

import "testing"

// The metadata-write exception must be exactly that: added cache_control keys and
// nothing else. Anything wider would put us back to splicing a lossy re-marshal
// over a message bifrost cannot round-trip.
func TestMetadataOnlyWritesRejectsNonMetadataChanges(t *testing.T) {
	pre := `{"role":"assistant","content":[{"type":"text","text":"hello"},{"type":"tool_use"}]}`
	cases := []struct {
		name, post string
		want       bool
	}{
		{"added cache_control", `{"role":"assistant","content":[{"type":"text","text":"hello"},{"type":"tool_use","cache_control":{"type":"ephemeral"}}]}`, true},
		{"two added", `{"role":"assistant","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}},{"type":"tool_use","cache_control":{"type":"ephemeral"}}]}`, true},
		{"no change", pre, false},
		{"text edited too", `{"role":"assistant","content":[{"type":"text","text":"HELLO"},{"type":"tool_use","cache_control":{"type":"ephemeral"}}]}`, false},
		{"block removed", `{"role":"assistant","content":[{"type":"tool_use","cache_control":{"type":"ephemeral"}}]}`, false},
		{"role changed", `{"role":"user","content":[{"type":"text","text":"hello"},{"type":"tool_use","cache_control":{"type":"ephemeral"}}]}`, false},
		{"string content", `{"role":"assistant","content":"hello"}`, false},
	}
	for _, tc := range cases {
		if _, ok := metadataOnlyWrites([]byte(pre), []byte(tc.post)); ok != tc.want {
			t.Errorf("%s: ok=%v want %v", tc.name, ok, tc.want)
		}
	}
}

// applyMetaWrites must refuse when the raw body's block layout disagrees with what
// the writes assume, so a key can never land on the wrong block.
func TestApplyMetaWritesRefusesOnShapeMismatch(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t"}]}]}`)
	w := []metaWrite{{path: "content.1.cache_control", raw: `{"type":"ephemeral"}`}}
	if _, ok := applyMetaWrites(body, "messages.0", 2, w); ok {
		t.Fatal("expected a refusal: the raw message has 1 block, the writes assume 2")
	}
	// Never overwrite a breakpoint the caller already set.
	set := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)
	w = []metaWrite{{path: "content.0.cache_control", raw: `{"type":"ephemeral"}`}}
	if _, ok := applyMetaWrites(set, "messages.0", 1, w); ok {
		t.Fatal("expected a refusal: the caller's own cache_control must not be overwritten")
	}
}

func TestBreakpointCounting(t *testing.T) {
	body := []byte(`{
		"system":[{"type":"text","cache_control":{"type":"ephemeral"}},{"type":"text","cache_control":{"type":"ephemeral"}},{"type":"text"}],
		"tools":[{"name":"x"},{"name":"y","cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"user","content":"a tool output that merely mentions cache_control in its text"},
			{"role":"assistant","content":[{"type":"tool_use","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","cache_control":{"type":"ephemeral"},"content":"q"}
		]}`)
	if got := wireBreakpoints(body); got != 5 {
		t.Errorf("wireBreakpoints = %d, want 5", got)
	}
}
