package offload

import (
	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/schema"
	"github.com/maximhq/bifrost/core/schemas"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("dedup", newDedup) }

// Dedup replaces a tool output that is byte-identical to an earlier one in the
// same request with a short pointer + expand marker, stashing the original.
// Exact-match only in v1; near-duplicate (similarity threshold) is deferred.
type Dedup struct {
	minTokens int
	mode      markerMode
}

type dedupConfig struct {
	MinTokens  int    `yaml:"min_tokens"`
	MarkerMode string `yaml:"marker_mode"` // full (default) | summary | off
}

func newDedup(raw []byte) (components.Component, error) {
	cfg := dedupConfig{MinTokens: 100}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &Dedup{minTokens: cfg.MinTokens, mode: parseMarkerMode(cfg.MarkerMode)}, nil
}

func (Dedup) Name() string                 { return "dedup" }
func (Dedup) Enabled(*components.Ctx) bool { return true }

func (d *Dedup) Offload(req *schemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	seen := map[string]int{} // content hash -> first message index
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
		if content == "" || schema.TextTokens(content) < d.minTokens {
			continue
		}
		h := hashKey(content)
		if _, dup := seen[h]; !dup {
			seen[h] = i
			continue
		}
		// Later duplicate: collapse to a pointer (stash+marker in full mode).
		tok, key := mark(c, rep, d.mode, content, "")
		schema.SetMessageText(m, "[identical to an earlier tool output] "+tok)
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
