// Package reformat holds the lossless components (they repack the request
// denser or add caching hints without losing information). Each registers via
// init(); a binary blank-imports components/all to pull them in.
package reformat

import (
	"github.com/kagenti/context-guru/components"
	"github.com/maximhq/bifrost/core/schemas"
)

func init() { components.Register("cacheinject", newCacheinject) }

// Cacheinject places an Anthropic-family cache_control breakpoint on the prefix
// boundary so the provider's KV cache hits across turns. It is a Reformat: it
// adds a control directive, changes no model-visible content, and loses nothing.
//
// v1 heuristic: mark the last content block of the last message BEFORE the
// newest turn (a more stable boundary than the live message). Refined with
// sticky prefix tracking in P5. No-op for non-Anthropic providers and when a
// breakpoint is already present.
type Cacheinject struct{}

func newCacheinject(_ []byte) (components.Component, error) { return &Cacheinject{}, nil }

func (Cacheinject) Name() string { return "cacheinject" }

func (Cacheinject) Enabled(c *components.Ctx) bool { return true }

func (Cacheinject) Reformat(req *schemas.BifrostChatRequest, rep *components.Report, _ *components.Ctx) error {
	if !cacheAware(req.Provider) {
		rep.Skipped = true
		return nil
	}
	// Boundary = the message just before the newest turn; fall back to the last
	// message when there are too few to have a stable prefix.
	if len(req.Input) == 0 {
		rep.Skipped = true
		return nil
	}
	idx := len(req.Input) - 1
	if len(req.Input) >= 2 {
		idx = len(req.Input) - 2
	}
	m := &req.Input[idx]
	if m.Content == nil || len(m.Content.ContentBlocks) == 0 {
		// String-content messages can't carry a block-level breakpoint; skip
		// rather than restructure them (keeps this strictly lossless).
		rep.Skipped = true
		return nil
	}
	last := &m.Content.ContentBlocks[len(m.Content.ContentBlocks)-1]
	if last.CacheControl != nil {
		rep.Skipped = true // already marked; leave byte-stable
		return nil
	}
	last.CacheControl = &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}
	return nil
}

// cacheAware reports whether the provider honours Anthropic-style cache_control.
func cacheAware(p schemas.ModelProvider) bool {
	switch p {
	case schemas.Anthropic, schemas.Bedrock, schemas.Vertex:
		return true
	default:
		return false
	}
}
