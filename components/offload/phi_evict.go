package offload

import (
	"sort"

	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/expand"
	"github.com/kagenti/context-guru/schema"
	bschemas "github.com/maximhq/bifrost/core/schemas"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("phi_evict", newPhiEvict) }

// PhiEvict ranks tool outputs by a lean-ctx-style context-field score Φ and
// offloads the lowest-scoring ones until the transcript fits a token budget.
//
//	Φ = wR·relevance + wH·recency − wC·cost − wD·redundancy
//
// This is the scalar-ranking essence of lean-ctx's Context Field; the full
// heat-diffusion/PageRank/Thompson-bandit machinery is a documented refinement.
// The MMR/Lost-in-the-Middle reorder is a separate ordering concern (deferred);
// here Φ drives eviction, the reduction. The most recent tool output is never
// evicted (the agent is most likely to need it).
type PhiEvict struct {
	budget  int
	weights weights
}

type weights struct{ R, H, C, D float64 }

type phiConfig struct {
	BudgetTokens int    `yaml:"budget_tokens"`
	Weights      string `yaml:"weights"` // balanced | aggressive | conservative
}

func newPhiEvict(raw []byte) (components.Component, error) {
	cfg := phiConfig{BudgetTokens: 120000, Weights: "balanced"}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &PhiEvict{budget: cfg.BudgetTokens, weights: presetWeights(cfg.Weights)}, nil
}

func presetWeights(name string) weights {
	switch name {
	case "aggressive": // punish cost harder
		return weights{R: 0.30, H: 0.10, C: 0.45, D: 0.15}
	case "conservative": // trust relevance/recency, light cost
		return weights{R: 0.45, H: 0.20, C: 0.05, D: 0.05}
	default: // balanced (lean-ctx defaults, cost/redundancy folded)
		return weights{R: 0.40, H: 0.20, C: 0.30, D: 0.10}
	}
}

func (PhiEvict) Name() string                 { return "phi_evict" }
func (PhiEvict) Enabled(*components.Ctx) bool { return true }

type scored struct {
	idx    int
	tokens int
	phi    float64
}

func (p *PhiEvict) Offload(req *bschemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	tools := toolIndices(req)
	total := 0
	for _, i := range tools {
		total += schema.TextTokens(schema.MessageText(req.Input[i]))
	}
	if total <= p.budget || len(tools) <= 1 {
		rep.Skipped = true
		return nil, nil
	}

	query := keywords(lastUserText(req))
	items := make([]scored, 0, len(tools))
	seen := map[string]struct{}{}
	for pos, i := range tools {
		content := schema.MessageText(req.Input[i])
		tk := schema.TextTokens(content)
		recency := float64(pos) / float64(len(tools)-1) // 0..1, newest = 1
		cost := 0.0
		if total > 0 {
			cost = float64(tk) / float64(total)
		}
		redundancy := 0.0
		if h := hashKey(content); h != "" {
			if _, dup := seen[h]; dup {
				redundancy = 1
			}
			seen[h] = struct{}{}
		}
		phi := p.weights.R*overlap(query, content) + p.weights.H*recency -
			p.weights.C*cost - p.weights.D*redundancy
		items = append(items, scored{idx: i, tokens: tk, phi: phi})
	}

	// Evict lowest Φ first; never the most recent tool output.
	newest := tools[len(tools)-1]
	sort.SliceStable(items, func(a, b int) bool { return items[a].phi < items[b].phi })

	var keys []string
	for _, it := range items {
		if total <= p.budget {
			break
		}
		if it.idx == newest {
			continue
		}
		msg := &req.Input[it.idx]
		if !schema.Rewritable(*msg) {
			continue // non-text blocks would be dropped by a text rewrite
		}
		content := schema.MessageText(*msg)
		if len(expand.ParseMarkers(content)) > 0 {
			continue
		}
		key := hashKey(content)
		c.Store.Put(key, []byte(content))
		schema.SetMessageText(msg, "[evicted to fit context budget] "+expand.Marker(key)+" [full output: call "+expand.ToolName+"]")
		keys = append(keys, key)
		total -= it.tokens
	}
	if len(keys) == 0 {
		rep.Skipped = true
	}
	return keys, nil
}
