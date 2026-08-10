package cheapmodel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The Anthropic backend must send the invariant preamble as a `system` block carrying a
// cache_control breakpoint, with the variable part left in the user message. Wrong shape
// = no caching, silently (issue #28 part A).
func TestAnthropicSendsCachedSystemBlock(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":900}}`)
	}))
	defer srv.Close()

	_, err := Anthropic{BaseURL: srv.URL, Model: "m"}.
		CompleteSystem(context.Background(), "INVARIANT PREAMBLE", "VARIABLE PART")
	if err != nil {
		t.Fatal(err)
	}

	sys, ok := body["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("expected a 1-block system array, got %#v", body["system"])
	}
	blk := sys[0].(map[string]any)
	if blk["type"] != "text" || blk["text"] != "INVARIANT PREAMBLE" {
		t.Fatalf("system block must carry the preamble as text: %#v", blk)
	}
	cc, ok := blk["cache_control"].(map[string]any)
	if !ok || cc["type"] != "ephemeral" {
		t.Fatalf("system block must carry an ephemeral cache_control breakpoint: %#v", blk)
	}
	// The variable part must stay in the user message — putting it in the cached block
	// would make the prefix differ every call and cache nothing.
	msgs := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected exactly one user message, got %d", len(msgs))
	}
	if m := msgs[0].(map[string]any); m["role"] != "user" || m["content"] != "VARIABLE PART" {
		t.Fatalf("variable part must be the user message: %#v", m)
	}
}

// Complete (no system) must keep the original single-user-message shape, so nothing that
// relies on it changes behavior.
func TestAnthropicCompleteKeepsSingleMessageShape(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"OK"}]}`)
	}))
	defer srv.Close()

	if _, err := (Anthropic{BaseURL: srv.URL, Model: "m"}).Complete(context.Background(), "P"); err != nil {
		t.Fatal(err)
	}
	if _, present := body["system"]; present {
		t.Fatal("Complete without a system part must not send a system field")
	}
}

// The OpenAI backend has no explicit breakpoints, so it must degrade CLEANLY: a leading
// system message (the cacheable-prefix idiom there) and NO invented cache_control field,
// which the API would reject.
func TestOpenAIDegradesToLeadingSystemMessage(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"OUT"}}],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":80}}}`)
	}))
	defer srv.Close()

	if _, err := (OpenAI{BaseURL: srv.URL, Model: "m"}).
		CompleteSystem(context.Background(), "PREAMBLE", "VARIABLE"); err != nil {
		t.Fatal(err)
	}
	if _, present := body["system"]; present {
		t.Fatal("OpenAI must not send a top-level system field")
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected system+user messages, got %d", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "PREAMBLE" {
		t.Fatalf("preamble must be a LEADING system message: %#v", first)
	}
	if _, bad := first["cache_control"]; bad {
		t.Fatal("must not invent cache_control on the OpenAI backend")
	}
	if second := msgs[1].(map[string]any); second["role"] != "user" || second["content"] != "VARIABLE" {
		t.Fatalf("variable part must be the user message: %#v", second)
	}
}

// OpenAI counts cached tokens INSIDE prompt_tokens; Anthropic reports the tiers
// disjointly. Normalize, or the "fresh input" figure means different things per backend
// and the cost model silently double-counts.
func TestOpenAICachedTokensAreNotDoubleCounted(t *testing.T) {
	resetUsage()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"OUT"}}],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":80}}}`)
	}))
	defer srv.Close()

	if _, err := (OpenAI{BaseURL: srv.URL, Model: "m"}).Complete(context.Background(), "P"); err != nil {
		t.Fatal(err)
	}
	_, in, out := Usage()
	_, read := CacheUsage()
	if in != 20 { // 100 prompt - 80 cached
		t.Fatalf("fresh input tokens = %d, want 20 (cached excluded)", in)
	}
	if read != 80 {
		t.Fatalf("cache read tokens = %d, want 80", read)
	}
	if out != 5 {
		t.Fatalf("output tokens = %d, want 5", out)
	}
}

// A read of 0 across calls is the signal that a breakpoint is being silently ignored (the
// prefix is under the model's minimum cacheable length) — the measured reality on
// claude-haiku-4-5, whose minimum is 4096 tokens against our ~1463-token preamble. The
// accounting must make that visible rather than implying a win from placement alone.
func TestCacheReadZeroIsVisible(t *testing.T) {
	resetUsage()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Mirrors the gateway's real response for a sub-minimum prefix: no write, no read.
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":1808,"output_tokens":10}}`)
	}))
	defer srv.Close()

	for i := 0; i < 3; i++ {
		if _, err := (Anthropic{BaseURL: srv.URL, Model: "m"}).
			CompleteSystem(context.Background(), "SHORT PREAMBLE", "VAR"); err != nil {
			t.Fatal(err)
		}
	}
	write, read := CacheUsage()
	if write != 0 || read != 0 {
		t.Fatalf("sub-minimum prefix must record no cache activity, got write=%d read=%d", write, read)
	}
	calls, in, _ := Usage()
	if calls != 3 || in != 3*1808 {
		t.Fatalf("all input must be billed fresh: calls=%d in=%d", calls, in)
	}
	// AvgCallCost must reflect that: no cache benefit, full input price every call.
	avg, ok := AvgCallCost(HaikuPricing())
	if !ok || avg <= 0 {
		t.Fatalf("AvgCallCost must be observable and positive, got %v ok=%v", avg, ok)
	}
}

// resetUsage clears the process counters so usage assertions are independent.
func resetUsage() {
	llmCalls.Store(0)
	llmInputTokens.Store(0)
	llmOutputTokens.Store(0)
	llmCacheWrite.Store(0)
	llmCacheRead.Store(0)
}
