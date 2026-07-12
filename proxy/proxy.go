// Package proxy is the context-guru HTTP proxy: it runs the component pipeline
// on inbound chat requests, then forwards them to the configured upstream
// provider. It is the eval-containers gateway (exposes /openai + /anthropic on
// one port) and the standalone LLM-proxy integration.
//
// It reuses bifrost's ChatMessage type but not its transport: the transport
// can't inject an in-process Go plugin, so we drive the request path directly.
// Message rewriting is byte-lossless (headroom invariant I1) — only the
// `messages` array is re-serialized; every other field of the original body is
// preserved verbatim via sjson.
package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kagenti/context-guru/apply"
	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/expand"
	"github.com/kagenti/context-guru/metrics"
	"github.com/kagenti/context-guru/schema"
	"github.com/kagenti/context-guru/store"
	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/sjson"
)

// Options configures upstreams and credential injection. Each upstream is a base
// URL the matching route forwards to (the incoming path is appended). When a key
// is set it replaces the client's auth on forward — this is the eval-containers
// gateway model, where the agent holds only a placeholder key and the real
// provider key lives in the gateway env. Leave keys empty to pass the incoming
// auth through unchanged (local/dev use).
type Options struct {
	OpenAIUpstream    string // e.g. https://api.openai.com
	AnthropicUpstream string
	OpenAIKey         string // injected as Authorization: Bearer <key>
	AnthropicKey      string // injected as x-api-key: <key>
	// ForceModel, when set, overwrites the request's "model" field. eval-containers
	// uses this to pin every call to EVAL_MODEL regardless of what the agent asked for.
	ForceModel string
	Client     *http.Client
}

// upstream binds a provider to its base URL, the canonical provider path to POST
// to (decoupled from the gateway's incoming /openai|/anthropic namespace), and a
// credential injector.
type upstream struct {
	base   string
	path   string
	setKey func(http.Header)
}

// Handler serves the proxy + management routes.
type Handler struct {
	pipe   *components.Pipeline
	store  store.Store
	agg    *metrics.Aggregator
	opts   Options
	client *http.Client
}

// New builds the proxy handler. agg may be nil (no /stats rollups).
func New(pipe *components.Pipeline, st store.Store, agg *metrics.Aggregator, opts Options) *Handler {
	c := opts.Client
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Handler{pipe: pipe, store: st, agg: agg, opts: opts, client: c}
}

// Mux wires the routes: chat proxying + health/stats/expand management.
func (h *Handler) Mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("POST /openai/v1/chat/completions", h.chat(bschemas.OpenAI, upstream{
		base:   h.opts.OpenAIUpstream,
		path:   "/v1/chat/completions",
		setKey: bearerKey(h.opts.OpenAIKey),
	}))
	m.HandleFunc("POST /anthropic/v1/messages", h.chat(bschemas.Anthropic, upstream{
		base:   h.opts.AnthropicUpstream,
		path:   "/v1/messages",
		setKey: headerKey("x-api-key", h.opts.AnthropicKey),
	}))
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	m.HandleFunc("GET /stats", h.stats)
	m.HandleFunc("GET /expand", h.expand)
	return m
}

func bearerKey(key string) func(http.Header) {
	if key == "" {
		return nil
	}
	return func(h http.Header) { h.Set("Authorization", "Bearer "+key) }
}

func headerKey(name, key string) func(http.Header) {
	if key == "" {
		return nil
	}
	return func(h http.Header) { h.Set(name, key) }
}

func (h *Handler) chat(provider bschemas.ModelProvider, up upstream) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		// Pin the model if configured (eval-containers EVAL_MODEL).
		if h.opts.ForceModel != "" {
			if out, err := sjson.SetBytes(body, "model", h.opts.ForceModel); err == nil {
				body = out
			}
		}
		// Rewrite the messages via the shared apply path; fail open.
		body, _ = apply.Body(
			r.Context(), h.pipe, h.store, provider, body,
			r.Header.Get("x-context-guru-session"),
			strings.EqualFold(r.Header.Get("x-context-guru-bypass"), "true"),
		)
		h.serve(w, r, provider, up, body)
	}
}

// maxExpandRounds caps the expand continuation loop (headroom's default).
const maxExpandRounds = 3

// TODO(review): expand.ToolDef is never injected into the outgoing request's
// tools array, so the model is only told to "call context_guru_expand" by the
// marker text but the tool is not actually advertised. The continuation loop
// below therefore fires only if the client itself declared the tool. Wiring
// ToolDef in requires per-provider tools-array injection kept byte-stable across
// turns (sticky) to avoid busting the provider prefix cache — a feature, not a
// minimal fix, so it is left for a human decision.

var errNoUpstream = errors.New("no upstream configured")

// serve forwards the request and runs the expand continuation loop: if the model
// calls the expand tool (and only that tool), resolve the originals from the
// store, append the tool-result turn, and re-invoke upstream — up to a few
// rounds. Streaming responses skip the loop and pass straight through.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request, provider bschemas.ModelProvider, up upstream, body []byte) {
	for round := 0; ; round++ {
		resp, err := h.doUpstream(r, up, body)
		if err != nil {
			http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
			return
		}
		if round >= maxExpandRounds || strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
			h.stream(w, resp)
			return
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		calls, otherTools := expand.ResponseCalls(string(provider), respBody)
		if len(calls) == 0 || otherTools {
			writeBuffered(w, resp, respBody)
			return
		}
		// Build a tool_result for EVERY expand call — the provider requires one per
		// tool_call_id or the continuation is malformed. Expired/unknown ids get an
		// explicit placeholder rather than being omitted.
		resolved := map[string]string{}
		got := 0
		for _, c := range calls {
			if orig, ok := expand.Resolve(h.store, c.HashID); ok {
				resolved[c.CallID] = orig
				got++
				if h.agg != nil {
					h.agg.RecordExpand(schema.TextTokens(orig)) // bounce: offload had to come back
				}
			} else {
				resolved[c.CallID] = "[expand: original for id " + c.HashID + " is no longer available]"
			}
		}
		next, ok := expand.Continuation(string(provider), body, respBody, resolved)
		if got == 0 || !ok {
			writeBuffered(w, resp, respBody) // nothing recovered; return the model's own call
			return
		}
		body = next // loop: re-invoke with the originals in hand
	}
}

func (h *Handler) doUpstream(r *http.Request, up upstream, body []byte) (*http.Response, error) {
	if up.base == "" {
		return nil, errNoUpstream
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, up.base+up.path, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	copyHeaders(req.Header, r.Header)
	if up.setKey != nil {
		// Gateway mode: drop the client's placeholder auth, inject the real key.
		req.Header.Del("Authorization")
		req.Header.Del("x-api-key")
		up.setKey(req.Header)
	}
	return h.client.Do(req)
}

// stream copies an upstream response through with flushing (SSE-friendly).
func (h *Handler) stream(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	flush, _ := w.(http.Flusher)
	buf := make([]byte, 16*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if flush != nil {
				flush.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
}

func writeBuffered(w http.ResponseWriter, resp *http.Response, body []byte) {
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func (h *Handler) stats(w http.ResponseWriter, _ *http.Request) {
	if h.agg == nil {
		w.Write([]byte("{}"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.agg.Snapshot())
}

// expand resolves a stashed original by id — the HTTP side of reversibility (the
// model-callable tool loop is a separate concern, added with response handling).
func (h *Handler) expand(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	orig, ok := expand.Resolve(h.store, id)
	if !ok {
		http.Error(w, "expired or unknown id", http.StatusNotFound)
		return
	}
	w.Write([]byte(orig))
}

// copyHeaders copies headers except hop-by-hop ones; the caller's provider auth
// (Authorization / x-api-key) passes straight through to the upstream.
func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Keep-Alive", "Transfer-Encoding", "Content-Length", "Host":
			continue
		}
		if strings.HasPrefix(strings.ToLower(k), "x-context-guru-") {
			continue
		}
		dst[k] = append([]string(nil), vs...)
	}
}
