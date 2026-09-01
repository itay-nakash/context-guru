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

// sweepPipeline is a pipeline that CONTAINS extract_llm_sweep, i.e. one that can actually adjudicate.
// The gate on the injection is (Anthropic route AND this component present), so a fixture built with
// `pipeline: []` cannot exercise the injection at all — and a test that asserted the tool appeared
// under `pipeline: []` was asserting the defect: the tool was reaching the `off` control arm of every
// published comparison. See proxy.chat.
const sweepPipeline = "pipeline: [extract_llm_sweep]\n"

// forwardedBody posts one request through the proxy on the ANTHROPIC route with a sweep-bearing
// pipeline, and returns exactly what reached upstream.
func forwardedBody(t *testing.T, body []byte) []byte {
	t.Helper()
	return forwardedOn(t, "/anthropic/v1/messages", sweepPipeline, body)
}

// forwardedOn is forwardedBody with the route and pipeline spelled out, for the cases whose whole
// point is that one of those two differs.
func forwardedOn(t *testing.T, route, yaml string, body []byte) []byte {
	t.Helper()
	var got []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(route, "anthropic") {
			_, _ = w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","model":"claude",` +
				`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
				`"usage":{"input_tokens":5,"output_tokens":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer upstream.Close()
	h, _ := buildHandler(t, yaml, upstream.URL)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()
	resp, err := http.Post(srv.URL+route, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return got
}

// toolsRequest is the ANTHROPIC dialect, because that is the only dialect the injection targets:
// prefixAskerFor returns nil for every other provider and cheapmodel/openai.go has no CompletePrefixed
// at all, so the definition could never be read there.
func toolsRequest(t *testing.T, msgs ...map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"model":      "claude-sonnet-5",
		"max_tokens": 64,
		"tools": []any{map[string]any{"name": "read_file", "description": "read a file",
			"input_schema": map[string]any{"type": "object"}}},
		"messages": msgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// streamingToolsRequest is toolsRequest with stream:true, for the SSE splice path.
func streamingToolsRequest(t *testing.T, msgs ...map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"model":      "claude-sonnet-5",
		"max_tokens": 64,
		"stream":     true,
		"tools": []any{map[string]any{"name": "read_file", "description": "read a file",
			"input_schema": map[string]any{"type": "object"}}},
		"messages": msgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// toolsRequestOpenAI is the same request in the OpenAI dialect, for the provider half of the gate.
func toolsRequestOpenAI(t *testing.T, msgs ...map[string]any) []byte {
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
	// Appended last, after the client's own tool, so the client's order is untouched.
	tools := gjson.GetBytes(got, "tools").Array()
	if n := len(tools); n < 2 || tools[n-1].Get("name").String() != adjudicate.ToolName {
		t.Errorf("our tool is not last in the tools array: %s", gjson.GetBytes(got, "tools").Raw)
	}
	if tools[0].Get("name").String() != "read_file" {
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
		map[string]any{"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": "c1", "name": adjudicate.ToolName, "input": map[string]any{},
		}}},
		map[string]any{"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "c1", "is_error": true,
			"content": "Error: No such tool available: " + adjudicate.ToolName,
		}}},
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

// THE GATE, half one: a pipeline with no extract_llm_sweep can never adjudicate, so advertising the
// tool there buys nothing and costs the cacheable prefix. `off` is the A/B CONTROL ARM of every
// published comparison in this repo, and codesmart is a shipped preset with no sweep in it; injecting
// unconditionally perturbed both by a measured 946 bytes at the head of the prefix on every request.
func TestAdjudicateToolNotAdvertisedWhenThePipelineCannotAdjudicate(t *testing.T) {
	for _, yaml := range []string{"pipeline: []\n", "pipeline: [format]\n"} {
		got := forwardedOn(t, "/anthropic/v1/messages", yaml,
			toolsRequest(t, map[string]any{"role": "user", "content": "go"}))
		if len(got) == 0 {
			t.Fatalf("%s: nothing reached upstream", yaml)
		}
		if strings.Contains(string(got), adjudicate.ToolName) {
			t.Errorf("%s: advertised on a pipeline that cannot adjudicate: %s", yaml, got)
		}
	}
}

// THE GATE, half two: the provider. prefixAskerFor returns nil for anything but Anthropic and
// cheapmodel/openai.go has no CompletePrefixed at all, so on the OpenAI route the definition is
// unreachable by construction — it was pure prefix cost, ~217 tokens per request.
func TestAdjudicateToolNotAdvertisedOnANonAnthropicRoute(t *testing.T) {
	got := forwardedOn(t, "/openai/v1/chat/completions", sweepPipeline,
		toolsRequestOpenAI(t, map[string]any{"role": "user", "content": "go"}))
	if len(got) == 0 {
		t.Fatal("nothing reached upstream")
	}
	if strings.Contains(string(got), adjudicate.ToolName) {
		t.Errorf("advertised on a route that can never read it: %s", got)
	}
}

// THE LEAK, non-streaming path. A tool_use for a proxy-injected tool must never reach the client: the
// client never declared it, cannot execute it, and answers "not found", losing the agent a turn. It
// must instead be answered IN BAND, before the client is written to, which leaves the request-path
// repair as a backstop rather than the primary defence.
func TestAdjudicateStrayCallDoesNotReachTheClientOnTheJSONPath(t *testing.T) {
	round := 0
	var second []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		round++
		w.Header().Set("Content-Type", "application/json")
		if round == 1 {
			// The model calls OUR tool, which is always a defect by construction.
			_, _ = w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","model":"claude",` +
				`"content":[{"type":"tool_use","id":"stray1","name":"` + adjudicate.ToolName +
				`","input":{"verdicts":[]}}],"stop_reason":"tool_use",` +
				`"usage":{"input_tokens":5,"output_tokens":1}}`))
			return
		}
		second = body
		_, _ = w.Write([]byte(`{"id":"m2","type":"message","role":"assistant","model":"claude",` +
			`"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn",` +
			`"usage":{"input_tokens":6,"output_tokens":2}}`))
	}))
	defer upstream.Close()
	h, _ := buildHandler(t, sweepPipeline, upstream.URL)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/anthropic/v1/messages", "application/json",
		bytes.NewReader(toolsRequest(t, map[string]any{"role": "user", "content": "go"})))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// PRECONDITION: the loop must actually have run a second round, or this asserts nothing.
	if round < 2 {
		t.Fatalf("the stray was never intercepted -- only %d upstream round(s), client got: %s",
			round, got)
	}
	if strings.Contains(string(got), adjudicate.ToolName) {
		t.Errorf("the proxy-injected tool_use reached the CLIENT: %s", got)
	}
	// Answered in band, so the model could finish its turn rather than wait for a tool nobody runs.
	if !strings.Contains(string(second), "runs automatically") {
		t.Errorf("the stray was withheld but never answered upstream: %s", second)
	}
	if !strings.Contains(string(got), "done") {
		t.Errorf("the client did not receive the finished turn: %s", got)
	}
}

// THE LEAK, streaming path. The splicer withheld only the expand tool by name, so an adjudication call
// streamed through event by event and the client saw it live.
func TestAdjudicateStrayCallDoesNotReachTheClientOnTheSSEPath(t *testing.T) {
	round := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		round++
		if round == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			for _, ev := range []string{
				`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`,
				`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"stray1","name":"` + adjudicate.ToolName + `","input":{}}}`,
				`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
				`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
				`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
			} {
				_, _ = w.Write([]byte(ev + "\n\n"))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			return
		}
		// The continuation MUST also stream. The request said stream:true, and a JSON body on a
		// later round is a documented upstream anomaly that cannot be spliced into an event
		// stream -- the loop then hands the withheld events back, which is the very leak this
		// test is trying to observe. A fixture that answers in JSON therefore fails for a reason
		// that has nothing to do with the withhold set.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, ev := range []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"m2","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":6,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		} {
			_, _ = w.Write([]byte(ev + "\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer upstream.Close()
	h, _ := buildHandler(t, sweepPipeline, upstream.URL)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()
	body := streamingToolsRequest(t, map[string]any{"role": "user", "content": "go"})
	resp, err := http.Post(srv.URL+"/anthropic/v1/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if round < 2 {
		t.Fatalf("the streamed stray was never intercepted -- only %d round(s), client got: %s",
			round, got)
	}
	if strings.Contains(string(got), adjudicate.ToolName) {
		t.Errorf("the proxy-injected tool_use was STREAMED to the client: %s", got)
	}
	if !strings.Contains(string(got), "done") {
		t.Errorf("the client did not receive the finished turn: %s", got)
	}
}
