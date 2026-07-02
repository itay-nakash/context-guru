package proxyhttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kagenti/lab-context-engineering/config"
	"github.com/kagenti/lab-context-engineering/engine"
	"github.com/kagenti/lab-context-engineering/internal/markers"
	"github.com/kagenti/lab-context-engineering/observability"
)

// captureUpstream records the last body it received and returns a fixed response.
func captureUpstream(t *testing.T, got *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
}

func newProxy(t *testing.T, upstream string) http.Handler {
	t.Helper()
	s := config.Default()
	s.ProtectRecent = 1
	return New(Config{Engine: engine.New(s, nil, nil), Upstream: upstream})
}

func TestProxyReducesBeforeForwarding(t *testing.T) {
	var upstreamGot string
	up := captureUpstream(t, &upstreamGot)
	defer up.Close()
	h := newProxy(t, up.URL)

	big := strings.Repeat("a.go long file content line for the read result here\\n", 12)
	body := `{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"read","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"` + big + `"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"u2","name":"edit","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u2","content":"success: edited"}]},
		{"role":"user","content":[{"type":"text","text":"thanks"}]}
	]}`

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(upstreamGot) >= len(body) {
		t.Fatalf("upstream body not reduced: got %d, original %d", len(upstreamGot), len(body))
	}
	if len(markers.FindIDs(upstreamGot)) == 0 {
		t.Fatalf("expected a lab-cx marker in the forwarded body")
	}
}

func TestBypassForwardsOriginal(t *testing.T) {
	var upstreamGot string
	up := captureUpstream(t, &upstreamGot)
	defer up.Close()
	h := newProxy(t, up.URL)

	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-labcx-bypass", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if upstreamGot != body {
		t.Fatalf("bypass should forward the original body verbatim:\n got: %s\nwant: %s", upstreamGot, body)
	}
}

// TestRuntimeModeToggle: POST /labcx/mode flips reduction on/off without a restart.
// mode=off → forward the original untouched; mode=on → reduce again. Bad value → 400.
func TestRuntimeModeToggle(t *testing.T) {
	var upstreamGot string
	up := captureUpstream(t, &upstreamGot)
	defer up.Close()
	h := newProxy(t, up.URL)

	big := strings.Repeat("a.go long file content line for the read result here\\n", 12)
	body := `{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"read","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"` + big + `"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"u2","name":"edit","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u2","content":"success: edited"}]},
		{"role":"user","content":[{"type":"text","text":"thanks"}]}
	]}`

	post := func(payload string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/labcx/mode", strings.NewReader(payload))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /labcx/mode %s = %d", payload, rec.Code)
		}
		var got struct{ Mode string }
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return got.Mode
	}

	// Default is "on": a reduce-eligible request gets reduced.
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if len(upstreamGot) >= len(body) {
			t.Fatalf("default mode should reduce: got %d, original %d", len(upstreamGot), len(body))
		}
	}

	// Flip to off → the next request is forwarded verbatim.
	if m := post(`{"mode":"off"}`); m != "off" {
		t.Fatalf("mode after off = %q", m)
	}
	upstreamGot = ""
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if upstreamGot != body {
			t.Fatalf("mode=off should forward original verbatim:\n got: %s\nwant: %s", upstreamGot, body)
		}
	}

	// Flip back to on → reduction resumes.
	if m := post(`{"mode":"on"}`); m != "on" {
		t.Fatalf("mode after on = %q", m)
	}
	upstreamGot = ""
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if len(upstreamGot) >= len(body) {
			t.Fatalf("mode=on should reduce again: got %d, original %d", len(upstreamGot), len(body))
		}
	}

	// Unknown value → 400, mode unchanged.
	req := httptest.NewRequest(http.MethodPost, "/labcx/mode", strings.NewReader(`{"mode":"bogus"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad mode should be 400, got %d", rec.Code)
	}

	// GET reports the current mode.
	greq := httptest.NewRequest(http.MethodGet, "/labcx/mode", nil)
	grec := httptest.NewRecorder()
	h.ServeHTTP(grec, greq)
	var got struct{ Mode string }
	_ = json.Unmarshal(grec.Body.Bytes(), &got)
	if got.Mode != "on" {
		t.Fatalf("GET mode = %q, want on", got.Mode)
	}
}

func TestPassthroughForwardsUnknownPaths(t *testing.T) {
	var upstreamGot string
	up := captureUpstream(t, &upstreamGot)
	defer up.Close()
	h := newProxy(t, up.URL)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("passthrough status = %d", rec.Code)
	}
}

// TestEvalContainersPrefixedPath: eval-containers posts to /anthropic/v1/messages;
// the proxy must still reduce it and forward the full prefixed path to the gateway.
func TestEvalContainersPrefixedPath(t *testing.T) {
	var gotPath, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()
	h := newProxy(t, up.URL)

	big := strings.Repeat("a.go long file content line for the read result here\\n", 12)
	body := `{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"read","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"` + big + `"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"u2","name":"edit","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u2","content":"success: edited"}]},
		{"role":"user","content":[{"type":"text","text":"thanks"}]}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if gotPath != "/anthropic/v1/messages" {
		t.Fatalf("upstream path = %q, want the prefix preserved", gotPath)
	}
	if len(gotBody) >= len(body) {
		t.Fatalf("prefixed request was not reduced")
	}
}

type captureEmitter struct{ events []observability.Event }

func (c *captureEmitter) Emit(_ context.Context, e observability.Event) {
	c.events = append(c.events, e)
}

func TestProxyEmitsSavingsEvent(t *testing.T) {
	var upstreamGot string
	up := captureUpstream(t, &upstreamGot)
	defer up.Close()
	cap := &captureEmitter{}
	s := config.Default()
	s.ProtectRecent = 1
	h := New(Config{Engine: engine.New(s, nil, nil), Upstream: up.URL, Emitter: cap})

	big := strings.Repeat("a.go long file content line for the read result here\\n", 12)
	body := `{"model":"claude-x","messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"read","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"` + big + `"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"u2","name":"edit","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u2","content":"success: edited"}]},
		{"role":"user","content":[{"type":"text","text":"thanks"}]}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if len(cap.events) != 1 {
		t.Fatalf("expected 1 emitted event, got %d", len(cap.events))
	}
	e := cap.events[0]
	if e.System != "anthropic" || e.RequestModel != "claude-x" || e.TokensSaved <= 0 {
		t.Fatalf("unexpected event: %+v", e)
	}
}

func TestRequestBodyCap(t *testing.T) {
	h := New(Config{Engine: engine.New(config.Default(), nil, nil), Upstream: "http://unused", MaxBodyBytes: 10})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(strings.Repeat("x", 100)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want 413", rec.Code)
	}
}

func TestStatsExposesSavings(t *testing.T) {
	var upstreamGot string
	up := captureUpstream(t, &upstreamGot)
	defer up.Close()

	s := config.Default()
	s.ProtectRecent = 1
	agg := observability.NewAggregator(map[string]observability.CostRate{
		observability.DefaultCostKey: {InputPerMTok: 1.0},
	})
	h := New(Config{Engine: engine.New(s, nil, nil), Upstream: up.URL, Emitter: agg, Aggregator: agg})

	big := strings.Repeat("a.go long file content line for the read result here\\n", 12)
	body := `{"model":"claude-x","messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"read","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"` + big + `"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"u2","name":"edit","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u2","content":"success: edited"}]},
		{"role":"user","content":[{"type":"text","text":"thanks"}]}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	h.ServeHTTP(httptest.NewRecorder(), req)

	srec := httptest.NewRecorder()
	h.ServeHTTP(srec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if srec.Code != http.StatusOK {
		t.Fatalf("/stats status = %d", srec.Code)
	}
	var snap observability.Snapshot
	if err := json.Unmarshal(srec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("unmarshal /stats: %v", err)
	}
	if snap.Requests != 1 {
		t.Fatalf("Requests = %d, want 1", snap.Requests)
	}
	if snap.TokensSaved <= 0 {
		t.Fatalf("TokensSaved = %d, want > 0", snap.TokensSaved)
	}
}

func TestStatsDisabledWhenNoAggregator(t *testing.T) {
	h := newProxy(t, "http://unused")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/stats without aggregator = %d, want 404", rec.Code)
	}
}

func TestHealth(t *testing.T) {
	h := newProxy(t, "http://unused")
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health = %d", rec.Code)
	}
}

// TestModelBasePreservesPrefix: the per-request (source: incoming) model base must
// reproduce the main request's upstream URL, including any route prefix.
func TestModelBasePreservesPrefix(t *testing.T) {
	cases := []struct{ upstream, path, surface, want string }{
		{"http://gateway:4000", "/anthropic/v1/messages", "anthropic", "http://gateway:4000/anthropic"},
		{"http://gateway:4000/", "/v1/messages", "anthropic", "http://gateway:4000"},
		{"https://api.anthropic.com", "/v1/messages", "anthropic", "https://api.anthropic.com"},
		{"http://gateway:4000", "/openai/v1/chat/completions", "openai", "http://gateway:4000/openai"},
	}
	for _, c := range cases {
		if got := modelBase(c.upstream, c.path, c.surface); got != c.want {
			t.Errorf("modelBase(%q,%q,%q) = %q, want %q", c.upstream, c.path, c.surface, got, c.want)
		}
	}
}
