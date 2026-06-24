package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/internal/extract"
	"github.com/kagenti/lab-context-engineering/internal/markers"
	"github.com/kagenti/lab-context-engineering/internal/reduce"
	"github.com/kagenti/lab-context-engineering/internal/tokens"
)

const goalTurns = 6 // recent turns whose text conditions extraction + keep-set

// Model is the cheap-model client extraction calls. A host (the proxy, or a Kagenti
// AuthBridge plugin) implements it; the engine has no model client of its own.
type Model interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// ExtractConfig configures cheap-model extraction (public mirror of the internal cfg).
type ExtractConfig struct {
	Mode               string  // "auto" | "single" | "rlm" | "deterministic"
	Floor              int     // token floor; rlm kicks in at max(floor*4,8000) in auto
	MinKeepRatio       float64 // 0 disables the blunt ratio backstop
	AllowDeterministic bool
	MaxChars           int
	// Strategies, when non-empty, restricts the strategy order to these names by-name
	// (code | single | rlm | deterministic). Empty means "all".
	Strategies []string
}

// DefaultExtractConfig returns sensible extraction defaults.
func DefaultExtractConfig() ExtractConfig {
	c := extract.DefaultCfg()
	return ExtractConfig{Mode: c.Mode, Floor: c.Floor, AllowDeterministic: c.AllowDeterministic, MaxChars: c.MaxChars}
}

func (c ExtractConfig) internal() extract.Cfg {
	return extract.Cfg{Mode: c.Mode, Floor: c.Floor, MinKeepRatio: c.MinKeepRatio,
		AllowDeterministic: c.AllowDeterministic, MaxChars: c.MaxChars,
		AllowedStrategies: c.Strategies}
}

// EnableExtract turns on cheap-model extraction with the given model and config. It
// builds the candidate→extraction→reversible-splice adapter and registers it; the
// Extract stage then runs after Reduce. Fail-open per candidate. Settings that name
// component selection (ExtractMode, ExtractStrategies) override cfg when set, so a
// config file fully drives the run.
func (e *Engine) EnableExtract(model Model, cfg ExtractConfig) {
	if e.settings.ExtractMode != "" {
		cfg.Mode = e.settings.ExtractMode
	}
	if len(e.settings.ExtractStrategies) > 0 {
		cfg.Strategies = e.settings.ExtractStrategies
	}
	icfg := cfg.internal()
	cache := extract.NewCache()
	e.settings.ExtractEnabled = true
	e.extract = func(ctx context.Context, req canon.Request, cands []reduce.Candidate) error {
		goal := recentGoalText(req, goalTurns)
		keep := extract.HarvestIdentifiers(goal, 60)
		for _, c := range cands {
			func() {
				defer func() { _ = recover() }() // fail-open per candidate
				key := goalKey(extract.ContentKey(c.Text), goal, keep)
				result, ok := cache.Get(key)
				if !ok {
					var strat string
					result, strat = extract.RunExtraction(ctx, c.Text, goal, keep, c.TokenEst, icfg, model)
					if strat == "none" || result == "" {
						return
					}
					cache.Put(key, result)
				}
				e.splice(req, c, result)
			}()
		}
		return nil
	}
}

// goalKey makes the extraction cache goal-aware: the cache value is the FILTERED
// result, which depends on the goal and keep-set, not just the body. Keying on body
// alone would let the same tool output re-read under a different goal reuse the first
// goal's filtered result. The key composites the body's content key with the goal and
// the keep-set so a different goal is a cache miss.
func goalKey(contentKey, goal string, keep []string) string {
	h := sha256.New()
	h.Write([]byte(contentKey))
	h.Write([]byte{0})
	h.Write([]byte(goal))
	for _, k := range keep {
		h.Write([]byte{0})
		h.Write([]byte(k))
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// splice replaces the candidate block with the extracted result plus a reversible
// recovery marker (original stored), only if that is strictly smaller.
func (e *Engine) splice(req canon.Request, c reduce.Candidate, result string) {
	block := blockAt(req, c.MsgIndex, c.BlockIndex)
	if block == nil {
		return
	}
	rid := e.store.Put(c.Text)
	label := c.FilePath
	if label == "" {
		label = c.ToolName
	}
	if label == "" {
		label = "tool output"
	}
	newText := strings.TrimRight(result, "\n") + "\n" + markers.RecoveryNote(label, "extracted", rid)
	if tokens.Count(newText) >= tokens.Count(c.Text) {
		return // never inflate
	}
	switch block["type"] {
	case "tool_result":
		block["content"] = newText
	case "text":
		block["text"] = newText
	}
}

func blockAt(req canon.Request, mi, bi int) map[string]any {
	msgs := req.Messages()
	if mi < 0 || mi >= len(msgs) {
		return nil
	}
	list, ok := msgs[mi]["content"].([]any)
	if !ok || bi < 0 || bi >= len(list) {
		return nil
	}
	blk, _ := list[bi].(map[string]any)
	return blk
}

// recentGoalText concatenates the text of the last k turns — the agent's current
// focus — excluding tool_result content (harvesting ids from the very output being
// extracted would defeat extraction).
func recentGoalText(req canon.Request, k int) string {
	msgs := req.Messages()
	start := len(msgs) - k
	if start < 0 {
		start = 0
	}
	var out []string
	for _, m := range msgs[start:] {
		switch c := m["content"].(type) {
		case string:
			out = append(out, c)
		case []any:
			for _, b := range c {
				if bb, ok := b.(map[string]any); ok && bb["type"] == "text" {
					if t, ok := bb["text"].(string); ok {
						out = append(out, t)
					}
				}
			}
		}
	}
	s := strings.Join(out, "\n")
	if len(s) > 6000 {
		s = s[:6000]
	}
	return s
}
