package reduce

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/internal/tokens"
)

func hashParts(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(fmt.Sprint(p)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

func toolUseText(input any) string {
	if b, err := json.Marshal(input); err == nil {
		return string(b)
	}
	return fmt.Sprint(input)
}

// resultText flattens tool_result content (string, or list of text blocks/strings).
func resultText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var out []string
		for _, b := range c {
			switch bb := b.(type) {
			case map[string]any:
				if bb["type"] == "text" {
					if t, ok := bb["text"].(string); ok {
						out = append(out, t)
					}
				}
			case string:
				out = append(out, bb)
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

func asString(v any) string { s, _ := v.(string); return s }

// ExtractItems parses a canonical request into a flat list of ContextItem.
func ExtractItems(req canon.Request) []ContextItem {
	var items []ContextItem
	tooluseFile := map[string]string{}
	tooluseRange := map[string][2]*int{}

	for mi, msg := range req.Messages() {
		raw, isList := msg["content"].([]any)
		if !isList {
			if s, ok := msg["content"].(string); ok && s != "" {
				items = append(items, ContextItem{
					ID: hashParts(mi, 0, s), MsgIndex: mi, BlockIndex: 0,
					Kind: "text", Text: s, TokenEst: tokens.Count(s),
				})
			}
			continue
		}
		for bi, bRaw := range raw {
			blk, ok := bRaw.(map[string]any)
			if !ok {
				continue
			}
			switch blk["type"] {
			case "tool_use":
				input, _ := blk["input"].(map[string]any)
				if input == nil {
					input = map[string]any{}
				}
				fp := fileArg(input)
				off, lim := readRange(input)
				tuID := asString(blk["id"])
				if tuID != "" {
					tooluseFile[tuID] = fp
					tooluseRange[tuID] = [2]*int{off, lim}
				}
				text := toolUseText(blk["input"])
				items = append(items, ContextItem{
					ID: hashParts(mi, bi, blk["name"], text), MsgIndex: mi, BlockIndex: bi,
					Kind: "tool_use", ToolName: asString(blk["name"]), ToolUseID: tuID,
					FilePath: fp, Text: text, TokenEst: tokens.Count(text),
					ReadOffset: off, ReadLimit: lim,
				})
			case "tool_result":
				tuID := asString(blk["tool_use_id"])
				text := resultText(blk["content"])
				rng := tooluseRange[tuID]
				items = append(items, ContextItem{
					ID: hashParts(mi, bi, tuID, text), MsgIndex: mi, BlockIndex: bi,
					Kind: "tool_result", ToolUseID: tuID, FilePath: tooluseFile[tuID],
					Text: text, TokenEst: tokens.Count(text),
					ReadOffset: rng[0], ReadLimit: rng[1],
				})
			case "text":
				text := asString(blk["text"])
				items = append(items, ContextItem{
					ID: hashParts(mi, bi, text), MsgIndex: mi, BlockIndex: bi,
					Kind: "text", Text: text, TokenEst: tokens.Count(text),
				})
			}
		}
	}
	return items
}
