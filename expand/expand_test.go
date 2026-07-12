package expand

import (
	"testing"

	"github.com/kagenti/context-guru/store"
)

func TestToolDefShape(t *testing.T) {
	an := ToolDef("anthropic")
	if an["name"] != ToolName {
		t.Fatalf("anthropic tool def name=%v", an["name"])
	}
	if _, ok := an["input_schema"].(map[string]any)["properties"]; !ok {
		t.Fatalf("anthropic def missing input_schema.properties: %v", an)
	}
	oa := ToolDef("openai")
	if oa["type"] != "function" {
		t.Fatalf("openai def should be a function tool: %v", oa)
	}
	fn, ok := oa["function"].(map[string]any)
	if !ok || fn["name"] != ToolName || fn["parameters"] == nil {
		t.Fatalf("openai function shape wrong: %v", oa)
	}
}

func TestParseMarkersDistinctInOrder(t *testing.T) {
	got := ParseMarkers("a <<cg:K1>> b <<cg:K2>> c <<cg:K1>>")
	if len(got) != 2 || got[0] != "K1" || got[1] != "K2" {
		t.Fatalf("ParseMarkers=%v want [K1 K2] first-seen, deduped", got)
	}
	if ParseMarkers("no markers here") != nil {
		t.Fatal("text without markers must return nil")
	}
}

func TestResolve(t *testing.T) {
	st := store.NewMemory(store.Options{})
	if _, ok := Resolve(st, "absent"); ok {
		t.Fatal("absent key must miss")
	}
	st.Put("k", []byte("orig"))
	if v, ok := Resolve(st, "k"); !ok || v != "orig" {
		t.Fatalf("Resolve=%q ok=%v", v, ok)
	}
}

func TestContinuationFailsOpenOnBadShape(t *testing.T) {
	if _, ok := Continuation("anthropic", []byte(`{"messages":[]}`), []byte(`{}`), nil); ok {
		t.Fatal("anthropic response with no content must fail open (ok=false)")
	}
	if _, ok := Continuation("openai", []byte(`{"messages":[]}`), []byte(`{}`), nil); ok {
		t.Fatal("openai response with no message must fail open (ok=false)")
	}
}

func TestResponseCallsNoExpandCall(t *testing.T) {
	// A plain assistant answer with no tool calls => no expand calls, no other tools.
	calls, other := ResponseCalls("openai", []byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	if len(calls) != 0 || other {
		t.Fatalf("plain answer => no calls; got calls=%v other=%v", calls, other)
	}
}
