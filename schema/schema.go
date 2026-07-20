// Package schema wraps bifrost's provider-agnostic chat schema with the helpers
// context-guru components need: token accounting, deep-clone for fail-open
// snapshots, tool-result iteration, and byte-preservation for lossless
// round-trips of provider-specific fields.
//
// Components operate directly on *schemas.BifrostChatRequest (see package
// components); this package is the small toolbox around that type, not a
// competing model. Wire<->schema conversion lives in the host adapters — the
// bifrost proxy gets it for free from bifrost's transport, the AuthBridge
// plugin uses FromOpenAIBytes/FromAnthropicBytes (added in P3).
package schema

import (
	"encoding/json"

	"github.com/rossoctl/context-guru/internal/tokens"
	"github.com/maximhq/bifrost/core/schemas"
)

// Provider identifies the wire dialect a request arrived in. It drives
// provider-specific behaviour (e.g. cache_control is Anthropic-family) and how
// the adapter renders bytes back out.
type Provider = schemas.ModelProvider

// MessagesTokens estimates the token cost of a request's messages by counting
// the message CONTENT text — what the model actually reads — not the JSON
// envelope. This is the signal the never-worse gate needs: control metadata
// like cache_control adds envelope bytes but no model-visible tokens, so a
// cache-injection Reformat must not look "worse" for adding it.
func MessagesTokens(req *schemas.BifrostChatRequest) int {
	if req == nil {
		return 0
	}
	n := 0
	for _, m := range req.Input {
		n += tokens.Count(MessageText(m))
	}
	return n
}

// TextTokens counts tokens in a raw string via the shared tokenizer.
func TextTokens(s string) int { return tokens.Count(s) }

// CloneMessages deep-copies a message slice via JSON round-trip. Used to
// snapshot a request before a component runs so the pipeline can restore it on
// error/panic or when a component fails the never-worse gate. JSON round-trip
// is safe because bifrost's content types implement custom (Un)MarshalJSON that
// preserve cache_control/citations/cachePoint.
func CloneMessages(in []schemas.ChatMessage) []schemas.ChatMessage {
	if in == nil {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return in
	}
	var out []schemas.ChatMessage
	if err := json.Unmarshal(b, &out); err != nil {
		return in
	}
	return out
}

// BlockText returns the text payload of a content block, or "" if the block
// carries no text (image/audio/file/refusal).
func BlockText(b schemas.ChatContentBlock) string {
	if b.Text != nil {
		return *b.Text
	}
	return ""
}

// MessageText returns the concatenated text of a message, handling both the
// string-content and block-content representations.
func MessageText(m schemas.ChatMessage) string {
	if m.Content == nil {
		return ""
	}
	if m.Content.ContentStr != nil {
		return *m.Content.ContentStr
	}
	var s string
	for _, blk := range m.Content.ContentBlocks {
		s += BlockText(blk)
	}
	return s
}

// SetMessageText replaces a message's content with a single text string,
// collapsing any block structure. Components that rewrite a whole message's
// text (e.g. cmdfilter, offload markers) use this.
func SetMessageText(m *schemas.ChatMessage, text string) {
	m.Content = &schemas.ChatMessageContent{ContentStr: &text}
}

// Rewritable reports whether m's content can be safely replaced with a plain
// text string (via SetMessageText) without losing data. It is false when the
// message carries any non-text content block — image/audio/file/refusal, or a
// block type bifrost does not model (e.g. Anthropic tool_result, whose payload
// lives in fields MessageText never reads). Those bytes would vanish on a
// text-only rewrite and were never stashed, so components must skip such
// messages. String content and all-text block content are rewritable.
func Rewritable(m schemas.ChatMessage) bool {
	if m.Content == nil || m.Content.ContentStr != nil {
		return true
	}
	for _, b := range m.Content.ContentBlocks {
		switch b.Type {
		case schemas.ChatContentBlockTypeText, "":
			// text (or an untyped block we treat as text) is safe
		default:
			return false
		}
	}
	return true
}
