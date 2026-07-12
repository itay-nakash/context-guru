package expand

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Call is one model request to expand offloaded content: CallID identifies the
// tool call (for the continuation's tool_result), HashID is the store key.
type Call struct {
	CallID string
	HashID string
}

// ResponseCalls parses a provider response for expand tool calls. otherTools is
// true if the model also called some OTHER tool — in that case the host cannot
// safely auto-continue (the client must resolve the other tools), so the loop
// bails and returns the response as-is.
func ResponseCalls(provider string, resp []byte) (calls []Call, otherTools bool) {
	switch provider {
	case "anthropic":
		gjson.GetBytes(resp, "content").ForEach(func(_, blk gjson.Result) bool {
			if blk.Get("type").String() != "tool_use" {
				return true
			}
			if blk.Get("name").String() == ToolName {
				calls = append(calls, Call{CallID: blk.Get("id").String(), HashID: blk.Get("input.id").String()})
			} else {
				otherTools = true
			}
			return true
		})
	default: // openai and compatibles
		gjson.GetBytes(resp, "choices.0.message.tool_calls").ForEach(func(_, tc gjson.Result) bool {
			if tc.Get("function.name").String() == ToolName {
				hash := gjson.Get(tc.Get("function.arguments").String(), "id").String()
				calls = append(calls, Call{CallID: tc.Get("id").String(), HashID: hash})
			} else {
				otherTools = true
			}
			return true
		})
	}
	return calls, otherTools
}

// Continuation builds the next request body: it appends the assistant's
// tool-call turn and a tool_result turn carrying each resolved original, so the
// model can finish with the full content in hand. resolved maps CallID ->
// original text. ok=false if the shapes weren't as expected (caller returns the
// response unchanged — fail open).
func Continuation(provider string, reqBody, resp []byte, resolved map[string]string) ([]byte, bool) {
	switch provider {
	case "anthropic":
		content := gjson.GetBytes(resp, "content")
		if !content.Exists() {
			return nil, false
		}
		asst, err := sjson.SetRaw(`{"role":"assistant"}`, "content", content.Raw)
		if err != nil {
			return nil, false
		}
		out, err := sjson.SetRawBytes(reqBody, "messages.-1", []byte(asst))
		if err != nil {
			return nil, false
		}
		user := `{"role":"user","content":[]}`
		for callID, orig := range resolved {
			blk, _ := sjson.Set(`{"type":"tool_result"}`, "tool_use_id", callID)
			blk, _ = sjson.Set(blk, "content", orig)
			user, _ = sjson.SetRaw(user, "content.-1", blk)
		}
		out, err = sjson.SetRawBytes(out, "messages.-1", []byte(user))
		if err != nil {
			return nil, false
		}
		return out, true

	default: // openai
		msg := gjson.GetBytes(resp, "choices.0.message")
		if !msg.Exists() {
			return nil, false
		}
		out, err := sjson.SetRawBytes(reqBody, "messages.-1", []byte(msg.Raw))
		if err != nil {
			return nil, false
		}
		for callID, orig := range resolved {
			tool, _ := sjson.Set(`{"role":"tool"}`, "tool_call_id", callID)
			tool, _ = sjson.Set(tool, "content", orig)
			out, err = sjson.SetRawBytes(out, "messages.-1", []byte(tool))
			if err != nil {
				return nil, false
			}
		}
		return out, true
	}
}
