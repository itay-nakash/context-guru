package offload

import (
	"fmt"
	"strings"

	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/expand"
	"github.com/kagenti/context-guru/schema"
	"github.com/maximhq/bifrost/core/schemas"
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
}

type collapseConfig struct {
	MaxTokens int `yaml:"max_tokens"`
	HeadLines int `yaml:"head_lines"`
	TailLines int `yaml:"tail_lines"`
}

func newCollapse(raw []byte) (components.Component, error) {
	cfg := collapseConfig{MaxTokens: 2000, HeadLines: 20, TailLines: 20}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &Collapse{maxTokens: cfg.MaxTokens, headLines: cfg.HeadLines, tailLines: cfg.TailLines}, nil
}

func (Collapse) Name() string                 { return "collapse" }
func (Collapse) Enabled(*components.Ctx) bool { return true }

func (cl *Collapse) Offload(req *schemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	var keys []string
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
		if len(expand.ParseMarkers(content)) > 0 {
			continue // already offloaded by an earlier component
		}
		lines := strings.Split(content, "\n")
		if len(lines) <= cl.headLines+cl.tailLines {
			continue // few long lines; head/tail wouldn't help
		}
		omitted := len(lines) - cl.headLines - cl.tailLines
		var b strings.Builder
		b.WriteString(strings.Join(lines[:cl.headLines], "\n"))
		fmt.Fprintf(&b, "\n... (%d lines omitted) ", omitted)
		key := hashKey(content)
		b.WriteString(expand.Marker(key) + " [full output: call " + expand.ToolName + "]\n")
		b.WriteString(strings.Join(lines[len(lines)-cl.tailLines:], "\n"))
		c.Store.Put(key, []byte(content))
		schema.SetMessageText(m, b.String())
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		rep.Skipped = true
	}
	return keys, nil
}
