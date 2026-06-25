package engine

import (
	"encoding/json"
	"fmt"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/internal/cache"
	"github.com/kagenti/lab-context-engineering/internal/markers"
	"github.com/kagenti/lab-context-engineering/internal/reduce"
	"github.com/kagenti/lab-context-engineering/internal/tokens"
)

func errFromPanic(r any) error { return fmt.Errorf("compactor panic: %v", r) }

// Reducer runs the deterministic, lossless-first reduction pass (collapse / skeleton /
// format, cmdfilter, dedup). No model. Large structured tool outputs are left for the
// extract compactor (which runs first in the default pipeline); whatever it does not
// claim, Reducer reduces losslessly here.
type Reducer struct{}

func (Reducer) Name() string            { return reduceName }
func (Reducer) Enabled(c *Context) bool { return c.Settings.ReduceEnabled }

func (Reducer) Compact(req canon.Request, agg *Report, c *Context) (canon.Request, error) {
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
	opts.EnabledReducers = s.Reducers
	opts.EnabledEncoders = s.Encoders
	opts.CacheFloor = c.ClientCacheFloor
	opts.StickyIDs = c.StickyIDs

	rep := reduce.ReduceRequest(req, c.Store, c.Evictions, opts)
	agg.Reduce = rep
	return req, nil
}

// Cacher injects ephemeral cache_control breakpoints on the stable prefix. Only active
// when the client did not already self-cache (ClientCacheFloor < 0): a self-caching
// client (e.g. Claude Code) keeps its own breakpoints.
type Cacher struct{}

func (Cacher) Name() string { return cacheName }
func (Cacher) Enabled(c *Context) bool {
	return c.Settings.CacheEnabled && c.ClientCacheFloor < 0
}

func (Cacher) Compact(req canon.Request, agg *Report, c *Context) (canon.Request, error) {
	s := c.Settings
	anchor := -1
	if msgs, ok := req.Root["messages"].([]any); ok {
		anchor = cache.ProtectedAnchorIndex(msgs, s.ProtectRecent, s.ProtectRecentToolUses)
	}
	cache.Inject(req.Root, s.CacheBreakpoints, s.CacheStableGap, anchor, s.CacheToolsBreakpoint)
	agg.CacheInjected = true
	return req, nil
}

// Truncator is the naive baseline: it keeps the last KeepLast messages and drops the
// older ones, replacing them with one recoverable note. No relevance scoring, no model.
//
// ponytail: naive by design — it does not enforce user/assistant role alternation after
// the drop. It is the no-LLM control to measure smarter compactors against.
type Truncator struct{}

func (Truncator) Name() string            { return truncateName }
func (Truncator) Enabled(c *Context) bool { return c.Settings.TruncateEnabled }

func (Truncator) Compact(req canon.Request, agg *Report, c *Context) (canon.Request, error) {
	s := c.Settings
	keep := s.TruncateKeepLast
	if keep <= 0 {
		keep = 3
	}
	msgs := req.Messages()
	if len(msgs) <= keep {
		return req, nil
	}
	if s.TruncateTriggerTokens > 0 {
		if b, err := json.Marshal(msgs); err == nil && tokens.Count(string(b)) < s.TruncateTriggerTokens {
			return req, nil
		}
	}
	dropped := msgs[:len(msgs)-keep]
	b, _ := json.Marshal(dropped)
	rid := c.Store.Put(string(b))
	note := map[string]any{
		"role": "user",
		"content": "=== Truncated history ===\n" +
			markers.RecoveryNote(fmt.Sprintf("%d earlier message(s)", len(dropped)), "truncated", rid),
	}
	req.SetMessages(append([]map[string]any{note}, msgs[len(msgs)-keep:]...))
	return req, nil
}
