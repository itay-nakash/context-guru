package offload

import (
	"regexp"

	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/schema"
	"github.com/maximhq/bifrost/core/schemas"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("failed_run", newFailedRun) }

// runMarkers identify a tool output that is a test/build run — the kind that is
// superseded when the agent re-runs after a fix. Deliberately broad; false
// positives only cost an expand round-trip, never data (the original is stashed).
var runMarkers = regexp.MustCompile(`(?i)(\d+ (passed|failed|error)|BUILD (SUCCESS|FAIL)|=+ (FAILURES|test session)|Traceback \(most recent|\bFAILED\b|\bpanic:|\bnpm ERR!)`)

// FailedRun collapses earlier test/build runs that a later run supersedes: only
// the most recent run-like tool output is kept in full; earlier ones become a
// pointer + stash. This is the "provable-reason" collapse — a superseded run is
// safely recoverable via expand if the agent still needs it.
type FailedRun struct {
	minTokens int
	mode      markerMode
}

type failedRunConfig struct {
	MinTokens  int    `yaml:"min_tokens"`
	MarkerMode string `yaml:"marker_mode"` // full (default) | summary | off
}

func newFailedRun(raw []byte) (components.Component, error) {
	cfg := failedRunConfig{MinTokens: 100}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &FailedRun{minTokens: cfg.MinTokens, mode: parseMarkerMode(cfg.MarkerMode)}, nil
}

func (FailedRun) Name() string                 { return "failed_run" }
func (FailedRun) Enabled(*components.Ctx) bool { return true }

func (fr *FailedRun) Offload(req *schemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	// Find indices of run-like tool outputs, in order.
	var runs []int
	for i := range req.Input {
		m := req.Input[i]
		if m.Role != schemas.ChatMessageRoleTool {
			continue
		}
		if !schema.Rewritable(m) {
			continue // non-text blocks would be dropped by a text rewrite
		}
		content := schema.MessageText(m)
		if schema.TextTokens(content) < fr.minTokens {
			continue
		}
		if expand.HasPlaceholder(content) {
			continue // already offloaded
		}
		if runMarkers.MatchString(content) {
			runs = append(runs, i)
		}
	}
	if len(runs) < 2 {
		rep.Skipped = true
		return nil, nil
	}
	// Keep the last run in full; collapse every earlier one.
	var keys []string
	for _, i := range runs[:len(runs)-1] {
		m := &req.Input[i]
		content := schema.MessageText(*m)
		tok, key := mark(c, rep, fr.mode, content, " [full output: call "+expand.ToolName+"]")
		schema.SetMessageText(m, "[superseded by a later run] "+tok)
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys, nil
}
