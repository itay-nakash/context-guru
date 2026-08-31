package proxy_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/rossoctl/context-guru/internal/adjudicate"
)

// forwardedBody posts one request through the proxy and returns exactly what reached upstream.
func forwardedBody(t *testing.T, body []byte) []byte {
	t.Helper()
	var got []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer upstream.Close()
	h, _ := buildHandler(t, "pipeline: []\n", upstream.URL)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/openai/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return got
}

func toolsRequest(t *testing.T, msgs ...map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"model":    "gpt-x",
		"tools":    []any{map[string]any{"type": "function", "function": map[string]any{"name": "read_file"}}},
		"messages": msgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ADVERTISED ON EVERY TURN, not only on the turn a sweep is about to ask. `tools` hashes ahead of
// system and messages, so a tool that appears when the sweep fires and disappears on the next turn
// invalidates the cached prefix from position zero — the flap expand's InjectAuto exists to prevent.
// And the declaration is the whole mechanism: with the tool present and no tool_choice, the prefix ask
// measured 6 of 6 verdicts on 4 of 4 trials at cache-read price, against 0 of 6 under tool_choice:none.
func TestAdjudicateToolAdvertisedOnEveryTurn(t *testing.T) {
	got := forwardedBody(t, toolsRequest(t, map[string]any{"role": "user", "content": "go"}))
	if len(got) == 0 {
		t.Fatal("nothing reached upstream")
	}
	if !strings.Contains(string(got), adjudicate.ToolName) {
		t.Fatalf("the adjudication tool was not advertised on an ordinary turn: %s", got)
	}
	// Appended last, after the client's own tool AND after the expand tool, so the client's order is
	// untouched.
	tools := gjson.GetBytes(got, "tools").Array()
	if n := len(tools); n < 2 || tools[n-1].Get("function.name").String() != adjudicate.ToolName {
		t.Errorf("our tool is not last in the tools array: %s", gjson.GetBytes(got, "tools").Raw)
	}
	if tools[0].Get("function.name").String() != "read_file" {
		t.Error("the client's own tool moved; its order must be preserved exactly")
	}
	// A turn with nothing to adjudicate must carry it too — that is the point of injecting always.
	got2 := forwardedBody(t, toolsRequest(t, map[string]any{"role": "user", "content": "and again"}))
	if !strings.Contains(string(got2), adjudicate.ToolName) {
		t.Errorf("the tool came and went between turns, which discards the whole cached prefix: %s", got2)
	}
}

// A bypassed compaction request must not get it, for the same reason expand skips one: bypass promises
// a byte-identical forward.
func TestAdjudicateToolNotAdvertisedOnAnAgentCompaction(t *testing.T) {
	got := forwardedBody(t, toolsRequest(t, map[string]any{"role": "user", "content": ccCompactPrompt}))
	if strings.Contains(string(got), adjudicate.ToolName) {
		t.Errorf("injected into a bypassed compaction, which must forward byte-identically: %s", got)
	}
}

// A stray call the AGENT made must be answered on the request path. The client cannot execute a tool
// the proxy injected, so it answers "not found" and the agent loses a turn to a dead end.
func TestAdjudicateStrayCallIsAnsweredOnTheRequestPath(t *testing.T) {
	before := adjudicate.StrayAnswered()
	got := forwardedBody(t, toolsRequest(t,
		map[string]any{"role": "user", "content": "go"},
		map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"id": "c1", "type": "function",
			"function": map[string]any{"name": adjudicate.ToolName, "arguments": "{}"},
		}}},
		map[string]any{"role": "tool", "tool_call_id": "c1",
			"content": "Error: No such tool available: " + adjudicate.ToolName},
	))
	if strings.Contains(string(got), "No such tool available") {
		t.Errorf("the client's dead-end refusal was forwarded to the model unchanged: %s", got)
	}
	if !strings.Contains(string(got), "runs automatically") {
		t.Errorf("no substitute answer was written: %s", got)
	}
	if adjudicate.StrayAnswered() == before {
		t.Error("the stray was not counted; adjudicate_stray is the only signal that the tool's " +
			"description stopped working")
	}
}
