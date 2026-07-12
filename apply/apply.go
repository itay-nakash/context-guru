// Package apply is the one place the pipeline meets a raw wire request, shared
// by every host adapter (the bifrost proxy and the AuthBridge plugin). It
// extracts the messages array, runs the pipeline on it, and splices the result
// back into the original body — byte-lossless for every other field (headroom
// invariant I1). This is what makes "one implementation behind both
// integrations" concrete: hosts differ only in how they obtain the body,
// provider, and session id.
//
// Provider normalization. Components operate on OpenAI-shaped tool outputs
// (role=="tool" messages with string content). The Anthropic Messages API
// instead carries tool outputs as `tool_result` content blocks INSIDE user
// messages — a shape bifrost's ChatContentBlock cannot even represent (it drops
// the payload on unmarshal). So for Anthropic requests we expand each
// tool_result block into a synthetic role=tool message the existing components
// already know how to shrink, run the pipeline, then splice each rewritten
// tool output back into its exact source block via sjson. Everything the
// pipeline did not touch stays byte-identical.
package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/schema"
	"github.com/kagenti/context-guru/session"
	"github.com/kagenti/context-guru/store"
	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// debugTraffic, when CONTEXT_GURU_DEBUG is set, logs each inbound tool output's
// token count + first line so we can analyze why components did/didn't fire on
// real agent traffic. Diagnostic only.
var debugTraffic = os.Getenv("CONTEXT_GURU_DEBUG") != ""

func dumpToolOutputs(norm []bschemas.ChatMessage) {
	tools := 0
	for _, m := range norm {
		if m.Role != bschemas.ChatMessageRoleTool {
			continue
		}
		tools++
		t := schema.MessageText(m)
		head := strings.TrimSpace(t)
		if i := strings.IndexByte(head, '\n'); i >= 0 {
			head = head[:i]
		}
		if len(head) > 160 {
			head = head[:160]
		}
		slog.Info("cg.debug.toolout", "tokens", schema.TextTokens(t), "lines", strings.Count(t, "\n")+1, "head", head)
	}
	slog.Info("cg.debug.request", "tool_outputs", tools, "total_tool_tokens", schema.MessagesTokens(&bschemas.BifrostChatRequest{Input: norm}))
}

// slotKind is how a normalized message maps back to the raw body.
type slotKind int

const (
	// wholeMessage: norm message i corresponds 1:1 to messages.<msgIdx>; a change
	// re-marshals the whole message (guarded by lossless round-trip).
	wholeMessage slotKind = iota
	// anthropicToolText: norm message is a synthetic role=tool extracted from an
	// Anthropic tool_result block; a change rewrites only that block's `content`
	// string field, which is byte-lossless for the rest of the message.
	anthropicToolText
)

// slot records how one normalized message writes back to the raw body.
type slot struct {
	kind     slotKind
	path     string // sjson path: "messages.<i>" (whole) or "messages.<i>.content.<b>.content" (tool text)
	pre      []byte // wholeMessage: canonical marshal of the original message
	preText  string // anthropicToolText: original tool-output text (change detection)
	lossless bool   // wholeMessage: does bifrost round-trip this message without dropping fields
}

// Body runs the pipeline over the request body's messages and returns the
// rewritten body. changed=false means "forward the original unchanged" (no
// messages array, unparseable, or a re-serialization problem) — always fail
// open. explicitSession is the host-supplied session id ("" -> content hash).
func Body(ctx context.Context, pipe *components.Pipeline, st store.Store, provider bschemas.ModelProvider, body []byte, explicitSession string, bypass bool) ([]byte, bool) {
	msgsRaw := gjson.GetBytes(body, "messages")
	if !msgsRaw.Exists() || !msgsRaw.IsArray() {
		return body, false
	}

	norm, slots := normalize(provider, msgsRaw.Array())
	if len(norm) == 0 {
		return body, false
	}

	if debugTraffic {
		dumpToolOutputs(norm)
	}
	chat := &bschemas.BifrostChatRequest{Provider: provider, Input: norm}
	sys, firstUser := systemAndFirstUser(norm)
	c := &components.Ctx{
		Ctx:     ctx,
		Session: session.Resolve(explicitSession, sys, firstUser),
		Store:   st,
		Bypass:  bypass,
	}

	pipe.Run(chat, c)

	// A component changed the message count (none of the v1 set does) — the slot
	// map no longer aligns, so fail open and forward the original untouched.
	if len(chat.Input) != len(norm) {
		return body, false
	}

	out := body
	changed := false
	var changes []change
	for i := range chat.Input {
		s := slots[i]
		switch s.kind {
		case anthropicToolText:
			newText := schema.MessageText(chat.Input[i])
			if newText == s.preText {
				continue
			}
			var err error
			if out, err = sjson.SetBytes(out, s.path, newText); err != nil {
				return body, false
			}
			changed = true
			changes = append(changes, mkChange(s.path, s.preText, newText))
		default: // wholeMessage
			post, err := json.Marshal(chat.Input[i])
			if err != nil {
				return body, false
			}
			if bytes.Equal(post, s.pre) {
				continue // unmodified — keep the original bytes verbatim (I1)
			}
			if !s.lossless {
				// bifrost can't round-trip this message; splicing our re-marshal would
				// drop provider fields it doesn't model. Discard the change, keep the
				// original bytes. ponytail: correctness over the marginal saving here.
				continue
			}
			if out, err = sjson.SetRawBytes(out, s.path, post); err != nil {
				return body, false
			}
			changed = true
			var pm bschemas.ChatMessage
			_ = json.Unmarshal(s.pre, &pm)
			changes = append(changes, mkChange(s.path, schema.MessageText(pm), schema.MessageText(chat.Input[i])))
		}
	}
	if changed && dumpPath != "" {
		dumpChanges(c.Session, changes)
	}
	return out, changed
}

// change is one rewritten message, captured for the CONTEXT_GURU_DUMP trace so a
// human can see exactly what context-guru did to the wire.
type change struct {
	Path         string `json:"path"`
	BeforeTokens int    `json:"before_tokens"`
	AfterTokens  int    `json:"after_tokens"`
	Before       string `json:"before"`
	After        string `json:"after"`
}

func mkChange(path, before, after string) change {
	return change{
		Path: path, BeforeTokens: schema.TextTokens(before), AfterTokens: schema.TextTokens(after),
		Before: clip(before, 4000), After: clip(after, 4000),
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…[+" + strconv.Itoa(len(s)-n) + " bytes]"
}

var dumpPath = os.Getenv("CONTEXT_GURU_DUMP")

// dumpChanges appends one JSON line describing this request's rewrites.
func dumpChanges(session string, changes []change) {
	f, err := os.OpenFile(dumpPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	before, after := 0, 0
	for _, c := range changes {
		before += c.BeforeTokens
		after += c.AfterTokens
	}
	rec := map[string]any{
		"session": session, "n_changed": len(changes),
		"tokens_before": before, "tokens_after": after, "saved": before - after,
		"changes": changes,
	}
	if b, err := json.Marshal(rec); err == nil {
		f.Write(append(b, '\n'))
	}
}

// normalize builds the message slice the pipeline runs on plus a write-back slot
// per message. For OpenAI (and any non-Anthropic dialect) every message maps
// 1:1 to a whole-message slot — the request is already in the shape components
// expect. For Anthropic, each user-message `tool_result` block with string
// content becomes a synthetic role=tool message with a text-field write-back
// slot; the block's siblings and every other message are left for the raw body
// to carry verbatim.
func normalize(provider bschemas.ModelProvider, arr []gjson.Result) (norm []bschemas.ChatMessage, slots []slot) {
	for i, m := range arr {
		if provider == bschemas.Anthropic &&
			m.Get("role").String() == string(bschemas.ChatMessageRoleUser) &&
			m.Get("content").IsArray() {
			handled := false
			for b, blk := range m.Get("content").Array() {
				if blk.Get("type").String() != "tool_result" {
					continue
				}
				content := blk.Get("content")
				if content.Type != gjson.String {
					continue // array/structured tool_result content — skip (never lose non-text)
				}
				handled = true
				text := content.String()
				norm = append(norm, toolMessage(text, blk.Get("tool_use_id").String()))
				slots = append(slots, slot{
					kind:    anthropicToolText,
					path:    "messages." + strconv.Itoa(i) + ".content." + strconv.Itoa(b) + ".content",
					preText: text,
				})
			}
			if handled {
				continue // this user message contributed its tool_result blocks; body carries the rest
			}
		}
		// Default: whole-message slot. Unmarshal via bifrost and record whether that
		// round-trips losslessly.
		var cm bschemas.ChatMessage
		if err := json.Unmarshal([]byte(m.Raw), &cm); err != nil {
			continue // unparseable message — leave it in the body untouched
		}
		preMarshal, _ := json.Marshal(cm)
		norm = append(norm, cm)
		slots = append(slots, slot{
			kind:     wholeMessage,
			path:     "messages." + strconv.Itoa(i),
			pre:      preMarshal,
			lossless: jsonEqual([]byte(m.Raw), preMarshal),
		})
	}
	return norm, slots
}

// toolMessage builds a synthetic OpenAI-shaped tool message from an Anthropic
// tool_result so the (provider-agnostic) components can process it.
func toolMessage(text, toolUseID string) bschemas.ChatMessage {
	m := bschemas.ChatMessage{Role: bschemas.ChatMessageRoleTool}
	schema.SetMessageText(&m, text)
	if toolUseID != "" {
		id := toolUseID
		m.ChatToolMessage = &bschemas.ChatToolMessage{ToolCallID: &id}
	}
	return m
}

// jsonEqual reports whether two JSON documents are semantically equal (ignoring
// key order and whitespace). Used to decide whether bifrost's schema round-trips
// a message without dropping fields.
func jsonEqual(a, b []byte) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

func systemAndFirstUser(msgs []bschemas.ChatMessage) (sys, firstUser string) {
	for _, m := range msgs {
		t := schema.MessageText(m)
		switch m.Role {
		case bschemas.ChatMessageRoleSystem:
			sys += t
		case bschemas.ChatMessageRoleUser:
			if firstUser == "" {
				firstUser = t
			}
		}
	}
	return sys, firstUser
}
