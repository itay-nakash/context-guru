package offload

import (
	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/expand"
	"github.com/kagenti/context-guru/schema"
	bschemas "github.com/maximhq/bifrost/core/schemas"
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
}

type maskConfig struct {
	KeepRecent int `yaml:"keep_recent"`
	MinTokens  int `yaml:"min_tokens"`
}

func newMask(raw []byte) (components.Component, error) {
	cfg := maskConfig{KeepRecent: 3, MinTokens: 100}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &Mask{keepRecent: cfg.KeepRecent, minTokens: cfg.MinTokens}, nil
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
		if len(expand.ParseMarkers(content)) > 0 {
			continue
		}
		key := hashKey(content)
		c.Store.Put(key, []byte(content))
		schema.SetMessageText(msg, "[older tool output masked] "+expand.Marker(key)+" [full output: call "+expand.ToolName+"]")
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		rep.Skipped = true
	}
	return keys, nil
}
