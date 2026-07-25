package expand

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// InjectMode controls whether the expand tool is advertised on outgoing requests.
//
//   - "auto" (default): inject only when the request already declares tools AND the
//     store can persist stashes. This is the safe default — it never perturbs a
//     request that uses no tools (the riskiest case for models that penalize an
//     unexpected tool), and never advertises a tool that can't be resolved.
//   - "always": inject whenever the store persists (create the tools array if absent).
//   - "never": never inject (the pre-D2 behavior; pair with marker_mode: summary).
const (
	InjectAuto   = "auto"
	InjectAlways = "always"
	InjectNever  = "never"
)

// Inject appends the expand tool definition to the request body's tools array in a
// byte-stable way, so a model that offloaded content can call context_guru_expand to
// get it back (closing the reversibility loop the proxy's continuation handler drives).
//
// It is idempotent (a request that already declares the tool is returned unchanged),
// appends the tool LAST so the client's own tools keep their exact order and the
// provider prefix cache stays warm, and respects a forcing tool_choice (skips, so it
// never changes which tool the model is compelled to call). Fail-open: any error
// returns the original body with injected=false.
func Inject(provider, mode string, body []byte, storePersists bool) (out []byte, injected bool) {
	if mode == InjectNever || !storePersists {
		return body, false
	}
	// Respect an explicit non-auto tool_choice: forcing/none means we must not
	// perturb tool selection.
	if tc := gjson.GetBytes(body, "tool_choice"); tc.Exists() {
		if !toolChoiceIsAuto(tc) {
			return body, false
		}
	}
	tools := gjson.GetBytes(body, "tools")
	hasTools := tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
	if mode == InjectAuto && !hasTools {
		return body, false
	}
	// Idempotent: skip if the expand tool is already present.
	nameField := "function.name"
	if provider == "anthropic" {
		nameField = "name"
	}
	for _, t := range tools.Array() {
		if t.Get(nameField).String() == ToolName {
			return body, false
		}
	}
	nb, err := sjson.SetRawBytes(body, "tools.-1", ToolDefRaw(provider))
	if err != nil {
		return body, false // fail open
	}
	return nb, true
}

// toolChoiceIsAuto reports whether a tool_choice value leaves the model free to
// choose (so injecting one more tool is safe). OpenAI: the string "auto". Anthropic:
// an object {"type":"auto"}. Anything else (none/required/any/a specific tool) is
// treated as forcing and injection is skipped.
func toolChoiceIsAuto(tc gjson.Result) bool {
	if tc.Type == gjson.String {
		return tc.String() == "auto"
	}
	if tc.IsObject() {
		return tc.Get("type").String() == "auto"
	}
	return false
}
