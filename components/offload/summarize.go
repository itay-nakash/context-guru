package offload

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/expand"
	"github.com/kagenti/context-guru/schema"
	bschemas "github.com/maximhq/bifrost/core/schemas"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("summarize", newSummarize) }

// summarizeCallTimeout bounds the single summarizer call. It is higher than
// extract's ceiling because summarize makes ONE call over a large span, and a
// big trajectory legitimately takes the model longer to read and compress.
const summarizeCallTimeout = 150 * time.Second

// maxTrajectoryChars caps the trajectory text sent to the summarizer so a very
// large span still fits the model's context window (≈70k tokens, well under a
// 200k window with room for the summary) instead of erroring and failing open.
// The most recent turns are kept; the full original span is always stashed for
// expand, so the older prefix is never lost — only left out of THIS summary.
const maxTrajectoryChars = 280_000

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
	level             string
	keepLast          int
	minTokens         int
	resummarizeTokens int
	includeToolCalls  bool
	modelSource       string
	trigger           components.Trigger
	mode              markerMode
}

type summarizeConfig struct {
	SummaryLevel string `yaml:"summary_level"`      // concise | regular | highly_detailed
	KeepLast     int    `yaml:"keep_last"`          // messages kept verbatim at the tail
	StartFrom    int    `yaml:"start_from_message"` // legacy: folds into trigger.min_messages
	MinTokens    int    `yaml:"min_tokens"`         // min content tokens in the span to bother
	// ResummarizeTokens: once a summary exists, reuse it (no LLM call) until the
	// un-summarized tail since the last checkpoint grows past this many tokens,
	// then roll the checkpoint forward with a fresh summary. 0 = re-summarize
	// every eligible turn (old behavior).
	ResummarizeTokens int  `yaml:"resummarize_tokens"`
	IncludeToolCalls  bool `yaml:"include_tool_calls"`
	Model             struct {
		Source string `yaml:"source"` // incoming (default) | config
	} `yaml:"model"`
	Trigger    components.Trigger `yaml:"trigger"`
	MarkerMode string             `yaml:"marker_mode"` // full (default) | summary | off
}

func newSummarize(raw []byte) (components.Component, error) {
	cfg := summarizeConfig{SummaryLevel: "regular", KeepLast: 3, StartFrom: 6, MinTokens: 500, ResummarizeTokens: 6000}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	// Legacy start_from_message is a message-count gate; the canonical knob is
	// trigger.min_messages. Fold one into the other so both work.
	if cfg.Trigger.MinMessages == 0 {
		cfg.Trigger.MinMessages = cfg.StartFrom
	}
	return &Summarize{
		level: cfg.SummaryLevel, keepLast: cfg.KeepLast,
		minTokens: cfg.MinTokens, resummarizeTokens: cfg.ResummarizeTokens,
		includeToolCalls: cfg.IncludeToolCalls, modelSource: cfg.Model.Source, trigger: cfg.Trigger,
		mode: parseMarkerMode(cfg.MarkerMode),
	}, nil
}

func (Summarize) Name() string                 { return "summarize" }
func (Summarize) Enabled(*components.Ctx) bool { return true }
func (*Summarize) NeedsModel() bool            { return true }

func (s *Summarize) Offload(req *bschemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	msgs := req.Input
	// Keep msg0 (system/first) + the last keepLast; summarize the span between.
	start, end := 1, len(msgs)-s.keepLast
	// Request-level trigger: don't summarize (an LLM call) until the transcript
	// is genuinely large / deep. Zero thresholds fire always (back-compat).
	if !s.trigger.Fires(req) || end <= start {
		rep.Skipped = true
		return nil, nil
	}
	model := c.Model.For(s.modelSource)
	if model == nil {
		rep.Skipped = true // NeedsModel but none available → degrade gracefully
		return nil, nil
	}

	// Reuse a prior summary if the covered prefix is unchanged and the tail since
	// that checkpoint is still small — no LLM call, and the summary message stays
	// byte-identical (KV-cache stable). Roll the checkpoint forward only once the
	// tail grows past resummarize_tokens.
	if out, keys, ok := s.tryReuse(c, msgs, start, end); ok {
		if len(keys) == 0 {
			rep.Irreversible = true // reused a non-full checkpoint (nothing stashed)
		}
		req.Input = out
		return keys, nil
	}

	span := msgs[start:end]
	if schema.MessagesTokens(&bschemas.BifrostChatRequest{Input: span}) < s.minTokens {
		rep.Skipped = true
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(c.Ctx, summarizeCallTimeout)
	defer cancel()
	summary, err := s.summarize(ctx, model, span, conversationGoal(req))
	if err != nil {
		return nil, err // fail-open: the pipeline reverts this component
	}
	if strings.TrimSpace(summary) == "" {
		rep.Skipped = true
		return nil, nil
	}

	// Stash the replaced span so expand can restore it — full mode only. In
	// summary/off there is no restoration; flag the deliberate lossy drop so the
	// pipeline's dropped-without-stash guard permits it.
	var key string
	if s.mode == markerFull {
		spanJSON, err := json.Marshal(span)
		if err != nil {
			return nil, err
		}
		key = hashKey(string(spanJSON))
		c.Store.Put(key, spanJSON)
	} else {
		rep.Irreversible = true
	}

	summaryText := summaryWrapper(summary, key, s.mode)
	summaryMsg := bschemas.ChatMessage{Role: bschemas.ChatMessageRoleSystem}
	schema.SetMessageText(&summaryMsg, summaryText)

	// Checkpoint: this summary subsumes the leading span (len(span) messages from
	// index 1). A later turn appends messages, so this same prefix stays stable.
	saveCheckpoint(c, sumCheckpoint{
		SummaryMsg: summaryText, CoveredCount: end - start,
		CoveredHash: spanHash(span), Key: key,
	})

	// [msg0, summary, last-K] — reassign; apply.Body rebuilds losslessly.
	out := make([]bschemas.ChatMessage, 0, 2+s.keepLast)
	out = append(out, msgs[0], summaryMsg)
	out = append(out, msgs[end:]...)
	req.Input = out
	if key != "" {
		return []string{key}, nil
	}
	return nil, nil
}

// tryReuse re-emits the previous summary if (1) a checkpoint exists, (2) the
// covered prefix (msgs[1:1+CoveredCount]) is byte-unchanged, and (3) the tail
// since that boundary is below resummarize_tokens. It returns the rebuilt
// [msg0, priorSummary, msgs[boundary:]] and the (refreshed) stash key. No LLM
// call. ok=false means "re-summarize fresh".
func (s *Summarize) tryReuse(c *components.Ctx, msgs []bschemas.ChatMessage, start, end int) ([]bschemas.ChatMessage, []string, bool) {
	if s.resummarizeTokens <= 0 {
		return nil, nil, false
	}
	cp, ok := loadCheckpoint(c)
	if !ok || cp.CoveredCount <= 0 {
		return nil, nil, false
	}
	boundary := start + cp.CoveredCount
	if boundary > end { // covered prefix would overlap the kept tail — can't reuse
		return nil, nil, false
	}
	covered := msgs[start:boundary]
	if spanHash(covered) != cp.CoveredHash {
		return nil, nil, false // prefix diverged (different session / edited) → fresh
	}
	// The un-summarized middle since the checkpoint (excludes the kept last-K).
	if schema.MessagesTokens(&bschemas.BifrostChatRequest{Input: msgs[boundary:end]}) >= s.resummarizeTokens {
		return nil, nil, false // grown enough — roll the checkpoint forward
	}
	// Refresh the stashed original span so expand keeps resolving it (full-mode
	// checkpoints only — summary/off never stashed, so Key is empty).
	if cp.Key != "" {
		if b, err := json.Marshal(covered); err == nil {
			c.Store.Put(cp.Key, b)
		}
	}
	summaryMsg := bschemas.ChatMessage{Role: bschemas.ChatMessageRoleSystem}
	schema.SetMessageText(&summaryMsg, cp.SummaryMsg)
	out := make([]bschemas.ChatMessage, 0, 2+(len(msgs)-boundary))
	out = append(out, msgs[0], summaryMsg)
	out = append(out, msgs[boundary:]...)
	if cp.Key != "" {
		return out, []string{cp.Key}, true
	}
	return out, nil, true
}

// summarize builds the trajectory string and asks the model once (bounded retry).
// goal is the current task + recent turns, so the summary is grounded in what the
// agent is actually trying to do — not a blind digest of the middle.
func (s *Summarize) summarize(ctx context.Context, model components.Model, span []bschemas.ChatMessage, goal string) (string, error) {
	sys := summarizerSystemPrompt
	if !s.includeToolCalls {
		sys += summarizerMaskedNote
	}
	user := strings.Replace(summarizerUserPrompt, "{trajectory}", trajectoryString(span, s.includeToolCalls), 1)
	if g := strings.TrimSpace(goal); g != "" {
		user = "CURRENT TASK / QUESTION (summarize toward this):\n" + g + "\n\n" + user
	}
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
	s := b.String()
	// Keep the trajectory within the model's context window: if it's huge, keep
	// the most recent portion (the full original span is stashed for expand).
	if len(s) > maxTrajectoryChars {
		s = "…[older trajectory omitted from this summary; full original preserved for expand]\n\n" +
			s[len(s)-maxTrajectoryChars:]
	}
	return s
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

// summaryWrapper is the synthetic system message that replaces the span. In full
// mode it carries the <<cg:HASH>> marker so the expand tool can recover the full
// original trajectory; in summary mode a non-resolvable ⟪cg⟫ sentinel; in off
// mode nothing (the summary text itself is the only trace).
func summaryWrapper(summary, key string, mode markerMode) string {
	body := "=== History Summary ===\n" +
		"The earlier trajectory is summarized below.\n\n" +
		summary + "\n\n" +
		"Use this summary as the older context, and use the following messages as the most recent context. " +
		"Continue the task accordingly. Do not summarize the conversation again."
	switch mode {
	case markerFull:
		return body + "\n" + expand.Marker(key) + " [full earlier trajectory: call " + expand.ToolName + "]"
	case markerSummary:
		return body + "\n" + expand.SummaryMarker
	default: // off
		return body
	}
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
