package cheapmodel

import "sync/atomic"

// Usage tracks cumulative token usage of the cheap (config-source) model across
// all NeedsModel component calls in this process. It is the basis for reporting
// the CONTEXT-GURU components' OWN LLM cost (e.g. extract:code's Starlark-writer
// calls) separately from the agent's cost — the proxy exposes it at /stats and the
// benchmark prices it. Process-global (there is a single cheap model per proxy);
// per-component attribution would need the Model interface to carry a label, a
// deferred refinement — today the LLM component in a config is extract, so the
// global total is that component's cost.
var (
	llmCalls        atomic.Int64
	llmInputTokens  atomic.Int64
	llmOutputTokens atomic.Int64
)

// recordUsage adds one call's token usage to the process totals.
func recordUsage(inTok, outTok int) {
	llmCalls.Add(1)
	llmInputTokens.Add(int64(inTok))
	llmOutputTokens.Add(int64(outTok))
}

// Usage returns the cumulative cheap-model usage (calls, input tokens, output
// tokens) since process start.
func Usage() (calls, inTokens, outTokens int64) {
	return llmCalls.Load(), llmInputTokens.Load(), llmOutputTokens.Load()
}
