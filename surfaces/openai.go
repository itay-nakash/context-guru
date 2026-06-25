package surfaces

import (
	"encoding/json"
	"strings"

	"github.com/kagenti/lab-context-engineering/canon"
)

// OpenAI maps OpenAI Chat Completions onto the canonical (Anthropic-shaped) model.
// Tool-result reads are the main reduction target, so ToInternal normalizes the
// whole request but Render writes back only the reduced tool_result content into the
// original OpenAI message list — assistant tool_calls and plain text are left
// structurally intact. Ported from the reference prototype's openai_adapter.
type OpenAI struct{}

func (OpenAI) Name() string { return "openai" }

// openaiToken remembers what Render needs: the original request bytes (everything
// not rewritten survives verbatim) and tool_call_id → original message index.
type openaiToken struct {
	original []byte
	toolmap  map[string]int
}

func (OpenAI) ToInternal(body []byte) (canon.Request, RenderToken, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return canon.Request{}, nil, err
	}
	internal, toolmap := openaiToInternal(req)
	return canon.Request{Root: internal}, openaiToken{original: body, toolmap: toolmap}, nil
}

func (OpenAI) Render(req canon.Request, token RenderToken) ([]byte, error) {
	tok, ok := token.(openaiToken)
	if !ok {
		// No token (shouldn't happen via the engine) — fall back to re-encoding.
		return req.Encode()
	}
	var out map[string]any
	if err := json.Unmarshal(tok.original, &out); err != nil {
		return nil, err
	}
	openaiApplyBack(out, req.Root, tok.toolmap)
	return json.Marshal(out)
}

// contentString flattens OpenAI message content (string or array of text parts).
func contentString(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, p := range c {
			if pm, ok := p.(map[string]any); ok {
				if pm["type"] == "text" {
					if t, ok := pm["text"].(string); ok {
						parts = append(parts, t)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// openaiToInternal converts an OpenAI request to the Anthropic-shaped model and a
// {tool_call_id: openai_message_index} map.
func openaiToInternal(req map[string]any) (map[string]any, map[string]int) {
	var systemParts []string
	var aMsgs []any
	toolmap := map[string]int{}

	msgs, _ := req["messages"].([]any)
	for j, raw := range msgs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch m["role"] {
		case "system":
			systemParts = append(systemParts, contentString(m["content"]))
		case "tool":
			tcid, _ := m["tool_call_id"].(string)
			aMsgs = append(aMsgs, map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type":        "tool_result",
					"tool_use_id": tcid,
					"content":     contentString(m["content"]),
				}},
			})
			if tcid != "" {
				toolmap[tcid] = j
			}
		case "assistant":
			var blocks []any
			if txt := contentString(m["content"]); txt != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": txt})
			}
			if calls, ok := m["tool_calls"].([]any); ok {
				for _, tcRaw := range calls {
					tc, ok := tcRaw.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := tc["function"].(map[string]any)
					var input any = map[string]any{}
					if fn != nil {
						if argStr, ok := fn["arguments"].(string); ok && argStr != "" {
							var parsed any
							if err := json.Unmarshal([]byte(argStr), &parsed); err == nil {
								input = parsed
							}
						}
					}
					blk := map[string]any{"type": "tool_use", "id": tc["id"], "input": input}
					if fn != nil {
						blk["name"] = fn["name"]
					}
					blocks = append(blocks, blk)
				}
			}
			if len(blocks) == 0 {
				blocks = []any{map[string]any{"type": "text", "text": ""}}
			}
			aMsgs = append(aMsgs, map[string]any{"role": "assistant", "content": blocks})
		default: // user (or unknown) -> user text
			aMsgs = append(aMsgs, map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": contentString(m["content"])}},
			})
		}
	}

	var nonEmpty []string
	for _, p := range systemParts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	internal := map[string]any{
		"system":   strings.Join(nonEmpty, "\n"),
		"messages": aMsgs,
		"model":    req["model"],
	}
	return internal, toolmap
}

// openaiApplyBack copies reduced tool_result content from the canonical request back
// into the original OpenAI message list.
func openaiApplyBack(out map[string]any, reduced map[string]any, toolmap map[string]int) {
	outMsgs, _ := out["messages"].([]any)
	redMsgs, _ := reduced["messages"].([]any)
	for _, mRaw := range redMsgs {
		m, ok := mRaw.(map[string]any)
		if !ok {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, bRaw := range content {
			blk, ok := bRaw.(map[string]any)
			if !ok || blk["type"] != "tool_result" {
				continue
			}
			tcid, _ := blk["tool_use_id"].(string)
			j, ok := toolmap[tcid]
			if !ok || j < 0 || j >= len(outMsgs) {
				continue
			}
			if om, ok := outMsgs[j].(map[string]any); ok {
				om["content"] = blk["content"]
			}
		}
	}
}
