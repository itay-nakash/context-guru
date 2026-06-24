package engine

import (
	"fmt"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/internal/cache"
	"github.com/kagenti/lab-context-engineering/internal/reduce"
)

func errFromPanic(r any) error { return fmt.Errorf("stage panic: %v", r) }

// ReduceStage runs the deterministic, lossless-first reduction pass.
type ReduceStage struct{}

func (ReduceStage) Name() string            { return "reduce" }
func (ReduceStage) Enabled(c *Context) bool { return c.Settings.ReduceEnabled }

func (ReduceStage) Run(req canon.Request, agg *Report, c *Context) error {
	s := c.Settings
	opts := reduce.DefaultOpts()
	opts.ProtectRecent = s.ProtectRecent
	opts.ProtectRecentToolUses = s.ProtectRecentToolUses
	opts.ProvableOnly = s.ProvableOnly
	opts.CollapseOutputs = s.CollapseOutputs
	opts.ContextLimit = s.ContextLimit
	opts.ReduceCachedPrefix = s.ReduceCachedPrefix
	opts.CmdFilter = s.CmdFilter
	opts.RehydrateOnCompaction = s.RehydrateOnCompaction
	opts.CacheFloor = c.ClientCacheFloor
	opts.StickyIDs = c.StickyIDs
	// Only mark candidates when an extractor is wired; otherwise leave large outputs
	// for the deterministic actions to handle.
	opts.LLMCompact = c.Extract != nil && s.ExtractEnabled
	opts.LLMCompactFloor = s.LLMCompactFloor
	opts.LLMCompactStructuredOnly = s.LLMCompactStructuredOnly

	rep := reduce.ReduceRequest(req, c.Store, c.Evictions, opts)
	agg.Reduce = rep
	agg.Candidates = rep.LLMCandidates
	return nil
}

// ExtractStage hands the large candidate outputs to the injected cheap-model
// extractor. No-op when no extractor is wired or there are no candidates.
type ExtractStage struct{}

func (ExtractStage) Name() string { return "extract" }
func (ExtractStage) Enabled(c *Context) bool {
	return c.Settings.ExtractEnabled && c.Extract != nil
}

func (ExtractStage) Run(req canon.Request, agg *Report, c *Context) error {
	if len(agg.Candidates) == 0 {
		return nil
	}
	return c.Extract(c.GoCtx, req, agg.Candidates)
}

// CacheStage injects ephemeral cache_control breakpoints on the stable prefix. Only
// active when the client did not already self-cache (ClientCacheFloor < 0), matching
// winnow: a self-caching client (e.g. Claude Code) keeps its own breakpoints.
type CacheStage struct{}

func (CacheStage) Name() string { return "cache" }
func (CacheStage) Enabled(c *Context) bool {
	return c.Settings.CacheEnabled && c.ClientCacheFloor < 0
}

func (CacheStage) Run(req canon.Request, agg *Report, c *Context) error {
	s := c.Settings
	anchor := -1
	if msgs, ok := req.Root["messages"].([]any); ok {
		anchor = cache.ProtectedAnchorIndex(msgs, s.ProtectRecent, s.ProtectRecentToolUses)
	}
	cache.Inject(req.Root, s.CacheBreakpoints, s.CacheStableGap, anchor, s.CacheToolsBreakpoint)
	agg.CacheInjected = true
	return nil
}
