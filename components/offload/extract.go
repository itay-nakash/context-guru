package offload

import (
	"context"
	"strings"
	"time"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/internal/extract"
	"github.com/rossoctl/context-guru/schema"
	"gopkg.in/yaml.v3"
)

// llmCallTimeout bounds a single in-request extract model call so a slow/hung
// model fails open (the component falls back / reverts) instead of stalling the
// agent. extract can make several calls per request (one per large tool output),
// so this ceiling stays modest; the per-content result cache keeps repeats free.
const llmCallTimeout = 60 * time.Second

func init() { components.Register("extract", newExtract) }

// Extract projects a large tool output down to the part relevant to the current
// query, stashing the full original. Three strategies:
//   - deterministic (default, no LLM): keep query-keyword + head/tail + error lines.
//   - code: a cheap LLM writes a Starlark `extract_relevant_data` filter, run in a
//     sandbox (no imports/IO, step+time limited); the output is accepted only if a
//     containment + sanity check proves it's a lossless projection, else it falls
//     back to deterministic. (winnow's llm_compact, ported.)
//   - rlm: reserved for very large outputs; currently maps to code.
//
// The LLM strategies need a Model (Ctx.Model, per model.source); when none is
// available they degrade to deterministic. The full original is always stashed
// under a marker, so any reduction is reversible via the expand tool.
type Extract struct {
	minTokens   int
	head        int
	tail        int
	strategy    string
	modelSource string
	modelClient components.Model // config-pinned client (model: block), or nil
	trigger     components.Trigger
	mode        markerMode
	rewrite     bool
}

type extractConfig struct {
	MinTokens  int                `yaml:"min_tokens"`
	Head       int                `yaml:"head_lines"`
	Tail       int                `yaml:"tail_lines"`
	Strategy   string             `yaml:"strategy"` // deterministic | code | rlm
	Model      modelConfig        `yaml:"model"`
	Trigger    components.Trigger `yaml:"trigger"`
	MarkerMode string             `yaml:"marker_mode"` // full (default) | summary | off
	// Rewrite (code strategy only): drop the deletion-only containment proof so the
	// model may reword/summarize/rewrite. Lossy + unverified — pair with a non-full
	// marker_mode. Default false keeps the verified deletion-only guarantee.
	Rewrite bool `yaml:"rewrite"`
}

func newExtract(raw []byte) (components.Component, error) {
	cfg := extractConfig{MinTokens: 300, Head: 5, Tail: 5, Strategy: "deterministic"}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	// Legacy min_tokens is the per-output floor; the canonical knob is
	// trigger.min_output_tokens. Fold one into the other so both work.
	if cfg.Trigger.MinOutputTokens == 0 {
		cfg.Trigger.MinOutputTokens = cfg.MinTokens
	}
	return &Extract{minTokens: cfg.MinTokens, head: cfg.Head, tail: cfg.Tail, strategy: cfg.Strategy, modelSource: cfg.Model.Source, modelClient: cfg.Model.Client(), trigger: cfg.Trigger, mode: parseMarkerMode(cfg.MarkerMode), rewrite: cfg.Rewrite}, nil
}

// outputFloor is the minimum tokens a single tool output must have to be worth
// offloading (the "large output" trigger).
func (e *Extract) outputFloor() int {
	if e.trigger.MinOutputTokens > 0 {
		return e.trigger.MinOutputTokens
	}
	return e.minTokens
}

func (Extract) Name() string                 { return "extract" }
func (Extract) Enabled(*components.Ctx) bool { return true }

// NeedsModel reports whether the configured strategy calls an LLM.
func (e *Extract) NeedsModel() bool { return e.strategy == "code" || e.strategy == "rlm" }

func (e *Extract) Offload(req *bschemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	// Request-level trigger: for the LLM strategies, don't spend a model call
	// until the request is genuinely large / deep. Deterministic runs always
	// (it's cheap). Zero thresholds fire always (backward compatible).
	if e.NeedsModel() && !e.trigger.Fires(req) {
		rep.Skipped = true
		return nil, nil
	}
	goal := conversationGoal(req) // full task + recent turns, not one trailing sentence
	query := keywords(goal)
	if len(query) == 0 {
		rep.Skipped = true
		return nil, nil // nothing to condition relevance on
	}
	var model components.Model
	if e.NeedsModel() {
		if model = e.modelClient; model == nil { // config-pinned client wins
			model = c.Model.For(e.modelSource)
		}
	}
	floor := e.outputFloor()
	keepIDs := extract.HarvestIdentifiers(goal, 40)
	var keys []string
	changed := 0
	for _, i := range toolIndices(req) {
		msg := &req.Input[i]
		if !schema.Rewritable(*msg) {
			continue // non-text blocks would be dropped by a text rewrite
		}
		content := schema.MessageText(*msg)
		if content == "" || schema.TextTokens(content) < floor {
			continue
		}
		if expand.HasPlaceholder(content) {
			continue
		}
		// Reuse a prior compaction of this exact output (marker/whitespace
		// insensitive) — no LLM call, and the same bytes keep the prefix stable.
		id := extract.ContentKey(content)
		projected, ok := "", false
		if cached, hit := getResult(c, id); hit {
			projected, ok = string(cached), true
		} else if projected, ok = e.reduce(c, content, goal, keepIDs, query, model); ok {
			putResult(c, id, []byte(projected))
		}
		if !ok || schema.TextTokens(projected) >= schema.TextTokens(content) {
			continue
		}
		tok, key := mark(c, rep, e.mode, content, " [full output: call "+expand.ToolName+"]")
		schema.SetMessageText(msg, projected+"\n"+tok)
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

// reduce picks the strategy: for code/rlm with a model, run the sandboxed
// LLM-generated filter (containment-validated inside RunExtraction, with its own
// deterministic fallback); otherwise the deterministic line projection. Always
// fail-open — any miss falls through to project().
func (e *Extract) reduce(c *components.Ctx, content, goal string, keepIDs []string, query map[string]struct{}, model components.Model) (string, bool) {
	if model != nil && e.NeedsModel() {
		cfg := extract.DefaultCfg()
		cfg.Mode = "code" // rlm is deferred → use the Starlark code strategy
		cfg.Floor = e.outputFloor()
		cfg.Rewrite = e.rewrite
		ctx, cancel := context.WithTimeout(c.Ctx, llmCallTimeout)
		res, _ := extract.RunExtraction(ctx, content, goal, keepIDs, schema.TextTokens(content), cfg, model)
		cancel()
		if res != "" && res != content {
			return res, true
		}
	}
	return e.project(content, query)
}

// project keeps head/tail context plus every line relevant to the query or
// carrying an error, collapsing the runs between with an ellipsis marker.
func (e *Extract) project(content string, query map[string]struct{}) (string, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) <= e.head+e.tail+1 {
		return "", false
	}
	keep := make([]bool, len(lines))
	for i := 0; i < e.head && i < len(lines); i++ {
		keep[i] = true
	}
	for i := len(lines) - e.tail; i < len(lines); i++ {
		if i >= 0 {
			keep[i] = true
		}
	}
	for i, ln := range lines {
		if hasError(ln) || overlap(query, ln) > 0 {
			keep[i] = true
		}
	}
	var b strings.Builder
	gap := false
	for i, ln := range lines {
		if keep[i] {
			if gap {
				b.WriteString("…\n")
				gap = false
			}
			b.WriteString(ln)
			b.WriteByte('\n')
		} else {
			gap = true
		}
	}
	return strings.TrimRight(b.String(), "\n"), true
}
