package offload

import (
	"regexp"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/schema"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("failed_run", newFailedRun) }

// runMarkers identify a tool output that is a test/build run — the kind that is
// superseded when the agent re-runs after a fix. Deliberately broad; false
// positives only cost an expand round-trip, never data (the original is stashed).
var runMarkers = regexp.MustCompile(`(?i)(\d+ (passed|failed|error)|BUILD (SUCCESS|FAIL)|=+ (FAILURES|test session)|Traceback \(most recent|\bFAILED\b|\bpanic:|\bnpm ERR!)`)

// failMarkers identify a run that FAILED. Only a failed earlier run is safely
// "superseded" by a later run (the agent fixed it and moved on); a PASSED/successful
// earlier run is a distinct result the agent may still reference (e.g. `pytest test_a`
// passing, then `pytest test_b`), so it is kept verbatim. Restricting collapse to
// failures is what keeps failed_run from hiding a still-relevant successful result —
// a general, agent-agnostic safety rule.
// Note: the count must be NON-ZERO ("0 failed" is a PASS, not a failure); the bare
// pytest "FAILED <path>" token is matched CASE-SENSITIVELY so a lowercase "0 failed"
// summary doesn't trip it.
var failMarkers = regexp.MustCompile(`(?i)([1-9]\d* (failed|error(s|ed)?)\b|build fail|=+ failures|traceback \(most recent|\bpanic:|\bnpm err!|\bexit(ed with)? (code )?[1-9])|(?-i:\bFAILED\b)`)

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
	// Keep the last run in full; collapse every earlier one THAT FAILED. A passed/
	// successful earlier run is a distinct result the agent may still reference, so
	// it stays verbatim (only genuinely-superseded failures are collapsed).
	var keys []string
	changed := 0
	for _, i := range runs[:len(runs)-1] {
		m := &req.Input[i]
		content := schema.MessageText(*m)
		// Reapply a previously-frozen collapse on EVERY turn (cache-stable), regardless
		// of the tail boundary — the agent re-sends the original, so we must re-collapse
		// it to the same bytes or it reverts to full and churns the cache.
		if fk, saved, ok := reapplyFrozen(c, fr.Name(), m); ok {
			rep.TokensBefore += saved // (report best-effort; pipeline recomputes exact)
			changed++
			keys = append(keys, fk...)
			continue
		}
		if isKeptVerbatim(c, contentKey(content)) {
			continue // agent expanded this superseded run; leave it verbatim (no bounce)
		}
		if !failMarkers.MatchString(content) {
			continue // earlier run succeeded — not superseded, keep it
		}
		// A NEW collapse mutates an OLDER message (the superseded run), which on a
		// prompt-cached agent flips already-cached content full→collapsed and forces a
		// cache-write of the whole suffix — the dominant +cost we measured (121 such
		// transitions on SWE-50). On a cached agent the superseded run already bills at
		// the cheap cache-read rate, so collapsing it doesn't pay: skip NEW collapses
		// entirely (frozen ones are still reapplied above for stability). With caching
		// OFF, collapse freely — there the content cut is a direct saving.
		if c.CacheAware {
			continue
		}
		newText, key, eff, ok := tryMark(c, fr.mode, content, " [full output: call "+expand.ToolName+"]",
			func(tok string) string { return "[superseded by a later failed→re-run] " + tok })
		if !ok {
			continue // collapse+marker wouldn't shrink this run; leave it verbatim
		}
		commitMark(c, rep, eff, key, content)
		schema.SetMessageText(m, newText)
		freeze(c, fr.Name(), content, newText) // freeze so later turns replay it (no churn)
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
