package proxyhttp

import (
	"context"
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
		t.Fatalf("expected a winnow marker in the forwarded body")
	}
}

func TestBypassForwardsOriginal(t *testing.T) {
	var upstreamGot string
	up := captureUpstream(t, &upstreamGot)
	defer up.Close()
	h := newProxy(t, up.URL)

	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-winnow-bypass", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if upstreamGot != body {
		t.Fatalf("bypass should forward the original body verbatim:\n got: %s\nwant: %s", upstreamGot, body)
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

func TestHealth(t *testing.T) {
	h := newProxy(t, "http://unused")
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health = %d", rec.Code)
	}
}
