package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/internal/extract"
	"github.com/kagenti/lab-context-engineering/internal/markers"
	"github.com/kagenti/lab-context-engineering/internal/reduce"
	"github.com/kagenti/lab-context-engineering/internal/store"
	"github.com/kagenti/lab-context-engineering/internal/tokens"
)

const goalTurns = 6 // recent turns whose text conditions extraction + keep-set

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

// Extractor is the cheap-model extraction compactor. It does its OWN detection of large
// structured tool outputs in the conversation (independent of the reduce pass), projects
// each to a smaller faithful subset via the model, and splices it back with a reversible
// recovery marker. Fail-open per candidate. Implements Compactor.
type Extractor struct {
	spec  ModelSpec
	icfg  extract.Cfg
	cache *extract.Cache
}

// NewExtractor builds the extract compactor with the given model spec + config.
func NewExtractor(spec ModelSpec, cfg ExtractConfig) *Extractor {
	return &Extractor{spec: spec, icfg: cfg.internal(), cache: extract.NewCache()}
}

func (*Extractor) Name() string            { return extractName }
func (*Extractor) Enabled(c *Context) bool { return c.Settings.ExtractEnabled }

func (x *Extractor) Compact(req canon.Request, agg *Report, c *Context) (canon.Request, error) {
	model := x.spec.Resolve(c)
	if model == nil {
		return req, nil // no model available (e.g. incoming creds missing) — fail-open
	}
	opts := reduce.DefaultOpts()
	opts.ProtectRecent = c.Settings.ProtectRecent
	opts.ProtectRecentToolUses = c.Settings.ProtectRecentToolUses
	opts.ProvableOnly = c.Settings.ProvableOnly
	opts.ContextLimit = c.Settings.ContextLimit
	opts.ReduceCachedPrefix = c.Settings.ReduceCachedPrefix
	opts.CacheFloor = c.ClientCacheFloor
	opts.LLMCompactFloor = c.Settings.LLMCompactFloor
	opts.LLMCompactStructuredOnly = c.Settings.LLMCompactStructuredOnly

	cands := reduce.SelectLLMCandidates(req, opts)
	agg.Candidates = cands
	goal := recentGoalText(req, goalTurns)
	keep := extract.HarvestIdentifiers(goal, 60)
	for _, cand := range cands {
		func() {
			defer func() { _ = recover() }() // fail-open per candidate
			key := goalKey(extract.ContentKey(cand.Text), goal, keep)
			result, ok := x.cache.Get(key)
			if !ok {
				var strat string
				result, strat = extract.RunExtraction(c.GoCtx, cand.Text, goal, keep, cand.TokenEst, x.icfg, model)
				if strat == "none" || result == "" {
					return
				}
				x.cache.Put(key, result)
			}
			splice(req, cand, result, c.Store)
		}()
	}
	return req, nil
}

// EnableExtract registers the extract compactor backed by a static model (config
// source). Settings.ExtractMode / ExtractStrategies override cfg when set, so a config
// file fully drives the run.
func (e *Engine) EnableExtract(model Model, cfg ExtractConfig) {
	e.EnableExtractSpec(ModelSpec{Static: model}, cfg)
}

// EnableExtractSpec registers the extract compactor with an explicit ModelSpec — use
// ModelSpec{UseIncoming:true} to reuse the proxied request's own model + credentials.
func (e *Engine) EnableExtractSpec(spec ModelSpec, cfg ExtractConfig) {
	if e.settings.ExtractMode != "" {
		cfg.Mode = e.settings.ExtractMode
	}
	if len(e.settings.ExtractStrategies) > 0 {
		cfg.Strategies = e.settings.ExtractStrategies
	}
	e.settings.ExtractEnabled = true
	e.Register(extractName, NewExtractor(spec, cfg))
}

// goalKey makes the extraction cache goal-aware: the cache value is the FILTERED
// result, which depends on the goal and keep-set, not just the body. The key composites
// the body's content key with the goal and the keep-set so a different goal is a miss.
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
func splice(req canon.Request, c reduce.Candidate, result string, st store.Rewind) {
	block := blockAt(req, c.MsgIndex, c.BlockIndex)
	if block == nil {
		return
	}
	rid := st.Put(c.Text)
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
