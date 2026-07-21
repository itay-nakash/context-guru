package offload

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/schema"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("collapse", newCollapse) }

// Collapse is the content-agnostic fallback for an oversized tool output that no
// more specific component handled: it keeps a head + tail window, stashes the
// full original, and leaves an expand marker. Runs late in the pipeline (after
// cmdfilter/format), and skips anything already carrying a marker so it never
// double-collapses.
type Collapse struct {
	maxTokens int
	headLines int
	tailLines int
	mode      markerMode
}

type collapseConfig struct {
	MaxTokens  int    `yaml:"max_tokens"`
	HeadLines  int    `yaml:"head_lines"`
	TailLines  int    `yaml:"tail_lines"`
	MarkerMode string `yaml:"marker_mode"` // full (default) | summary | off
}

func newCollapse(raw []byte) (components.Component, error) {
	cfg := collapseConfig{MaxTokens: 2000, HeadLines: 20, TailLines: 20}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &Collapse{maxTokens: cfg.MaxTokens, headLines: cfg.HeadLines, tailLines: cfg.TailLines, mode: parseMarkerMode(cfg.MarkerMode)}, nil
}

func (Collapse) Name() string                 { return "collapse" }
func (Collapse) Enabled(*components.Ctx) bool { return true }

func (cl *Collapse) Offload(req *schemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	var keys []string
	changed := 0
	for i := range req.Input {
		m := &req.Input[i]
		if m.Role != schemas.ChatMessageRoleTool {
			continue
		}
		if !schema.Rewritable(*m) {
			continue // non-text blocks would be dropped by a text rewrite
		}
		content := schema.MessageText(*m)
		if content == "" || schema.TextTokens(content) <= cl.maxTokens {
			continue
		}
		if expand.HasPlaceholder(content) {
			continue // already offloaded by an earlier component
		}
		lines := strings.Split(content, "\n")
		if len(lines) <= cl.headLines+cl.tailLines {
			continue // few long lines; head/tail wouldn't help
		}
		omitted := len(lines) - cl.headLines - cl.tailLines
		tok, key := mark(c, rep, cl.mode, content, " [full output: call "+expand.ToolName+"]")
		var b strings.Builder
		b.WriteString(strings.Join(lines[:cl.headLines], "\n"))
		fmt.Fprintf(&b, "\n... (%d lines omitted) ", omitted)
		b.WriteString(tok + "\n")
		b.WriteString(strings.Join(lines[len(lines)-cl.tailLines:], "\n"))
		schema.SetMessageText(m, b.String())
		changed++
		if key != "" {
			keys = append(keys, key)
		}
	}
	if changed == 0 {
		rep.Skipped = true
	}
	return keys, nil
}
