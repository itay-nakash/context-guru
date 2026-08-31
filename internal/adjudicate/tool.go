// Package adjudicate declares the context-maintenance tool the cold-sweep adjudication asks with, and
// the wire helpers that keep it byte-stable in the prompt-cache prefix.
//
// WHY A TOOL AT ALL, rather than only asking for a JSON array in the reply text. Measured against the
// live gateway with the same prefix and only tool_choice varying:
//
//	tool_choice          reply shape                 cache            verdict coverage
//	{"type":"none"}      prose / thinking only       read 8,268 (free)   0 of 6 -- no answer at all
//	{"type":"tool",name} tool_use                    MISS, wrote 8,378   6 of 6
//	(omitted)            tool_use                    read 8,268 (free)   6 of 6, on 4 of 4 trials
//
// Three things follow, and each is the opposite of an assumption this repo carried in
// cheapmodel.CompletePrefixed:
//
//   - Setting tool_choice:none to STOP the model answering with a tool_use is what forced it into
//     PROSE. A sampled reply reasoned correctly under the criterion and said so in sentences ("the task
//     is not yet complete, and no summary of this raw data has been recorded elsewhere") -- which the
//     contract itself calls a valid answer -- and was then scored as an unparseable failure.
//   - FORCING the tool is not free: naming one produced a separate cache entry (8,378 written against
//     the 8,268 already cached), so tool_choice DOES participate in the key when it names a tool, even
//     though `none` does not.
//   - Merely DECLARING the tool, with no tool_choice at all, gets a schema-shaped answer covering the
//     whole batch at cache-read price.
//
// The tool is therefore injected on EVERY request rather than only when the sweep is about to ask.
// `tools` hashes before system and messages, so a tool that appears and disappears invalidates the
// prefix from position zero -- the same flap expand's `always` mode exists to prevent.
//
// This does not replace the text path. extract.ParseVerdicts and extract.BuildFallbackAsk are still
// the fallback, and a model that answers in prose anyway is still read exactly as before; the tool
// only changes which reply shape is PREFERRED.
package adjudicate

import (
	"strconv"
	"sync/atomic"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// strayAnswered counts tool_results rewritten because the AGENT called this tool. Surfaced in /stats
// as adjudicate_stray: the rate is the only signal for whether the description is doing its job.
// MEASURED at 0 across ~4,900 requests with the description below, which is why the answer text is a
// cheap insurance policy rather than a hot path.
var strayAnswered atomic.Int64

// StrayAnswered returns how many stray calls have been answered.
func StrayAnswered() int64 { return strayAnswered.Load() }

// ToolName is the wire name. Prefixed like the expand tool so an operator reading a transcript can
// tell at a glance which tools the proxy injected and which the client owns.
const ToolName = "context_guru_adjudicate"

// StrayAnswer is what a stray call from the AGENT gets. The model does call an advertised tool it was
// told to leave alone -- directly observed with context_guru_expand, which a run called at step 2 --
// and the client cannot execute a tool the proxy injected, so it answers something like
// "Tool 'context_guru_adjudicate' not found" and the agent loses a turn to a dead end. This gives it a
// definite, uninteresting answer instead.
const StrayAnswer = "Context maintenance runs automatically in the background. No action is required " +
	"from you, and you do not need to call this tool. Continue with the task."

// toolDesc tells the model who the tool is for. "Do not call it yourself" does not GUARANTEE it will
// not (see StrayAnswer), but it measured 0 strays in ~4,900 requests and costs nothing.
const toolDesc = "Internal to the context manager. Reports which earlier tool outputs are spent and " +
	"safe to remove from the transcript. This is invoked by the context manager, not by you - do not " +
	"call it yourself."

// schemaJSON constrains the answer, and its field names are exactly extract.Verdict's JSON tags so the
// existing parser reads a tool input unchanged.
//
// The label is a small INTEGER, never the tool_use id: asked for opaque ids the model REGULARISED them
// (answering toolu_01..07 for toolu_probe_00..07), because reproducing a random identifier from
// thousands of tokens back is a copying task rather than a judgement. With integer labels it was 0 bad
// labels across 40+ trials.
const schemaJSON = `{"type":"object","properties":{"verdicts":{"type":"array","description":` +
	`"One entry per label you were shown. Answer for EVERY label.","items":{"type":"object","properties":{` +
	`"i":{"type":"integer","description":"The label you were shown."},` +
	`"needed_by":{"type":"string","enum":["a","b","c","none"],"description":` +
	`"Which outstanding obligation still needs this output, or none if it is spent."},` +
	`"quote":{"type":"string","description":"Verbatim transcript text creating that obligation; empty when needed_by is none."},` +
	`"verdict":{"type":"string","enum":["keep","drop"],"description":"drop requires needed_by to be none."}},` +
	`"required":["i","needed_by","verdict"]}}},"required":["verdicts"]}`

// anthropicDef and openAIDef are the tool definitions, kept as raw JSON so injection is a byte splice
// and the cached prefix stays stable to the byte -- a re-marshalled map would be free to reorder keys.
const anthropicDef = `{"name":"` + ToolName + `","description":"` + toolDesc + `","input_schema":` + schemaJSON + `}`

const openAIDef = `{"type":"function","function":{"name":"` + ToolName + `","description":"` + toolDesc +
	`","parameters":` + schemaJSON + `}}`

// ToolDefRaw returns the provider-shaped tool definition.
func ToolDefRaw(provider string) []byte {
	if provider == "anthropic" {
		return []byte(anthropicDef)
	}
	return []byte(openAIDef)
}

// HasTool reports whether body already declares the tool. This is the ADVERTISE test, and a host must
// answer stray calls exactly when it is true.
func HasTool(provider string, body []byte) bool {
	field := "function.name"
	if provider == "anthropic" {
		field = "name"
	}
	for _, t := range gjson.GetBytes(body, "tools").Array() {
		if t.Get(field).String() == ToolName {
			return true
		}
	}
	return false
}

// Inject appends the tool to body's tools array, byte-stably and idempotently.
//
// Appended LAST so the client's own tools keep their exact order, and skipped when a forcing
// tool_choice is present so tool selection is never perturbed. Also skipped when the request declares
// no tools at all: handing the model its first tool changes what it believes it can do, and that is
// the riskiest case for a model that penalizes an unexpected tool. Fail-open — any trouble returns the
// original body.
func Inject(provider string, body []byte) (out []byte, injected bool) {
	if tc := gjson.GetBytes(body, "tool_choice"); tc.Exists() && !toolChoiceIsAuto(tc) {
		return body, false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() || len(tools.Array()) == 0 {
		return body, false
	}
	if HasTool(provider, body) {
		return body, false
	}
	nb, err := sjson.SetRawBytes(body, "tools.-1", ToolDefRaw(provider))
	if err != nil {
		return body, false // fail open
	}
	return nb, true
}

// toolChoiceIsAuto reports whether a tool_choice leaves the model free to choose. OpenAI: the string
// "auto". Anthropic: {"type":"auto"}. Anything else (none/required/any/a named tool) is forcing, and
// injection is skipped.
func toolChoiceIsAuto(tc gjson.Result) bool {
	if tc.Type == gjson.String {
		return tc.String() == "auto"
	}
	if tc.IsObject() {
		return tc.Get("type").String() == "auto"
	}
	return false
}

// AnswerStrayCalls replaces the client's tool_result for any call the AGENT made to this tool with a
// definite answer, and reports how many it replaced.
//
// Same request-path shape as expand.RepairToolResults, and for the same reason: the client executes
// the real tools itself, finds no tool by this name because the PROXY injected it, and answers
// something like "Tool 'context_guru_adjudicate' not found". Left alone, the model reads a failure it
// cannot act on and may retry. Rewriting it on the next request works WITH the client's loop instead
// of against it, needs nothing implemented client-side, and is deterministic — the same substitution
// every turn, so the prefix does not flap.
//
// Reading the tool_use out of the transcript rather than trusting the tool_result is what makes this
// safe: a client's tool_result is rewritten only when the assistant turn it answers called OUR tool.
// A body with no such call comes back byte-identical, because this runs on every request and a
// gratuitous rewrite would change the prefix and cost a cache write for nothing.
func AnswerStrayCalls(provider string, body []byte) (out []byte, answered int) {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body, 0
	}
	// An id-less tool_use would make "" a live key, and then a tool_result carrying no tool_use_id --
	// someone else's block -- would match it and be overwritten. Both halves of the pair must name an id.
	ours := map[string]bool{}
	for _, m := range msgs.Array() {
		if provider == "anthropic" {
			for _, blk := range m.Get("content").Array() {
				if blk.Get("type").String() == "tool_use" && blk.Get("name").String() == ToolName {
					if id := blk.Get("id").String(); id != "" {
						ours[id] = true
					}
				}
			}
			continue
		}
		for _, tc := range m.Get("tool_calls").Array() {
			if tc.Get("function.name").String() == ToolName {
				if id := tc.Get("id").String(); id != "" {
					ours[id] = true
				}
			}
		}
	}
	if len(ours) == 0 {
		return body, 0
	}
	out = body
	for mi, m := range msgs.Array() {
		base := "messages." + strconv.Itoa(mi)
		if provider != "anthropic" {
			if m.Get("role").String() != "tool" || !ours[m.Get("tool_call_id").String()] {
				continue
			}
			if nb, err := sjson.SetBytes(out, base+".content", StrayAnswer); err == nil {
				out, answered = nb, answered+1
			}
			continue
		}
		for bi, blk := range m.Get("content").Array() {
			if blk.Get("type").String() != "tool_result" || !ours[blk.Get("tool_use_id").String()] {
				continue
			}
			path := base + ".content." + strconv.Itoa(bi)
			nb, err := sjson.SetBytes(out, path+".content", StrayAnswer)
			if err != nil {
				continue
			}
			out, answered = nb, answered+1
			// The block is no longer an error: leaving is_error set tells the model its own call
			// failed while handing it the answer to that call.
			if blk.Get("is_error").Exists() {
				if nb, err := sjson.SetBytes(out, path+".is_error", false); err == nil {
					out = nb
				}
			}
		}
	}
	if answered > 0 {
		strayAnswered.Add(int64(answered))
	}
	return out, answered
}
