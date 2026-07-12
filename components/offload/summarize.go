package offload

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/expand"
	"github.com/kagenti/context-guru/schema"
	bschemas "github.com/maximhq/bifrost/core/schemas"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("summarize", newSummarize) }

// Summarize compresses the middle of a long trajectory into one LLM-written
// summary (ported from CE-Manager's ReSum-style summarizer). It restructures the
// message list to [msg0, <summary system message>, last-K messages], replacing
// everything in between. It is an Offload: the replaced span is stashed under a
// <<cg:HASH>> marker (carried in the summary message) so the expand tool can
// restore it. NeedsModel — it no-ops when no model is available.
//
// This is the one component that changes the message count; apply.Body rebuilds
// the body preserving the retained messages' original bytes.
type Summarize struct {
	level            string
	keepLast         int
	startFrom        int
	minTokens        int
	includeToolCalls bool
	modelSource      string
}

type summarizeConfig struct {
	SummaryLevel     string `yaml:"summary_level"`      // concise | regular | highly_detailed
	KeepLast         int    `yaml:"keep_last"`          // messages kept verbatim at the tail
	StartFrom        int    `yaml:"start_from_message"` // no-op until the list reaches this length
	MinTokens        int    `yaml:"min_tokens"`         // min content tokens in the span to bother
	IncludeToolCalls bool   `yaml:"include_tool_calls"`
	Model            struct {
		Source string `yaml:"source"` // incoming (default) | config
	} `yaml:"model"`
}

func newSummarize(raw []byte) (components.Component, error) {
	cfg := summarizeConfig{SummaryLevel: "regular", KeepLast: 3, StartFrom: 6, MinTokens: 500}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &Summarize{
		level: cfg.SummaryLevel, keepLast: cfg.KeepLast, startFrom: cfg.StartFrom,
		minTokens: cfg.MinTokens, includeToolCalls: cfg.IncludeToolCalls, modelSource: cfg.Model.Source,
	}, nil
}

func (Summarize) Name() string                 { return "summarize" }
func (Summarize) Enabled(*components.Ctx) bool { return true }
func (*Summarize) NeedsModel() bool            { return true }

func (s *Summarize) Offload(req *bschemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	msgs := req.Input
	// Keep msg0 (system/first) + the last keepLast; summarize the span between.
	start, end := 1, len(msgs)-s.keepLast
	if len(msgs) < s.startFrom || end <= start {
		rep.Skipped = true
		return nil, nil
	}
	model := c.Model.For(s.modelSource)
	if model == nil {
		rep.Skipped = true // NeedsModel but none available → degrade gracefully
		return nil, nil
	}
	span := msgs[start:end]
	if schema.MessagesTokens(&bschemas.BifrostChatRequest{Input: span}) < s.minTokens {
		rep.Skipped = true
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(c.Ctx, llmCallTimeout)
	defer cancel()
	summary, err := s.summarize(ctx, model, span)
	if err != nil {
		return nil, err // fail-open: the pipeline reverts this component
	}
	if strings.TrimSpace(summary) == "" {
		rep.Skipped = true
		return nil, nil
	}

	// Stash the replaced span so expand can restore it.
	spanJSON, err := json.Marshal(span)
	if err != nil {
		return nil, err
	}
	key := hashKey(string(spanJSON))
	c.Store.Put(key, spanJSON)

	summaryMsg := bschemas.ChatMessage{Role: bschemas.ChatMessageRoleSystem}
	schema.SetMessageText(&summaryMsg, summaryWrapper(summary, key))

	// [msg0, summary, last-K] — reassign; apply.Body rebuilds losslessly.
	out := make([]bschemas.ChatMessage, 0, 2+s.keepLast)
	out = append(out, msgs[0], summaryMsg)
	out = append(out, msgs[end:]...)
	req.Input = out
	return []string{key}, nil
}

// summarize builds the trajectory string and asks the model once (bounded retry).
func (s *Summarize) summarize(ctx context.Context, model components.Model, span []bschemas.ChatMessage) (string, error) {
	sys := summarizerSystemPrompt
	if !s.includeToolCalls {
		sys += summarizerMaskedNote
	}
	user := strings.Replace(summarizerUserPrompt, "{trajectory}", trajectoryString(span, s.includeToolCalls), 1)
	if suffix := summaryLevelSuffix[s.level]; suffix != "" {
		user += "\n" + suffix
	}
	prompt := sys + "\n\n" + user
	var lastErr error
	for i := 0; i < 3; i++ {
		out, err := model.Complete(ctx, prompt)
		if err != nil {
			lastErr = err
			continue
		}
		return ensureSummaryTags(out), nil
	}
	return "", lastErr
}

// trajectoryString renders the span as "[role]\n{content}" blocks. When tool
// calls are excluded, tool-role content is replaced by a placeholder.
func trajectoryString(span []bschemas.ChatMessage, includeToolCalls bool) string {
	var b strings.Builder
	for i, m := range span {
		if i > 0 {
			b.WriteString("\n\n")
		}
		content := schema.MessageText(m)
		if !includeToolCalls && m.Role == bschemas.ChatMessageRoleTool {
			content = "<masked_tool_output>"
		}
		b.WriteString("[")
		b.WriteString(string(m.Role))
		b.WriteString("]\n")
		b.WriteString(content)
	}
	return b.String()
}

func ensureSummaryTags(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if !strings.Contains(s, "<summary>") {
		s = "<summary>\n" + s
	}
	if !strings.Contains(s, "</summary>") {
		s = s + "\n</summary>"
	}
	return s
}

// summaryWrapper is the synthetic system message that replaces the span; it
// carries the marker so the expand tool can recover the full original trajectory.
func summaryWrapper(summary, key string) string {
	return "=== History Summary ===\n" +
		"The earlier trajectory is summarized below.\n\n" +
		summary + "\n\n" +
		"Use this summary as the older context, and use the following messages as the most recent context. " +
		"Continue the task accordingly. Do not summarize the conversation again.\n" +
		expand.Marker(key) + " [full earlier trajectory: call " + expand.ToolName + "]"
}

// Prompts ported verbatim from CE-Manager (src/ce_manager/prompts/summarizer.py).
const summarizerSystemPrompt = "You analyze long agent trajectories with tool calls and produce compact, factual summaries. Do not guess or invent information."

const summarizerMaskedNote = " Notice, all the tool calls content have been removed from the trajectory, you should base your summary only on the remaining content."

const summarizerUserPrompt = `You are an expert at analyzing conversation history and extracting relevant information. Your task is to thoroughly evaluate the conversation history and current question to provide a comprehensive summary that will help answer the question.

Task Guidelines:
1. Information Analysis
• Carefully analyze the conversation history to identify truly useful information.
• Focus on information that directly contributes to answering the question.
• Do NOT make assumptions, guesses, or inferences beyond what is explicitly stated in the conversation.
• If information is missing or unclear, do NOT include it in your summary.

2. Summary Requirements
• Extract only the most relevant information that is explicitly present in the conversation.
• Synthesize information from multiple exchanges when relevant. Only include information that is certain and clearly stated in the conversation.
• Do NOT output or mention any information that is uncertain, insufficient, or cannot be confirmed from the conversation.

3. Output Format Your response should be structured as follows:
<summary>
• Essential Information: [Organize the relevant and certain information from the conversation history that helps address the question.]
</summary>

Strictly avoid fabricating, inferring, or exaggerating any information not present in the conversation. Only output information that is certain and explicitly stated.

Trajectory: {trajectory}`

var summaryLevelSuffix = map[string]string{
	"concise":         "Please generate a concise summary",
	"regular":         "Please generate a comprehensive and useful summary",
	"highly_detailed": "Please generate a highly detailed, fully comprehensive, explicitly grounded summary that includes every relevant and certain piece of information from the conversation.",
}
