package surfaces

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kagenti/lab-context-engineering/canon"
)

// jsonEqual compares two JSON byte slices for semantic (not byte) equality.
func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshal a: %v", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshal b: %v", err)
	}
	return reflect.DeepEqual(av, bv)
}

func TestAnthropicRoundTrip(t *testing.T) {
	body := []byte(`{"model":"claude-x","max_tokens":1024,"system":"be terse","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	s := Anthropic{}
	req, token, err := s.ToInternal(body)
	if err != nil {
		t.Fatalf("ToInternal: %v", err)
	}
	if token != nil {
		t.Fatalf("anthropic token should be nil, got %v", token)
	}
	out, err := s.Render(req, token)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !jsonEqual(t, body, out) {
		t.Fatalf("round-trip changed the request:\n in: %s\nout: %s", body, out)
	}
}

func TestAnthropicPreservesUnknownFields(t *testing.T) {
	body := []byte(`{"model":"m","temperature":0.2,"stream":true,"metadata":{"user_id":"u1"},"messages":[]}`)
	req, _, err := Anthropic{}.ToInternal(body)
	if err != nil {
		t.Fatalf("ToInternal: %v", err)
	}
	out, _ := req.Encode()
	if !jsonEqual(t, body, out) {
		t.Fatalf("unknown fields lost:\n in: %s\nout: %s", body, out)
	}
}

func TestOpenAIToInternalShape(t *testing.T) {
	body := []byte(`{
		"model":"gpt-x",
		"messages":[
			{"role":"system","content":"sys"},
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"", "tool_calls":[{"id":"call_1","function":{"name":"read","arguments":"{\"path\":\"a.go\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"FILE CONTENTS"}
		]
	}`)
	req, token, err := OpenAI{}.ToInternal(body)
	if err != nil {
		t.Fatalf("ToInternal: %v", err)
	}
	if req.Model() != "gpt-x" {
		t.Fatalf("model = %q", req.Model())
	}
	if s, _ := req.Root["system"].(string); s != "sys" {
		t.Fatalf("system = %q", s)
	}
	msgs := req.Messages()
	if len(msgs) != 3 { // user, assistant(tool_use), user(tool_result)
		t.Fatalf("got %d canonical messages, want 3", len(msgs))
	}
	// assistant message should carry a tool_use block with parsed input.
	asst := msgs[1]
	blocks := canon.Blocks(asst)
	var foundToolUse bool
	for _, b := range blocks {
		if canon.BlockType(b) == "tool_use" {
			foundToolUse = true
			input, _ := b["input"].(map[string]any)
			if input["path"] != "a.go" {
				t.Fatalf("tool_use input = %v", b["input"])
			}
		}
	}
	if !foundToolUse {
		t.Fatalf("no tool_use block in assistant message: %v", blocks)
	}
	if token == nil {
		t.Fatalf("openai token must not be nil")
	}
}

func TestOpenAIRenderWritesBackToolResult(t *testing.T) {
	body := []byte(`{
		"model":"gpt-x",
		"messages":[
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"name":"read","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ORIGINAL LONG OUTPUT"}
		]
	}`)
	req, token, err := OpenAI{}.ToInternal(body)
	if err != nil {
		t.Fatalf("ToInternal: %v", err)
	}
	// Simulate a stage reducing the tool_result content.
	for _, m := range req.Messages() {
		for _, b := range canon.Blocks(m) {
			if canon.BlockType(b) == "tool_result" {
				b["content"] = "REDUCED"
			}
		}
	}
	out, err := OpenAI{}.Render(req, token)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var rendered map[string]any
	if err := json.Unmarshal(out, &rendered); err != nil {
		t.Fatalf("unmarshal rendered: %v", err)
	}
	msgs := rendered["messages"].([]any)
	toolMsg := msgs[1].(map[string]any)
	if toolMsg["content"] != "REDUCED" {
		t.Fatalf("tool result not written back: %v", toolMsg["content"])
	}
	// The assistant message (not a reduction target) must be untouched.
	asst := msgs[0].(map[string]any)
	if _, ok := asst["tool_calls"]; !ok {
		t.Fatalf("assistant tool_calls were dropped")
	}
}

func TestGeminiUnsupportedIsFailOpen(t *testing.T) {
	_, _, err := Gemini{}.ToInternal([]byte(`{"contents":[]}`))
	if err != ErrUnsupported {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
}
