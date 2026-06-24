// Package canon defines lab-context-engineering's canonical request model: an
// Anthropic-shaped chat request. Every surface (Anthropic, OpenAI, Gemini) maps a
// provider's wire format onto this model, the stage pipeline reads and mutates it,
// and the surface renders it back. It is the one shape the engine understands.
//
// The model is intentionally a generic decoded-JSON container (Root) rather than a
// fully-typed struct: an LLM request carries many provider-specific top-level fields
// (max_tokens, temperature, stream, metadata, ...) and per-block extensions
// (cache_control). Keeping the whole object as decoded JSON preserves every field
// across a decode→encode round-trip and lets stages add breakpoints freely — exactly
// what the winnow prototype relied on. Typed helpers give ergonomic access to the
// parts stages actually touch (messages and content blocks).
package canon

import (
	"bytes"
	"encoding/json"
)

// Request is a canonical (Anthropic-shaped) chat request. Root is the decoded
// top-level JSON object and is the source of truth; the typed accessors below read
// and mutate it in place.
type Request struct {
	Root map[string]any
}

// Decode parses an Anthropic-shaped request body into a Request. Numbers are
// preserved as json.Number so an integer field re-encodes as "1", not "1.0".
func Decode(body []byte) (Request, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return Request{}, err
	}
	if root == nil {
		root = map[string]any{}
	}
	return Request{Root: root}, nil
}

// Encode serializes the request back to JSON bytes.
func (r Request) Encode() ([]byte, error) {
	return json.Marshal(r.Root)
}

// Clone returns a deep copy so a surface can mutate without touching the caller's
// data. It round-trips through JSON, preserving json.Number fidelity.
func (r Request) Clone() Request {
	b, err := json.Marshal(r.Root)
	if err != nil {
		return Request{Root: map[string]any{}}
	}
	out, err := Decode(b)
	if err != nil {
		return Request{Root: map[string]any{}}
	}
	return out
}

// Model returns the "model" field, or "" if absent.
func (r Request) Model() string {
	s, _ := r.Root["model"].(string)
	return s
}

// Messages returns the raw message list as a slice of maps. Mutating an element
// mutates the underlying request. Returns nil if there are no messages.
func (r Request) Messages() []map[string]any {
	raw, ok := r.Root["messages"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

// Blocks returns the content blocks of a message as a slice of maps. Anthropic
// content may be a plain string (returned as a single synthesized text block) or a
// list of block objects.
func Blocks(msg map[string]any) []map[string]any {
	switch c := msg["content"].(type) {
	case string:
		return []map[string]any{{"type": "text", "text": c}}
	case []any:
		out := make([]map[string]any, 0, len(c))
		for _, b := range c {
			if bb, ok := b.(map[string]any); ok {
				out = append(out, bb)
			}
		}
		return out
	default:
		return nil
	}
}

// BlockType returns a block's "type" field.
func BlockType(block map[string]any) string {
	s, _ := block["type"].(string)
	return s
}
