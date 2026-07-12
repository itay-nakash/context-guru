package expand

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponseCallsOpenAI(t *testing.T) {
	resp := `{"choices":[{"message":{"role":"assistant","tool_calls":[
		{"id":"call_1","type":"function","function":{"name":"context_guru_expand","arguments":"{\"id\":\"HASH1\"}"}}
	]}}]}`
	calls, other := ResponseCalls("openai", []byte(resp))
	if other || len(calls) != 1 || calls[0].CallID != "call_1" || calls[0].HashID != "HASH1" {
		t.Fatalf("bad parse: %+v other=%v", calls, other)
	}
}

func TestResponseCallsOtherToolBails(t *testing.T) {
	resp := `{"choices":[{"message":{"tool_calls":[
		{"id":"c1","function":{"name":"context_guru_expand","arguments":"{\"id\":\"H\"}"}},
		{"id":"c2","function":{"name":"do_something_else","arguments":"{}"}}
	]}}]}`
	calls, other := ResponseCalls("openai", []byte(resp))
	if !other || len(calls) != 1 {
		t.Fatalf("expected otherTools=true with one expand call, got %+v other=%v", calls, other)
	}
}

func TestResponseCallsAnthropic(t *testing.T) {
	resp := `{"role":"assistant","content":[
		{"type":"text","text":"let me look"},
		{"type":"tool_use","id":"toolu_1","name":"context_guru_expand","input":{"id":"HASH2"}}
	]}`
	calls, other := ResponseCalls("anthropic", []byte(resp))
	if other || len(calls) != 1 || calls[0].CallID != "toolu_1" || calls[0].HashID != "HASH2" {
		t.Fatalf("bad anthropic parse: %+v other=%v", calls, other)
	}
}

func TestContinuationOpenAIAppendsTurns(t *testing.T) {
	req := `{"model":"gpt","messages":[{"role":"user","content":"hi"}]}`
	resp := `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","function":{"name":"context_guru_expand","arguments":"{\"id\":\"H\"}"}}]}}]}`
	out, ok := Continuation("openai", []byte(req), []byte(resp), map[string]string{"call_1": "THE ORIGINAL"})
	if !ok {
		t.Fatal("continuation failed")
	}
	msgs := gjson.GetBytes(out, "messages")
	if msgs.Get("#").Int() != 3 {
		t.Fatalf("expected 3 messages (user, assistant, tool), got %d: %s", msgs.Get("#").Int(), out)
	}
	if msgs.Get("2.role").String() != "tool" || msgs.Get("2.tool_call_id").String() != "call_1" ||
		!strings.Contains(msgs.Get("2.content").String(), "THE ORIGINAL") {
		t.Fatalf("tool result turn wrong: %s", out)
	}
}

func TestContinuationAnthropicAppendsTurns(t *testing.T) {
	req := `{"messages":[{"role":"user","content":"hi"}]}`
	resp := `{"content":[{"type":"tool_use","id":"toolu_1","name":"context_guru_expand","input":{"id":"H"}}]}`
	out, ok := Continuation("anthropic", []byte(req), []byte(resp), map[string]string{"toolu_1": "ORIG"})
	if !ok {
		t.Fatal("continuation failed")
	}
	msgs := gjson.GetBytes(out, "messages")
	if msgs.Get("#").Int() != 3 || msgs.Get("1.role").String() != "assistant" || msgs.Get("2.role").String() != "user" {
		t.Fatalf("expected user,assistant,user turns: %s", out)
	}
	if msgs.Get("2.content.0.tool_use_id").String() != "toolu_1" || !strings.Contains(msgs.Get("2.content.0.content").String(), "ORIG") {
		t.Fatalf("tool_result wrong: %s", out)
	}
}
