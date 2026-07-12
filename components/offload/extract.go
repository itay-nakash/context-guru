package offload

import (
	"strings"

	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/expand"
	"github.com/kagenti/context-guru/schema"
	bschemas "github.com/maximhq/bifrost/core/schemas"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("extract", newExtract) }

// Extract projects a large tool output down to the part relevant to the current
// query, stashing the full original. v1 ships the deterministic strategy:
// keep lines matching the recent query's keywords (plus head/tail context and
// any error lines). The LLM strategies (code = model-generated Starlark filter
// with a containment validator; rlm = recursive chunked) are the winnow-parity
// refinement — they need a cheap-model client (ModelSpec), which is not yet
// wired; selecting them falls back to deterministic with a note.
type Extract struct {
	minTokens int
	head      int
	tail      int
	strategy  string
}

type extractConfig struct {
	MinTokens int    `yaml:"min_tokens"`
	Head      int    `yaml:"head_lines"`
	Tail      int    `yaml:"tail_lines"`
	Strategy  string `yaml:"strategy"` // deterministic | code | rlm
}

func newExtract(raw []byte) (components.Component, error) {
	cfg := extractConfig{MinTokens: 300, Head: 5, Tail: 5, Strategy: "deterministic"}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &Extract{minTokens: cfg.MinTokens, head: cfg.Head, tail: cfg.Tail, strategy: cfg.Strategy}, nil
}

func (Extract) Name() string                 { return "extract" }
func (Extract) Enabled(*components.Ctx) bool { return true }

// NeedsModel reports whether the configured strategy calls an LLM. The pipeline
// injects the ModelSpec when true. (The LLM path is not yet implemented; see the
// package note — this reports intent for when the cheap-model client lands.)
func (e *Extract) NeedsModel() bool { return e.strategy == "code" || e.strategy == "rlm" }

func (e *Extract) Offload(req *bschemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	query := keywords(lastUserText(req))
	if len(query) == 0 {
		rep.Skipped = true
		return nil, nil // nothing to condition relevance on
	}
	var keys []string
	for _, i := range toolIndices(req) {
		msg := &req.Input[i]
		if !schema.Rewritable(*msg) {
			continue // non-text blocks would be dropped by a text rewrite
		}
		content := schema.MessageText(*msg)
		if content == "" || schema.TextTokens(content) < e.minTokens {
			continue
		}
		if len(expand.ParseMarkers(content)) > 0 {
			continue
		}
		projected, ok := e.project(content, query)
		if !ok || schema.TextTokens(projected) >= schema.TextTokens(content) {
			continue
		}
		key := hashKey(content)
		c.Store.Put(key, []byte(content))
		schema.SetMessageText(msg, projected+"\n"+expand.Marker(key)+" [full output: call "+expand.ToolName+"]")
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		rep.Skipped = true
	}
	return keys, nil
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
