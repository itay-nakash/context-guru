// Package reduce is the deterministic, lossless-first reduction core ported from
// the reference prototype: parse a canonical request into items, score each item's relevance from
// transcript signals, and apply the cheapest faithful, reversible action. No I/O,
// clock, or randomness beyond the injected rewind store.
package reduce

// ContextItem is one reducible block of the request. Ids are content hashes so they
// are stable turn-to-turn (required for cache stability).
type ContextItem struct {
	ID         string
	MsgIndex   int
	BlockIndex int
	Kind       string // "tool_result" | "text" | "tool_use"
	ToolName   string
	ToolUseID  string
	FilePath   string
	Text       string
	TokenEst   int
	ReadOffset *int
	ReadLimit  *int
}

// Verdict is the relevance decision for an item.
type Verdict struct {
	ItemID    string
	Score     float64
	Reason    string // referenced|stale|unused|lossy_candidate|superseded_dup|empty|protected_recent|kept_default
	Protected bool
}

// Reduced is the outcome of routing an item to an action.
type Reduced struct {
	ItemID   string
	Action   string  // keep|collapse|skeleton|format
	NewText  *string // nil means keep verbatim
	RewindID string
}

// Zones is the frozen-prefix / live-zone split.
type Zones struct {
	FrozenCount  int
	AtCompaction bool
}
