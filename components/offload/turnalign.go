package offload

import (
	bschemas "github.com/maximhq/bifrost/core/schemas"
)

// Turn alignment, shared by every component that replaces a SPAN of the transcript rather
// than editing messages in place.
//
// WHY IT IS ITS OWN FILE. Both summarization_llmd and cache_aware_summarizer cut a span with
// two boundaries, and both must respect the same invariant: a tool exchange is ATOMIC.
// Anthropic and OpenAI each reject a tool result whose tool_use is absent, and a tool_use
// whose result is absent, so a boundary landing mid-exchange produces a request the provider
// 400s. On this stack a 400 is recorded as an INFRASTRUCTURE exception rather than a
// compaction failure, which silently biases an A/B toward whichever arm makes shorter
// prompts — deleting exactly the long trajectories the method exists to serve.
//
// Keeping one copy is what lets two arms differ by ONE variable. A second implementation
// would be a second place for the invariant to drift, and the drift would show up as an
// exception-rate difference attributed to the method under test.

// alignHeadToTurn extends the pinned head forward so the summarized span never STARTS
// inside a run of tool results. Without it a head ending on the assistant message that
// requested the calls (or partway through its parallel results) leaves a tool call whose
// results were summarized away.
func alignHeadToTurn(msgs []bschemas.ChatMessage, head int) int {
	n := len(msgs)
	if head <= 0 || head >= n {
		return head
	}
	// Swallow the results belonging to a turn the head already opened.
	for head < n && msgs[head].Role == bschemas.ChatMessageRoleTool {
		head++
	}
	// If the head still ENDS on a message that requested tool calls, none of its results
	// followed (the loop above would have taken them), so it is dangling — drop it into
	// the span rather than send an unanswered tool call. A LOOP, not one decrement:
	// consecutive tool-calling assistant messages are malformed input, and malformed
	// input is exactly where a provider 400 comes from, so one pass is not enough.
	for head > 0 && hasToolCalls(msgs[head-1]) {
		head--
	}
	return head
}

// alignTailToTurn retracts the pinned tail backward so it never OPENS on a tool result
// whose tool call sits in the summarized span. It walks onto the message that made the
// calls, which keeps one extra (complete) turn — the safe direction.
func alignTailToTurn(msgs []bschemas.ChatMessage, tail, head int) int {
	for tail > head && tail < len(msgs) && msgs[tail].Role == bschemas.ChatMessageRoleTool {
		tail--
	}
	return tail
}

// hasToolCalls reports whether m requested tool calls as bifrost models them.
//
// ⚠️ OpenAI-shaped traffic ONLY. bifrost does not map Anthropic `tool_use` content
// blocks onto ToolCalls (same limitation agentdiet's serializeStep documents), so on the
// Anthropic path this is always false and the role=tool run scan is what protects the
// boundary. That scan is sufficient there, because apply's normalize turns each
// Anthropic tool_result block into a synthetic role=tool message.
func hasToolCalls(m bschemas.ChatMessage) bool {
	return m.ChatAssistantMessage != nil && len(m.ChatAssistantMessage.ToolCalls) > 0
}
