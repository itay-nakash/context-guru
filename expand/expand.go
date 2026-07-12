// Package expand holds the host-agnostic half of reversibility (design D6,
// after headroom's CCR): the marker format Offload components write, the
// expand(id) tool definition injected per provider, and resolution of a stashed
// original from the Store.
//
// The continuation LOOP that actually answers an expand tool call is host glue
// (the bifrost proxy wraps its chat route; the AuthBridge plugin does it in
// OnResponse) — but every host reuses ParseMarkers, ToolDef, and Resolve from
// here so the wire contract stays identical across integrations.
package expand

import (
	"regexp"

	"github.com/kagenti/context-guru/store"
)

// ToolName is the model-callable tool that retrieves offloaded content.
const ToolName = "context_guru_expand"

// Marker is the sentinel an Offload component writes in place of dropped
// content. HASH is the store key. Sticky-on per session (headroom's golden
// tool-bytes rule) so injecting the tool never busts the provider prefix cache.
//
//	<<cg:HASH>>
var markerRe = regexp.MustCompile(`<<cg:([A-Za-z0-9_-]{1,64})>>`)

// Marker renders the sentinel for a given store key.
func Marker(key string) string { return "<<cg:" + key + ">>" }

// ParseMarkers returns the distinct store keys referenced by any markers in s,
// in first-seen order.
func ParseMarkers(s string) []string {
	m := markerRe.FindAllStringSubmatch(s, -1)
	seen := map[string]struct{}{}
	var out []string
	for _, g := range m {
		k := g[1]
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// Resolve returns the original stashed under key, or ("",false) if it's gone
// (expired/evicted). Callers must handle the miss gracefully — an expired
// original silently turns a lossless offload lossy (headroom's known TTL edge).
func Resolve(s store.Store, key string) (string, bool) {
	b, ok := s.Get(key)
	if !ok {
		return "", false
	}
	return string(b), true
}

// ToolDef returns the expand tool definition shaped for the given provider
// dialect. OpenAI/Anthropic differ (parameters vs input_schema); the returned
// map is ready to append to the request's tools array.
func ToolDef(provider string) map[string]any {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "string", "description": "The HASH from a <<cg:HASH>> marker to retrieve in full."},
		},
		"required": []string{"id"},
	}
	desc := "Retrieve the full original content that was compressed and replaced by a <<cg:HASH>> marker."
	switch provider {
	case "anthropic":
		return map[string]any{"name": ToolName, "description": desc, "input_schema": params}
	default: // openai and compatibles
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": ToolName, "description": desc, "parameters": params},
		}
	}
}
