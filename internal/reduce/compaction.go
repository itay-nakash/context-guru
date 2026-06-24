package reduce

import (
	"strings"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/internal/markers"
	"github.com/kagenti/lab-context-engineering/internal/store"
)

// Phrases characterizing a known agent compaction prompt (Claude Code / Codex /
// Gemini CLI). A false positive only forgoes one turn's savings. Ported from
// winnow's compaction.py.
var compactionPhrases = []string{
	"this session is being continued from a previous conversation",
	"create a detailed summary of the conversation",
	"context checkpoint compaction",
	"your task is to create a summary",
	"create a handoff summary",
	"summarize the conversation",
	"summary of the conversation so far",
	"continue from where we left off",
}

func textOf(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, b := range c {
			if bb, ok := b.(map[string]any); ok {
				if t, ok := bb["text"].(string); ok {
					parts = append(parts, t)
				} else if cs, ok := bb["content"].(string); ok {
					parts = append(parts, cs)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// IsCompactionRequest reports whether the request looks like the agent asking the
// model to summarize the conversation (so reduction should pass it through).
func IsCompactionRequest(req canon.Request) bool {
	haystack := strings.ToLower(textOf(req.Root["system"]))
	msgs := req.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i]["role"] == "user" {
			haystack += "\n" + strings.ToLower(textOf(msgs[i]["content"]))
			break
		}
	}
	for _, p := range compactionPhrases {
		if strings.Contains(haystack, p) {
			return true
		}
	}
	return false
}

// RehydrateMarkers replaces any winnow-collapsed block with its stored original, in
// place. Returns the number of blocks restored.
func RehydrateMarkers(req canon.Request, st store.Rewind) int {
	restored := 0
	for _, m := range req.Messages() {
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, bRaw := range content {
			blk, ok := bRaw.(map[string]any)
			if !ok {
				continue
			}
			var key string
			switch blk["type"] {
			case "tool_result":
				key = "content"
			case "text":
				key = "text"
			default:
				continue
			}
			s, ok := blk[key].(string)
			if !ok {
				continue
			}
			ids := markers.FindIDs(s)
			if len(ids) == 0 {
				continue
			}
			if original, ok := st.Get(ids[0]); ok {
				blk[key] = original
				restored++
			}
		}
	}
	return restored
}
