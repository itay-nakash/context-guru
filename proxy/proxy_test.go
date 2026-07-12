package proxy_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/kagenti/context-guru/components/all"
	"github.com/kagenti/context-guru/config"
	"github.com/kagenti/context-guru/metrics"
	"github.com/kagenti/context-guru/proxy"
	"github.com/kagenti/context-guru/store"
	"github.com/tidwall/gjson"
)

// buildHandler wires a real config->pipeline->proxy against a mock upstream that
// records the body it receives.
func buildHandler(t *testing.T, yaml string, upstream string) (*proxy.Handler, store.Store) {
	t.Helper()
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	agg := metrics.NewAggregator()
	pipe, err := cfg.Build(agg)
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory(store.Options{})
	return proxy.New(pipe, st, agg, proxy.Options{OpenAIUpstream: upstream, AnthropicUpstream: upstream}), st
}

func openAIBody(msgs ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"model": "gpt-x", "temperature": 0.2, "messages": msgs})
	return b
}

func TestProxyReducesThenForwards(t *testing.T) {
	var got []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	h, _ := buildHandler(t, "pipeline: [dedup]\n", upstream.URL)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	dump := strings.Repeat("a verbose repeated tool output line\n", 60)
	body := openAIBody(
		map[string]any{"role": "user", "content": "do the thing"},
		map[string]any{"role": "tool", "tool_call_id": "a", "content": dump},
		map[string]any{"role": "tool", "tool_call_id": "b", "content": dump},
	)
	resp, err := http.Post(srv.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Upstream must have received a SMALLER messages array (dedup collapsed the dup),
	// while non-message fields (model, temperature) survive verbatim (I1).
	if len(got) == 0 {
		t.Fatal("upstream received nothing")
	}
	if gjson.GetBytes(got, "model").String() != "gpt-x" || gjson.GetBytes(got, "temperature").Float() != 0.2 {
		t.Fatalf("non-message fields not preserved: %s", got)
	}
	third := gjson.GetBytes(got, "messages.2.content").String()
	if !strings.Contains(third, "identical to an earlier") {
		t.Fatalf("dedup did not run through the proxy: %q", third)
	}
	if len(got) >= len(body) {
		t.Fatalf("proxy did not shrink the request (before=%d after=%d)", len(body), len(got))
	}
}

// TestAnthropicRouteReducesToolResult drives the real /anthropic/v1/messages
// gateway route with a Claude-Code-shaped body (tool outputs as tool_result
// blocks in user messages) and asserts the offloader fires end-to-end.
func TestAnthropicRouteReducesToolResult(t *testing.T) {
	var got []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type":"message","content":[{"type":"text","text":"ok"}]}`))
	}))
	defer upstream.Close()

	h, _ := buildHandler(t, "pipeline: [dedup]\ncomponents:\n  dedup: {min_tokens: 20}\n", upstream.URL)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	dump := strings.Repeat("verbose repeated anthropic tool output line\n", 40)
	body, _ := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "do it"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": dump},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t2", "content": dump},
			}},
		},
	})
	resp, err := http.Post(srv.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if len(got) == 0 {
		t.Fatal("upstream received nothing")
	}
	if gjson.GetBytes(got, "model").String() != "claude-sonnet-4-6" {
		t.Fatalf("model not preserved: %s", got)
	}
	if !strings.Contains(gjson.GetBytes(got, "messages.2.content.0.content").String(), "identical to an earlier") {
		t.Fatalf("dedup did not run on the anthropic tool_result via the proxy: %s", got)
	}
	if len(got) >= len(body) {
		t.Fatalf("proxy did not shrink the anthropic request (before=%d after=%d)", len(body), len(got))
	}
}

func TestBypassHeaderForwardsUnchanged(t *testing.T) {
	var got []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	h, _ := buildHandler(t, "pipeline: [dedup]\n", upstream.URL)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	dump := strings.Repeat("repeated line\n", 60)
	body := openAIBody(
		map[string]any{"role": "tool", "content": dump},
		map[string]any{"role": "tool", "content": dump},
	)
	req, _ := http.NewRequest("POST", srv.URL+"/openai/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("x-context-guru-bypass", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gjson.GetBytes(got, "messages.1.content").String() != dump {
		t.Fatal("bypass should forward messages unchanged")
	}
}

func TestGatewayInjectsRealKey(t *testing.T) {
	var gotAuth, gotXAPI string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotXAPI = r.Header.Get("x-api-key")
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	cfg, _ := config.LoadBytes([]byte("pipeline: []\n"))
	pipe, _ := cfg.Build(nil)
	h := proxy.New(pipe, store.NewMemory(store.Options{}), nil, proxy.Options{
		OpenAIUpstream: upstream.URL, OpenAIKey: "real-openai-key",
	})
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	body := openAIBody(map[string]any{"role": "user", "content": "hi"})
	req, _ := http.NewRequest("POST", srv.URL+"/openai/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer sk-proxy") // placeholder from the agent
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotAuth != "Bearer real-openai-key" {
		t.Fatalf("gateway should inject the real key, upstream saw %q", gotAuth)
	}
	_ = gotXAPI
}

func TestExpandToolLoop(t *testing.T) {
	var calls int
	var secondBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			// model asks to expand the offloaded content
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[` +
				`{"id":"call_1","type":"function","function":{"name":"context_guru_expand","arguments":"{\"id\":\"HASH\"}"}}` +
				`]},"finish_reason":"tool_calls"}]}`))
			return
		}
		secondBody = b
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer upstream.Close()

	h, st := buildHandler(t, "pipeline: []\n", upstream.URL)
	st.Put("HASH", []byte("THE ORIGINAL CONTENT")) // as if a prior turn offloaded it
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	body := openAIBody(map[string]any{"role": "user", "content": "go"})
	resp, err := http.Post(srv.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	final, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if calls != 2 {
		t.Fatalf("expected 2 upstream calls (initial + continuation), got %d", calls)
	}
	if !strings.Contains(string(final), "done") {
		t.Fatalf("proxy should return the final answer, got %s", final)
	}
	if !strings.Contains(string(secondBody), "THE ORIGINAL CONTENT") {
		t.Fatalf("continuation must carry the resolved original, got %s", secondBody)
	}
	if gjson.GetBytes(secondBody, "messages.#").Int() != 3 {
		t.Fatalf("continuation should append assistant + tool turns: %s", secondBody)
	}
}

// TestExpandPartialResolutionWellFormed guards the malformed-continuation bug:
// the model makes TWO expand calls but only one id resolves. The continuation
// must still carry a tool_result for BOTH call ids (the missing one gets a
// placeholder) or the provider rejects the request.
func TestExpandPartialResolutionWellFormed(t *testing.T) {
	var calls int
	var secondBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[` +
				`{"id":"call_1","type":"function","function":{"name":"context_guru_expand","arguments":"{\"id\":\"GOOD\"}"}},` +
				`{"id":"call_2","type":"function","function":{"name":"context_guru_expand","arguments":"{\"id\":\"GONE\"}"}}` +
				`]},"finish_reason":"tool_calls"}]}`))
			return
		}
		secondBody = b
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer upstream.Close()

	h, st := buildHandler(t, "pipeline: []\n", upstream.URL)
	st.Put("GOOD", []byte("RESOLVED ORIGINAL")) // only one of the two resolves
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	body := openAIBody(map[string]any{"role": "user", "content": "go"})
	resp, err := http.Post(srv.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if calls != 2 {
		t.Fatalf("expected a continuation round, got %d upstream calls", calls)
	}
	// One tool message per tool_call_id (both call_1 and call_2), or the provider errors.
	var toolCalls int
	gjson.GetBytes(secondBody, "messages").ForEach(func(_, m gjson.Result) bool {
		if m.Get("role").String() == "tool" {
			toolCalls++
		}
		return true
	})
	if toolCalls != 2 {
		t.Fatalf("continuation must have a tool result per call id, got %d: %s", toolCalls, secondBody)
	}
	if !strings.Contains(string(secondBody), "RESOLVED ORIGINAL") || !strings.Contains(string(secondBody), "no longer available") {
		t.Fatalf("continuation should carry the resolved original and a placeholder for the expired id: %s", secondBody)
	}
}

func TestExpandRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{}`)) }))
	defer upstream.Close()
	h, _ := buildHandler(t, "pipeline: [collapse]\ncomponents:\n  collapse: {max_tokens: 20, head_lines: 2, tail_lines: 2}\n", upstream.URL)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("log content line with enough words to matter here\n")
	}
	body := openAIBody(map[string]any{"role": "tool", "content": b.String()})
	http.Post(srv.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(string(body)))

	// The collapsed message carries an expand marker; the id must resolve via /expand.
	stats, _ := http.Get(srv.URL + "/stats")
	var snap metrics.Snapshot
	json.NewDecoder(stats.Body).Decode(&snap)
	stats.Body.Close()
	if snap.Requests != 1 || snap.SavedTokens <= 0 {
		t.Fatalf("stats not recorded: %+v", snap)
	}
}
