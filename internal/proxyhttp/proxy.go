// Package proxyhttp is the HTTP shell around the engine: a transparent proxy that
// reduces /v1/messages and /v1/chat/completions requests and forwards everything
// (including streaming responses and non-model endpoints) upstream unchanged. This
// is the "any agent" and eval-containers integration — agents point their
// *_BASE_URL at it. Fail-open: any error forwards the original request.
package proxyhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kagenti/lab-context-engineering/engine"
	"github.com/kagenti/lab-context-engineering/observability"
	"github.com/kagenti/lab-context-engineering/surfaces"
)

// Mode is the proxy's process-wide runtime reduction mode, settable without a
// restart via POST /labcx/mode. It is the new-stack equivalent of the old
// CE-Manager activation: a KV-pressure monitor flips it on/off live.
type Mode string

const (
	ModeOn            Mode = "on"            // reduce with the startup-configured method
	ModeOff           Mode = "off"           // bypass: forward the original request untouched
	ModeDeterministic Mode = "deterministic" // reserved; currently behaves like ModeOn
)

// ParseMode validates a mode string (case/space-insensitive). ok=false for anything
// outside the enum, so callers can reject it with HTTP 400.
func ParseMode(s string) (Mode, bool) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeOn:
		return ModeOn, true
	case ModeOff:
		return ModeOff, true
	case ModeDeterministic:
		return ModeDeterministic, true
	default:
		return "", false
	}
}

// Config configures the proxy handler.
type Config struct {
	Engine *engine.Engine
	// Upstream, if set, is the base URL every request is forwarded to (the
	// eval-containers gateway case). If empty, requests route to the provider
	// default by path (api.anthropic.com / api.openai.com).
	Upstream         string
	AnthropicDefault string // default "https://api.anthropic.com"
	OpenAIDefault    string // default "https://api.openai.com"
	Client           *http.Client
	Emitter          observability.Emitter // default observability.Nop
	// Aggregator, if set, is served at GET /stats (Snapshot as JSON). It is
	// independent of Emitter: a host may stream via Emitter and also expose
	// process-wide stats here, or pass the same *observability.Aggregator as both.
	Aggregator *observability.Aggregator
	// MaxBodyBytes caps the request body read from clients; 0 means no cap. A body
	// exceeding the cap yields HTTP 413.
	MaxBodyBytes int64
	// UpstreamTimeout bounds each upstream request when Client is not set; 0 means
	// no timeout (http.DefaultClient).
	UpstreamTimeout time.Duration
	// BuildRequestModel, when set, builds an engine.Model from the incoming request's
	// own model + credentials (surface name, model id, resolved upstream base, request
	// headers) for LLM compactors configured with source "incoming". Returning nil means
	// "no per-request model" (those compactors then no-op). The host (proxy binary)
	// supplies this so cheapmodel construction stays out of this package.
	BuildRequestModel func(surfaceName, model, base string, h http.Header) engine.Model
	// InitialMode is the reduction mode at startup (on|off|deterministic). Empty or
	// invalid defaults to ModeOn, preserving the always-reduce default. Settable at
	// runtime via POST /labcx/mode.
	InitialMode Mode
}

type proxy struct {
	cfg  Config
	mode atomic.Value // holds Mode; runtime-settable via POST /labcx/mode
}

// New returns the proxy http.Handler. Routing is by path SUFFIX so it works both for
// direct agents (POST /v1/messages) and for eval-containers, which prefix the wire
// path (POST /anthropic/v1/messages, /openai/v1/chat/completions). The full original
// path is preserved when forwarding upstream.
func New(cfg Config) http.Handler {
	if cfg.Client == nil {
		if cfg.UpstreamTimeout > 0 {
			cfg.Client = &http.Client{Timeout: cfg.UpstreamTimeout}
		} else {
			cfg.Client = http.DefaultClient
		}
	}
	if cfg.AnthropicDefault == "" {
		cfg.AnthropicDefault = "https://api.anthropic.com"
	}
	if cfg.OpenAIDefault == "" {
		cfg.OpenAIDefault = "https://api.openai.com"
	}
	if cfg.Emitter == nil {
		cfg.Emitter = observability.Nop{}
	}
	p := &proxy{cfg: cfg}
	initial := cfg.InitialMode
	if _, ok := ParseMode(string(initial)); !ok {
		initial = ModeOn
	}
	p.mode.Store(initial)
	return p
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/health" || path == "/ready":
		p.ok(w, r)
	case path == "/labcx/expand":
		p.expand(w, r)
	case path == "/labcx/mode":
		p.modeControl(w, r)
	case path == "/stats":
		p.stats(w, r)
	case strings.HasSuffix(path, "/v1/messages"):
		p.model(surfaces.Anthropic{}, p.cfg.AnthropicDefault)(w, r)
	case strings.HasSuffix(path, "/chat/completions"):
		p.model(surfaces.OpenAI{}, p.cfg.OpenAIDefault)(w, r)
	default:
		p.passthrough(p.cfg.AnthropicDefault)(w, r)
	}
}

func (p *proxy) ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// stats serves the process-wide reduction Snapshot as JSON. Returns 404 when no
// Aggregator is configured.
func (p *proxy) stats(w http.ResponseWriter, _ *http.Request) {
	if p.cfg.Aggregator == nil {
		http.Error(w, "stats not enabled", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = p.cfg.Aggregator.WriteJSON(w)
}

func (p *proxy) expand(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	original, ok := p.cfg.Engine.Expand(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, original)
}

// currentMode returns the live reduction mode (defaults to ModeOn if unset).
func (p *proxy) currentMode() Mode {
	if m, ok := p.mode.Load().(Mode); ok {
		return m
	}
	return ModeOn
}

// modeControl gets or sets the process-wide reduction mode at runtime — no restart.
// GET → {"mode":"<current>"}. POST/PUT reads {"mode":"on|off|deterministic"} (or the
// ?mode= query) and stores it; an unknown value is rejected with HTTP 400. This is the
// switch a KV-pressure monitor flips to activate/deactivate context engineering live.
func (p *proxy) modeControl(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// fall through and report the current mode
	case http.MethodPost, http.MethodPut:
		want := r.URL.Query().Get("mode")
		if want == "" {
			var body struct {
				Mode string `json:"mode"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err == nil {
				want = body.Mode
			}
		}
		m, ok := ParseMode(want)
		if !ok {
			http.Error(w, "invalid mode (want on|off|deterministic)", http.StatusBadRequest)
			return
		}
		p.mode.Store(m)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"mode": string(p.currentMode())})
}

// upstreamBase returns the base URL to forward to: the configured single upstream,
// or the provider default.
func (p *proxy) upstreamBase(providerDefault string) string {
	if p.cfg.Upstream != "" {
		return p.cfg.Upstream
	}
	return providerDefault
}

func (p *proxy) model(surface surfaces.Surface, providerDefault string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := p.upstreamBase(providerDefault)
		body, err := p.readBody(w, r)
		if err != nil {
			return // readBody already wrote the response
		}
		// Reduce unless bypassed (per-request header or process-wide mode=off) or the
		// surface can't map this format.
		outBody := body
		if !bypassed(r) && p.currentMode() != ModeOff {
			start := time.Now()
			ctx := r.Context()
			if p.cfg.BuildRequestModel != nil {
				mb := modelBase(base, r.URL.Path, surface.Name())
				if m := p.cfg.BuildRequestModel(surface.Name(), modelOf(body), mb, r.Header); m != nil {
					ctx = engine.WithRequestModel(ctx, m)
				}
			}
			reduced, rep, ok := p.reduce(ctx, surface, body)
			latencyMs := int(time.Since(start).Milliseconds())
			if ok {
				outBody = reduced
				p.cfg.Emitter.Emit(r.Context(), observability.Event{
					System: surface.Name(), Surface: surface.Name(),
					RequestModel: modelOf(body), SessionID: rep.Reduce.SessionID,
					TokensBefore: rep.Reduce.TokensBefore, TokensAfter: rep.Reduce.TokensAfter,
					TokensSaved: rep.Reduce.TokensSaved, Ratio: rep.Reduce.Ratio,
					CacheInject: rep.CacheInjected, Extracted: len(rep.Candidates) > 0,
					StageErrors:     rep.StageErrors,
					ToolsTotal:      rep.Reduce.ToolsTotal,
					ToolDefTokens:   rep.Reduce.ToolDefTokens,
					ReducedCount:    len(rep.Reduce.ReducedIDs),
					CandidatesCount: len(rep.Candidates),
					FrozenCount:     rep.Reduce.FrozenCount,
					Rehydrated:      rep.Reduce.Rehydrated,
					AtCompaction:    rep.Reduce.AtCompaction,
					LatencyMillis:   latencyMs,
				})
			}
		}
		p.forward(w, r, base, outBody)
	}
}

// reduce maps → transforms → renders, returning the reduced body and report. ok=false
// means forward the original (unsupported surface or any failure — fail-open).
func (p *proxy) reduce(ctx context.Context, surface surfaces.Surface, body []byte) (out []byte, rep engine.Report, ok bool) {
	defer func() {
		if recover() != nil {
			out, ok = nil, false
		}
	}()
	req, token, err := surface.ToInternal(body)
	if err != nil {
		return nil, engine.Report{}, false
	}
	transformed, report := p.cfg.Engine.Transform(ctx, req)
	rendered, err := surface.Render(transformed, token)
	if err != nil {
		return nil, engine.Report{}, false
	}
	return rendered, report, true
}

// modelBase returns the base URL a per-request (source: incoming) model client should
// use so that appending the surface's fixed path (/v1/messages or /v1/chat/completions)
// reproduces the SAME upstream URL the main request forwards to — preserving any route
// prefix (e.g. /anthropic when the agent points at http://gateway:4000/anthropic).
func modelBase(upstream, reqPath, surfaceName string) string {
	suffix := "/v1/messages"
	if surfaceName == "openai" {
		suffix = "/v1/chat/completions"
	}
	prefix := strings.TrimSuffix(reqPath, suffix)
	if prefix == reqPath { // suffix not present; fall back to the bare upstream
		prefix = ""
	}
	return strings.TrimRight(upstream, "/") + prefix
}

// modelOf best-effort reads the "model" field from a request body for telemetry.
func modelOf(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Model
}

func (p *proxy) passthrough(providerDefault string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := p.readBody(w, r)
		if err != nil {
			return // readBody already wrote the response
		}
		p.forward(w, r, p.upstreamBase(providerDefault), body)
	}
}

// readBody reads the full request body, enforcing cfg.MaxBodyBytes when set. On a
// cap overflow it writes HTTP 413; on any other read error it writes HTTP 400. In
// both error cases the response is already written and the caller must just return.
func (p *proxy) readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if p.cfg.MaxBodyBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, p.cfg.MaxBodyBytes)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "read body", http.StatusBadRequest)
		}
		return nil, err
	}
	return body, nil
}

// hop-by-hop headers that must not be forwarded.
var hopHeaders = map[string]struct{}{
	"Connection": {}, "Keep-Alive": {}, "Proxy-Authenticate": {}, "Proxy-Authorization": {},
	"Te": {}, "Trailer": {}, "Transfer-Encoding": {}, "Upgrade": {},
}

// forward proxies the request to base+path with the given body and streams the
// response back (flushing per chunk so SSE streams pass through live).
func (p *proxy) forward(w http.ResponseWriter, r *http.Request, base string, body []byte) {
	url := strings.TrimRight(base, "/") + r.URL.Path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "bad upstream request", http.StatusBadGateway)
		return
	}
	for k, vs := range r.Header {
		if _, hop := hopHeaders[http.CanonicalHeaderKey(k)]; hop || k == "Host" {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.ContentLength = int64(len(body))

	resp, err := p.cfg.Client.Do(req)
	if err != nil {
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		if _, hop := hopHeaders[http.CanonicalHeaderKey(k)]; hop {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 16*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

func bypassed(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("x-labcx-bypass"), "true")
}
