// Package config holds the engine's settings and named presets. Settings are plain
// data; the proxy resolves them from LABCX_*-style env / preset and hands them to the
// engine. Mirrors the knobs the reference prototype exposes, trimmed to what the Go core
// uses.
package config

// Settings configures a reduction pass end to end.
type Settings struct {
	Disabled bool // global kill switch: forward untouched

	// Cache stage (cache_control injection).
	CacheEnabled         bool
	CacheBreakpoints     int
	CacheStableGap       int
	CacheToolsBreakpoint bool

	// Reduce stage.
	ReduceEnabled         bool
	ProtectRecent         int
	ProtectRecentToolUses int
	ProvableOnly          bool
	CollapseOutputs       bool
	ContextLimit          int
	ReduceCachedPrefix    bool
	CmdFilter             bool
	RehydrateOnCompaction bool

	// Extract compactor (cheap-model projection of large structured outputs).
	ExtractEnabled           bool
	ExtractMode              string // "" | auto | single | rlm | deterministic
	LLMCompactFloor          int
	LLMCompactStructuredOnly bool

	// Summarize compactor (LLM trajectory summarization; off by default).
	SummarizeEnabled       bool
	SummarizeLevel         string // concise | regular | highly_detailed
	SummarizeKeepLast      int    // recent messages kept verbatim after the summary
	SummarizeTriggerTokens int    // skip below this whole-conversation token count

	// Truncate compactor (naive: keep last N messages, drop older; off by default).
	TruncateEnabled       bool
	TruncateKeepLast      int
	TruncateTriggerTokens int

	// Named component selection. Each list is referenced BY NAME so that a component
	// added tomorrow (a new reducer/encoder/extract strategy/compactor) is controlled
	// purely from config — register it by name in its package, then list it here. An
	// empty list means "use all built-in defaults" (no filtering).
	//
	// Reducers filters internal/reduce reducers by Reducer.Name (e.g. collapse, skeleton,
	// format). Encoders filters AND orders the format re-encoders by name (e.g.
	// json_compact, toon, jsonl, markdown_kv, tsv, csv). ExtractStrategies restricts the
	// internal/extract strategy order (e.g. code, single, rlm, deterministic). Compactors
	// selects which compactors run and in what order (reduce, extract, summarize,
	// truncate, cache).
	Reducers          []string
	Encoders          []string
	ExtractStrategies []string
	Compactors        []string
}

// Default is the "balanced" preset: lossless caching + accuracy-safe reduction on,
// LLM extraction off (it needs an injected model client; the proxy enables it).
func Default() Settings {
	return Settings{
		CacheEnabled: true, CacheBreakpoints: 4, CacheStableGap: 2, CacheToolsBreakpoint: true,
		ReduceEnabled: true, ProtectRecent: 2, ProvableOnly: true, CollapseOutputs: true,
		CmdFilter: true, RehydrateOnCompaction: true,
		LLMCompactFloor: 3000, LLMCompactStructuredOnly: true,
	}
}

// Preset returns a named settings preset, or Default() for an unknown name.
func Preset(name string) Settings {
	s := Default()
	switch name {
	case "safe":
		// Lossless only: no predicted drops, no LLM, caching + lossless re-encode.
		s.ProvableOnly = true
		s.CollapseOutputs = false
	case "balanced", "":
		// defaults
	case "aggressive":
		s.ProvableOnly = false
		s.ProtectRecent = 1
	case "cache":
		s.ReduceEnabled = false
	case "coding", "mcp":
		s.ProtectRecentToolUses = 8
	}
	return s
}
