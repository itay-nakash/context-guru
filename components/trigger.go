package components

import (
	"github.com/rossoctl/context-guru/schema"
	"github.com/maximhq/bifrost/core/schemas"
)

// Trigger is the shared, configurable gate that decides whether an expensive
// (LLM-based) component should ACT on a request — so summarize/extract run only
// when it's worth an LLM call, not on every turn. It is embedded in a
// component's config as `trigger:` and works for any agent/benchmark/use-case
// because the thresholds are pure request shape (tokens and message count), not
// task-specific.
//
// A zero field is "no constraint", so the zero Trigger fires always (backward
// compatible with configs that don't set it). Request-level thresholds
// (MinRequestTokens, MinMessages) are checked by Fires; the per-item
// MinOutputTokens floor is checked by the component against each candidate
// (e.g. extract, per tool output).
type Trigger struct {
	MinRequestTokens int `yaml:"min_request_tokens"` // whole request must be at least this many tokens
	MinMessages      int `yaml:"min_messages"`       // …and carry at least this many messages (≈ steps)
	MinOutputTokens  int `yaml:"min_output_tokens"`  // per-item floor: only offload an output at least this big
}

// Fires reports whether the request-level thresholds are met. Both are ANDed;
// a zero threshold imposes no constraint. It does not consider MinOutputTokens
// (that is a per-item floor the component applies itself).
func (t Trigger) Fires(req *schemas.BifrostChatRequest) bool {
	if t.MinMessages > 0 && len(req.Input) < t.MinMessages {
		return false
	}
	if t.MinRequestTokens > 0 && schema.MessagesTokens(req) < t.MinRequestTokens {
		return false
	}
	return true
}
