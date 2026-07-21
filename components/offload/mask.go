package offload

import (
	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/schema"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("mask", newMask) }

// Mask hides older tool outputs beyond a keep-recent window (after CE-Manager's
// context garbage collection): the newest KeepRecent tool results stay verbatim,
// older ones are replaced with a short marker + stash. Age-based, complementary
// to the content-based offloaders.
type Mask struct {
	keepRecent int
	minTokens  int
	mode       markerMode
}

type maskConfig struct {
	KeepRecent int    `yaml:"keep_recent"`
	MinTokens  int    `yaml:"min_tokens"`
	MarkerMode string `yaml:"marker_mode"` // full (default) | summary | off
}

func newMask(raw []byte) (components.Component, error) {
	cfg := maskConfig{KeepRecent: 3, MinTokens: 100}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &Mask{keepRecent: cfg.KeepRecent, minTokens: cfg.MinTokens, mode: parseMarkerMode(cfg.MarkerMode)}, nil
}

func (Mask) Name() string                 { return "mask" }
func (Mask) Enabled(*components.Ctx) bool { return true }

func (m *Mask) Offload(req *bschemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	tools := toolIndices(req)
	if len(tools) <= m.keepRecent {
		rep.Skipped = true
		return nil, nil
	}
	var keys []string
	changed := 0
	// Mask every tool output except the most recent keepRecent.
	for _, i := range tools[:len(tools)-m.keepRecent] {
		msg := &req.Input[i]
		if !schema.Rewritable(*msg) {
			continue // non-text blocks would be dropped by a text rewrite
		}
		content := schema.MessageText(*msg)
		if content == "" || schema.TextTokens(content) < m.minTokens {
			continue
		}
		if expand.HasPlaceholder(content) {
			continue
		}
		tok, key := mark(c, rep, m.mode, content, " [full output: call "+expand.ToolName+"]")
		schema.SetMessageText(msg, "[older tool output masked] "+tok)
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
