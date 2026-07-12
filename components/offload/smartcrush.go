package offload

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/expand"
	"github.com/kagenti/context-guru/schema"
	bschemas "github.com/maximhq/bifrost/core/schemas"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("smartcrush", newSmartCrush) }

// SmartCrush is headroom's statistical JSON-array compressor, in essence: keep a
// first-K/last-K anchor window plus any item that carries an error signal, drop
// the rest, and stash the full original. Schema-preserving (kept items are
// verbatim originals). v1 uses fixed anchors; headroom's Kneedle adaptive-K is a
// documented refinement.
type SmartCrush struct {
	minItems  int
	minTokens int
	keepFirst int
	keepLast  int
}

type smartCrushConfig struct {
	MinItems  int `yaml:"min_items"`
	MinTokens int `yaml:"min_tokens"`
	KeepFirst int `yaml:"keep_first"`
	KeepLast  int `yaml:"keep_last"`
}

func newSmartCrush(raw []byte) (components.Component, error) {
	cfg := smartCrushConfig{MinItems: 5, MinTokens: 200, KeepFirst: 3, KeepLast: 2}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &SmartCrush{minItems: cfg.MinItems, minTokens: cfg.MinTokens, keepFirst: cfg.KeepFirst, keepLast: cfg.KeepLast}, nil
}

func (SmartCrush) Name() string                 { return "smartcrush" }
func (SmartCrush) Enabled(*components.Ctx) bool { return true }

func (s *SmartCrush) Offload(req *bschemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	var keys []string
	for _, i := range toolIndices(req) {
		msg := &req.Input[i]
		if !schema.Rewritable(*msg) {
			continue // non-text blocks would be dropped by a text rewrite
		}
		content := schema.MessageText(*msg)
		trimmed := strings.TrimSpace(content)
		if len(trimmed) == 0 || trimmed[0] != '[' || schema.TextTokens(content) < s.minTokens {
			continue
		}
		var items []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &items); err != nil || len(items) < s.minItems {
			continue
		}
		keep := s.keepSet(items)
		if len(keep) >= len(items) {
			continue // nothing to drop
		}
		kept := make([]json.RawMessage, 0, len(keep))
		for idx := range items {
			if _, ok := keep[idx]; ok {
				kept = append(kept, items[idx])
			}
		}
		crushed, err := json.Marshal(kept)
		if err != nil {
			continue
		}
		key := hashKey(content)
		c.Store.Put(key, []byte(content))
		note := fmt.Sprintf(" [%d of %d items shown; full array: call %s] %s",
			len(kept), len(items), expand.ToolName, expand.Marker(key))
		schema.SetMessageText(msg, string(crushed)+note)
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		rep.Skipped = true
	}
	return keys, nil
}

// keepSet is the set of item indices to preserve: first-K, last-K, and any item
// whose raw JSON carries an error signal.
func (s *SmartCrush) keepSet(items []json.RawMessage) map[int]struct{} {
	keep := map[int]struct{}{}
	for i := 0; i < s.keepFirst && i < len(items); i++ {
		keep[i] = struct{}{}
	}
	for i := len(items) - s.keepLast; i < len(items); i++ {
		if i >= 0 {
			keep[i] = struct{}{}
		}
	}
	for i, it := range items {
		if hasError(string(it)) {
			keep[i] = struct{}{}
		}
	}
	return keep
}
