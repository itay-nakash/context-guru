package expand

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func toolNames(t *testing.T, body []byte, provider string) []string {
	field := "function.name"
	if provider == "anthropic" {
		field = "name"
	}
	var out []string
	for _, tl := range gjson.GetBytes(body, "tools").Array() {
		out = append(out, tl.Get(field).String())
	}
	return out
}

func TestInjectOpenAIAddsToolLast(t *testing.T) {
	body := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"read_file"}}],"messages":[]}`)
	out, injected := Inject("openai", InjectAuto, body, true)
	if !injected {
		t.Fatal("expected injection when tools present + store persists")
	}
	names := toolNames(t, out, "openai")
	if len(names) != 2 || names[0] != "read_file" || names[1] != ToolName {
		t.Fatalf("expand tool must be appended LAST, got %v", names)
	}
}

func TestInjectAnthropicAddsToolLast(t *testing.T) {
	body := []byte(`{"model":"m","tools":[{"name":"bash","input_schema":{}}],"messages":[]}`)
	out, injected := Inject("anthropic", InjectAuto, body, true)
	if !injected {
		t.Fatal("expected injection")
	}
	names := toolNames(t, out, "anthropic")
	if len(names) != 2 || names[1] != ToolName {
		t.Fatalf("expand tool must be last, got %v", names)
	}
}

func TestInjectIdempotent(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","function":{"name":"x"}}]}`)
	once, _ := Inject("openai", InjectAuto, body, true)
	twice, injected := Inject("openai", InjectAuto, once, true)
	if injected {
		t.Fatal("second inject must be a no-op")
	}
	if !bytes.Equal(once, twice) {
		t.Fatal("idempotent inject must return byte-identical body")
	}
}

func TestInjectDeterministicBytes(t *testing.T) {
	// Byte-stable across calls (prefix-cache stability).
	base := []byte(`{"tools":[{"type":"function","function":{"name":"a"}}]}`)
	a, _ := Inject("openai", InjectAuto, base, true)
	b, _ := Inject("openai", InjectAuto, base, true)
	if !bytes.Equal(a, b) {
		t.Fatal("injected bytes must be deterministic")
	}
	// And the tool def itself is valid JSON with a stable shape.
	if !json.Valid(ToolDefRaw("openai")) || !json.Valid(ToolDefRaw("anthropic")) {
		t.Fatal("ToolDefRaw must be valid JSON")
	}
}

func TestInjectAutoSkipsWhenNoTools(t *testing.T) {
	body := []byte(`{"model":"m","messages":[]}`)
	_, injected := Inject("openai", InjectAuto, body, true)
	if injected {
		t.Fatal("auto must NOT inject when the request declares no tools")
	}
}

func TestInjectAlwaysCreatesToolsArray(t *testing.T) {
	body := []byte(`{"model":"m","messages":[]}`)
	out, injected := Inject("openai", InjectAlways, body, true)
	if !injected {
		t.Fatal("always must inject even with no tools")
	}
	if got := toolNames(t, out, "openai"); len(got) != 1 || got[0] != ToolName {
		t.Fatalf("always must create tools=[expand], got %v", got)
	}
}

func TestInjectNeverAndNoStore(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","function":{"name":"x"}}]}`)
	if _, in := Inject("openai", InjectNever, body, true); in {
		t.Fatal("never must not inject")
	}
	if _, in := Inject("openai", InjectAuto, body, false); in {
		t.Fatal("must not inject when store cannot persist (nothing to expand)")
	}
}

func TestInjectRespectsForcingToolChoice(t *testing.T) {
	// OpenAI required, and a specific forced function, and none: all skip.
	for _, tc := range []string{`"required"`, `"none"`, `{"type":"function","function":{"name":"x"}}`} {
		body := []byte(`{"tools":[{"type":"function","function":{"name":"x"}}],"tool_choice":` + tc + `}`)
		if _, in := Inject("openai", InjectAuto, body, true); in {
			t.Fatalf("must skip injection under forcing tool_choice %s", tc)
		}
	}
	// auto is fine.
	body := []byte(`{"tools":[{"type":"function","function":{"name":"x"}}],"tool_choice":"auto"}`)
	if _, in := Inject("openai", InjectAuto, body, true); !in {
		t.Fatal("tool_choice auto should allow injection")
	}
	// Anthropic {"type":"any"} is forcing; {"type":"auto"} is fine.
	if _, in := Inject("anthropic", InjectAuto, []byte(`{"tools":[{"name":"x"}],"tool_choice":{"type":"any"}}`), true); in {
		t.Fatal("anthropic tool_choice any must skip")
	}
	if _, in := Inject("anthropic", InjectAuto, []byte(`{"tools":[{"name":"x"}],"tool_choice":{"type":"auto"}}`), true); !in {
		t.Fatal("anthropic tool_choice auto should allow injection")
	}
}
