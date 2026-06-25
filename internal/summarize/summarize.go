// Package summarize produces a compact, factual summary of an agent trajectory. The
// prompts are ported verbatim from the CE-Manager / ACE "ReSum" summarizer. The
// engine's summarize compactor flattens the conversation to a trajectory string, calls
// Summarize, and rebuilds the message list around the result.
package summarize

import (
	"context"
	"errors"
	"strings"
)

// Model is the LLM client the summarizer calls. The host injects a concrete
// implementation; this core stays transport-free.
type Model interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// Summary verbosity levels.
const (
	LevelConcise        = "concise"
	LevelRegular        = "regular"
	LevelHighlyDetailed = "highly_detailed"
)

// systemPrompt and userPrompt are verbatim from the ReSum summarizer (prompts/summarizer.py).
const systemPrompt = "You analyze long agent trajectories with tool calls and produce compact, factual summaries. Do not guess or invent information."

const userPrompt = `You are an expert at analyzing conversation history and extracting relevant information. Your task is
to thoroughly evaluate the conversation history and current question to provide a comprehensive
summary that will help answer the question.

Task Guidelines:
1. Information Analysis
• Carefully analyze the conversation history to identify truly useful information.
• Focus on information that directly contributes to answering the question.
• Do NOT make assumptions, guesses, or inferences beyond what is explicitly stated in
the conversation.
• If information is missing or unclear, do NOT include it in your summary.

2. Summary Requirements
• Extract only the most relevant information that is explicitly present in the conversation.
• Synthesize information from multiple exchanges when relevant. Only include information that is certain and clearly stated in the conversation.
• Do NOT output or mention any information that is uncertain, insufficient, or cannot be
confirmed from the conversation.

3. Output Format Your response should be structured as follows:
<summary>
• Essential Information: [Organize the relevant and certain information from the conversation history that helps address the question.]
</summary>

Strictly avoid fabricating, inferring, or exaggerating any information not present in the conversation.
Only output information that is certain and explicitly stated.

Trajectory: {trajectory}
`

const (
	suffixConcise  = "Please generate a concise summary"
	suffixRegular  = "Please generate a comprehensive and useful summary"
	suffixDetailed = "Please generate a highly detailed, fully comprehensive, explicitly grounded summary " +
		"that includes every relevant and certain piece of information from the conversation."
)

func suffixFor(level string) string {
	switch level {
	case LevelConcise:
		return suffixConcise
	case LevelHighlyDetailed:
		return suffixDetailed
	default: // regular (and unknown)
		return suffixRegular
	}
}

// Prompt builds the (system, user) prompt pair for a trajectory at a verbosity level.
func Prompt(trajectory, level string) (system, user string) {
	user = strings.ReplaceAll(userPrompt, "{trajectory}", trajectory)
	if sfx := suffixFor(level); sfx != "" {
		user = user + "\n" + sfx
	}
	return systemPrompt, user
}

// Summarize asks the model to summarize the trajectory and returns the result wrapped
// in <summary>...</summary>. The Model interface takes a single prompt, so the system
// instruction is prepended to the user prompt.
func Summarize(ctx context.Context, trajectory, level string, model Model) (string, error) {
	if model == nil {
		return "", errors.New("summarize: no model")
	}
	system, user := Prompt(trajectory, level)
	out, err := model.Complete(ctx, system+"\n\n"+user)
	if err != nil {
		return "", err
	}
	return EnsureFormat(out), nil
}

// EnsureFormat guarantees the summary is wrapped in <summary>...</summary> tags.
func EnsureFormat(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "<summary>Empty trajectory</summary>"
	}
	if !strings.Contains(s, "<summary>") {
		s = "<summary>\n" + s
	}
	if !strings.Contains(s, "</summary>") {
		s = s + "\n</summary>"
	}
	return s
}
