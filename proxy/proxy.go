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

	"github.com/rossoctl/context-guru/apply"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/internal/cheapmodel"
	"github.com/rossoctl/context-guru/metrics"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
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
	// CheapModel is the static "config"-source LLM client for NeedsModel
	// components (nil = none). The "incoming"-source client is built per request
	// from the route's upstream + the gateway's real key.
	CheapModel components.Model
	// PipelineFor builds a pipeline for a per-request override on /compact
	// (?preset=… or x-context-guru-pipeline: a,b,c). nil = overrides ignored, the
	// handler always uses the configured pipeline. Supplied by main (which holds
	// the config + emitter) so proxy stays decoupled from the config package.
	PipelineFor func(preset string, names []string) (*components.Pipeline, error)
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
	m.HandleFunc("POST /compact", h.compact)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	m.HandleFunc("GET /stats", h.stats)
	m.HandleFunc("GET /expand", h.expand)
	return m
}

// compact runs the pipeline over the request body's messages and returns the
// rewritten body — without forwarding upstream. This is the "compact a context,
// hand it back" endpoint: a caller (e.g. the llm-d-router request-inline-
// compaction step) POSTs an inference request body and gets a smaller body of
// the same shape back. Fail-open: any parse/serialize trouble returns the
// original body with 200, so the caller's passthrough contract always holds.
//
// Provider defaults to OpenAI; ?provider=anthropic switches dialects. Config
// overrides (when Options.PipelineFor is set): ?preset=<name> or header
// x-context-guru-pipeline: comp1,comp2. Session/bypass honor the usual headers.
func (h *Handler) compact(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	provider := bschemas.OpenAI
	if strings.EqualFold(r.URL.Query().Get("provider"), "anthropic") {
		provider = bschemas.Anthropic
	}

	pipe := h.pipe
	if h.opts.PipelineFor != nil {
		preset := r.URL.Query().Get("preset")
		var names []string
		if hp := r.Header.Get("x-context-guru-pipeline"); hp != "" {
			names = splitComma(hp)
		}
		if preset != "" || len(names) != 0 {
			// ponytail: rebuild per override request; add an LRU cache if override QPS ever matters.
			if p, err := h.opts.PipelineFor(preset, names); err == nil {
				pipe = p
			} // build error => fall back to the configured pipeline (fail open)
		}
	}

	// No upstream here, so there is no "incoming" model; only the static
	// "config"-source client (and any endpoint pinned in a component's model: block).
	models := components.ModelSpec{Static: h.opts.CheapModel}
	out, _ := apply.BodyWithModel(
		r.Context(), pipe, h.store, provider, body,
		r.Header.Get("x-context-guru-session"),
		strings.EqualFold(r.Header.Get("x-context-guru-bypass"), "true"),
		models,
	)
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// splitComma splits a comma-separated header value into trimmed, non-empty names.
func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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

// incomingModel builds an LLM client that reuses the proxied request's own model
// and the route's upstream + credential, so a NeedsModel component can call the
// same backend the request targets. Prefers the gateway's injected key (gateway
// mode); falls back to the client's own auth header (pass-through). Returns nil
// when no upstream/model/key is resolvable, and the component degrades.
func (h *Handler) incomingModel(provider bschemas.ModelProvider, up upstream, body []byte, r *http.Request) components.Model {
	if up.base == "" {
		return nil
	}
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		return nil
	}
	switch provider {
	case bschemas.Anthropic:
		key := h.opts.AnthropicKey
		if key == "" {
			key = r.Header.Get("x-api-key")
		}
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if key == "" {
			return nil
		}
		return cheapmodel.Anthropic{BaseURL: up.base, Model: model, APIKey: key, Client: h.client}
	case bschemas.OpenAI:
		key := h.opts.OpenAIKey
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if key == "" {
			return nil
		}
		return cheapmodel.OpenAI{BaseURL: up.base, Model: model, APIKey: key, Client: h.client}
	}
	return nil
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
		// Rewrite the messages via the shared apply path; fail open. Supply the
		// LLM clients NeedsModel components may call: the per-request "incoming"
		// model (the route's upstream + the gateway's real key + the request's
		// model) and the static "config" cheap model.
		models := components.ModelSpec{
			Incoming: h.incomingModel(provider, up, body, r),
			Static:   h.opts.CheapModel,
		}
		body, _ = apply.BodyWithModel(
			r.Context(), h.pipe, h.store, provider, body,
			r.Header.Get("x-context-guru-session"),
			strings.EqualFold(r.Header.Get("x-context-guru-bypass"), "true"),
			models,
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
