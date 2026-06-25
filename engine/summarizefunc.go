package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/internal/markers"
	"github.com/kagenti/lab-context-engineering/internal/summarize"
	"github.com/kagenti/lab-context-engineering/internal/tokens"
)

// SummarizeConfig configures the summarize compactor.
type SummarizeConfig struct {
	Level         string // concise | regular | highly_detailed
	KeepLast      int    // recent messages kept verbatim after the summary
	TriggerTokens int    // skip summarizing below this whole-conversation token count
}

// DefaultSummarizeConfig returns sensible summarizer defaults.
func DefaultSummarizeConfig() SummarizeConfig {
	return SummarizeConfig{Level: summarize.LevelRegular, KeepLast: 3}
}

// Summarizer is the trajectory-summarization compactor (ported from CE-Manager): it
// replaces the older messages with one LLM-produced summary and keeps the last KeepLast
// messages verbatim, storing the dropped span for recovery. Implements Compactor.
type Summarizer struct {
	spec ModelSpec
	cfg  SummarizeConfig
}

// NewSummarizer builds the summarize compactor with the given model spec + config.
func NewSummarizer(spec ModelSpec, cfg SummarizeConfig) *Summarizer {
	if cfg.KeepLast <= 0 {
		cfg.KeepLast = 3
	}
	if cfg.Level == "" {
		cfg.Level = summarize.LevelRegular
	}
	return &Summarizer{spec: spec, cfg: cfg}
}

func (*Summarizer) Name() string            { return summarizeName }
func (*Summarizer) Enabled(c *Context) bool { return c.Settings.SummarizeEnabled }

func (x *Summarizer) Compact(req canon.Request, agg *Report, c *Context) (canon.Request, error) {
	model := x.spec.Resolve(c)
	if model == nil {
		return req, nil // no model available — fail-open
	}
	msgs := req.Messages()
	if len(msgs) <= x.cfg.KeepLast {
		return req, nil
	}
	if x.cfg.TriggerTokens > 0 {
		if b, err := json.Marshal(msgs); err == nil && tokens.Count(string(b)) < x.cfg.TriggerTokens {
			return req, nil
		}
	}
	dropped := msgs[:len(msgs)-x.cfg.KeepLast]
	summary, err := summarize.Summarize(c.GoCtx, trajectoryString(dropped), x.cfg.Level, model)
	if err != nil {
		return req, err
	}
	b, _ := json.Marshal(dropped)
	rid := c.Store.Put(string(b))
	note := map[string]any{
		"role": "user",
		"content": "=== History Summary ===\nThe earlier trajectory is summarized below.\n\n" +
			summary + "\n\n" +
			markers.RecoveryNote(fmt.Sprintf("%d earlier message(s)", len(dropped)), "summarized", rid),
	}
	req.SetMessages(append([]map[string]any{note}, msgs[len(msgs)-x.cfg.KeepLast:]...))
	return req, nil
}

// EnableSummarize registers the summarize compactor backed by a static model (config
// source). Settings summarize knobs override cfg when set.
func (e *Engine) EnableSummarize(model Model, cfg SummarizeConfig) {
	e.EnableSummarizeSpec(ModelSpec{Static: model}, cfg)
}

// EnableSummarizeSpec registers the summarize compactor with an explicit ModelSpec —
// use ModelSpec{UseIncoming:true} to reuse the proxied request's own model + creds.
func (e *Engine) EnableSummarizeSpec(spec ModelSpec, cfg SummarizeConfig) {
	if e.settings.SummarizeLevel != "" {
		cfg.Level = e.settings.SummarizeLevel
	}
	if e.settings.SummarizeKeepLast > 0 {
		cfg.KeepLast = e.settings.SummarizeKeepLast
	}
	if e.settings.SummarizeTriggerTokens > 0 {
		cfg.TriggerTokens = e.settings.SummarizeTriggerTokens
	}
	e.settings.SummarizeEnabled = true
	e.Register(summarizeName, NewSummarizer(spec, cfg))
}

// trajectoryString flattens a message list into a "[role]\ncontent" block transcript
// for the summarizer prompt.
func trajectoryString(msgs []map[string]any) string {
	var b strings.Builder
	for _, m := range msgs {
		role, _ := m["role"].(string)
		b.WriteString("[" + role + "]\n")
		for _, blk := range canon.Blocks(m) {
			switch canon.BlockType(blk) {
			case "text":
				if t, ok := blk["text"].(string); ok {
					b.WriteString(t)
				}
			case "tool_use":
				if name, ok := blk["name"].(string); ok {
					b.WriteString("(tool_use: " + name + ")")
				}
			case "tool_result":
				switch cc := blk["content"].(type) {
				case string:
					b.WriteString(cc)
				default:
					if bb, err := json.Marshal(cc); err == nil {
						b.Write(bb)
					}
				}
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}
