package reduce

import (
	"encoding/json"
	"math"
	"sort"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/internal/markers"
	"github.com/kagenti/lab-context-engineering/internal/store"
	"github.com/kagenti/lab-context-engineering/internal/tokens"
)

// Candidate is a large output left verbatim for the async Extract stage.
type Candidate struct {
	ID         string
	MsgIndex   int
	BlockIndex int
	Text       string
	FilePath   string
	ToolName   string
	TokenEst   int
}

// Report summarizes a reduction pass.
//
// ponytail: per-item "where did tokens go" breakdown omitted — counts + reduced ids
// are enough for metrics and cross-turn stickiness. Add breakdown if a dashboard needs it.
type Report struct {
	SessionID             string
	AtCompaction          bool
	FrozenCount           int
	TokensBefore          int
	TokensAfter           int
	TokensSaved           int
	Ratio                 float64
	ReducerErrors         int
	ToolDefTokens         int
	ToolsTotal            int
	ReducedIDs            []string
	LLMCandidates         []Candidate
	CompactionPassthrough bool
	Rehydrated            int
}

// Opts configures a reduction pass. Use DefaultOpts and adjust.
type Opts struct {
	ProtectRecent            int
	ContextLimit             int
	StickyIDs                map[string]struct{}
	CollapseOutputs          bool
	CacheFloor               int
	LLMCompact               bool
	LLMCompactFloor          int
	LLMCompactStructuredOnly bool
	RehydrateOnCompaction    bool
	ProtectRecentToolUses    int
	ProvableOnly             bool
	ReduceCachedPrefix       bool
	CmdFilter                bool

	// EnabledReducers / EnabledEncoders are config-selected allow-lists referenced by
	// NAME (Reducer.Name / encoder name). Empty means "all built-ins" — prior behavior.
	// They let config enable, disable, and (for encoders) reorder components without a
	// core edit; see config.Settings.Reducers / .Encoders.
	EnabledReducers []string
	EnabledEncoders []string
}

// DefaultOpts mirrors winnow's reduce_request defaults.
func DefaultOpts() Opts {
	return Opts{
		CollapseOutputs: true, CacheFloor: -1, LLMCompactFloor: 3000,
		LLMCompactStructuredOnly: true, CmdFilter: true,
	}
}

func measure(before, after string) (int, int, int, float64) {
	b, a := tokens.Count(before), tokens.Count(after)
	saved := b - a
	ratio := 0.0
	if b > 0 {
		ratio = math.Round(float64(saved)/float64(b)*10000) / 10000
	}
	return b, a, saved, ratio
}

func firstUserText(msgs []map[string]any) string {
	for _, m := range msgs {
		if m["role"] != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			return c
		case []any:
			for _, b := range c {
				if bb, ok := b.(map[string]any); ok && bb["type"] == "text" {
					if t, ok := bb["text"].(string); ok {
						return t
					}
				}
			}
		}
	}
	return ""
}

func serialize(msgs []map[string]any) string {
	b, _ := json.Marshal(msgs)
	return string(b)
}

func setBlockText(block map[string]any, newText string) {
	switch block["type"] {
	case "tool_result":
		block["content"] = newText
	case "text":
		block["text"] = newText
	}
}

func blockAt(msgs []map[string]any, mi, bi int) map[string]any {
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

// ReduceRequest reduces req in place and returns a Report. Fail-open: a per-item
// reducer error is counted and leaves that item verbatim. Ported from winnow's
// reduce_request.
func ReduceRequest(req canon.Request, st store.Rewind, ev *store.Eviction, opts Opts) Report {
	msgs := req.Messages()
	beforeText := serialize(msgs)
	systemStr := textOf(req.Root["system"])
	sid := store.SessionID(systemStr, firstUserText(msgs))

	toolsList, _ := req.Root["tools"].([]any)
	toolDefTokens := 0
	if len(toolsList) > 0 {
		if b, err := json.Marshal(toolsList); err == nil {
			toolDefTokens = tokens.Count(string(b))
		}
	}

	if opts.RehydrateOnCompaction && IsCompactionRequest(req) {
		restored := RehydrateMarkers(req, st)
		b, a, saved, ratio := measure(beforeText, serialize(req.Messages()))
		return Report{
			SessionID: sid, AtCompaction: true, TokensBefore: b, TokensAfter: a,
			TokensSaved: saved, Ratio: ratio, ToolDefTokens: toolDefTokens,
			ToolsTotal: len(toolsList), CompactionPassthrough: true, Rehydrated: restored,
		}
	}

	items := ExtractItems(req)
	inputTokens := 0
	for _, it := range items {
		inputTokens += it.TokenEst
	}
	zones := ComputeZones(len(msgs), inputTokens, opts.ContextLimit, 0)
	frozen := zones.FrozenCount
	if !opts.ReduceCachedPrefix && opts.CacheFloor+1 > frozen {
		frozen = opts.CacheFloor + 1
	}

	scoreOpts := DefaultScoreOpts(opts.ProtectRecent)
	scoreOpts.CollapseOutputs = opts.CollapseOutputs
	scoreOpts.ProtectRecentToolUses = opts.ProtectRecentToolUses
	scoreOpts.ProvableOnly = opts.ProvableOnly
	verdicts := map[string]Verdict{}
	for _, v := range ScoreRelevance(items, scoreOpts) {
		verdicts[v.ItemID] = v
	}

	byID := map[string]ContextItem{}
	for _, it := range items {
		byID[it.ID] = it
	}

	handled := map[string]struct{}{}
	reducedIDs := map[string]struct{}{}
	reducerErrors := 0

	// Batch collapse pre-pass.
	batchPass := func(selectFn func(ContextItem) bool, detect func([]string) []int, reason string, minTokens int) {
		var cands []ContextItem
		for _, it := range items {
			if it.Kind != "tool_result" {
				continue
			}
			if _, done := handled[it.ID]; done {
				continue
			}
			if it.MsgIndex < frozen || markers.HasForeign(it.Text) {
				continue
			}
			if v, ok := verdicts[it.ID]; ok && v.Protected {
				continue
			}
			if it.TokenEst < minTokens || !selectFn(it) {
				continue
			}
			cands = append(cands, it)
		}
		texts := make([]string, len(cands))
		for i, c := range cands {
			texts[i] = c.Text
		}
		for _, idx := range detect(texts) {
			it := cands[idx]
			block := blockAt(msgs, it.MsgIndex, it.BlockIndex)
			if block == nil {
				continue
			}
			newText, _ := collapse(it.Text, it.FilePath, reason, st, false)
			if tokens.Count(newText) < tokens.Count(it.Text) {
				setBlockText(block, newText)
				handled[it.ID] = struct{}{}
				reducedIDs[it.ID] = struct{}{}
			}
		}
	}

	batchPass(func(it ContextItem) bool { return isFailure(it.Text) || isSuccess(it.Text) },
		supersededFailedRuns, "failed_run", 0)
	batchPass(func(ContextItem) bool { return true },
		func(t []string) []int { return nearDuplicateEarlier(t, 0.85) }, "duplicate", 80)

	// Command-filter pre-pass.
	if opts.CmdFilter {
		cmdByID := map[string]string{}
		for _, m := range msgs {
			if list, ok := m["content"].([]any); ok {
				for _, bRaw := range list {
					b, ok := bRaw.(map[string]any)
					if !ok || b["type"] != "tool_use" {
						continue
					}
					input, _ := b["input"].(map[string]any)
					id, _ := b["id"].(string)
					if input != nil && id != "" {
						if cmd, ok := input["command"].(string); ok {
							cmdByID[id] = cmd
						}
					}
				}
			}
		}
		for _, it := range items {
			if it.Kind != "tool_result" {
				continue
			}
			if _, done := handled[it.ID]; done {
				continue
			}
			if it.MsgIndex < frozen || markers.HasForeign(it.Text) {
				continue
			}
			if v, ok := verdicts[it.ID]; ok && v.Protected {
				continue
			}
			cmd := cmdByID[it.ToolUseID]
			if cmd == "" {
				continue
			}
			filtered, ok := compactCommandOutput(cmd, it.Text)
			if !ok || tokens.Count(filtered) >= tokens.Count(it.Text) {
				continue
			}
			block := blockAt(msgs, it.MsgIndex, it.BlockIndex)
			if block == nil {
				continue
			}
			rid := st.Put(it.Text)
			label := cmd
			if len(label) > 48 {
				label = label[:48]
			}
			newText := filtered + "\n" + markers.RecoveryNote(label, "command output filtered", rid)
			if tokens.Count(newText) < tokens.Count(it.Text) {
				setBlockText(block, newText)
				handled[it.ID] = struct{}{}
				reducedIDs[it.ID] = struct{}{}
			}
		}
	}

	var llmCandidates []Candidate

	for _, item := range items {
		if _, done := handled[item.ID]; done {
			continue
		}
		if item.MsgIndex < frozen || markers.HasForeign(item.Text) {
			continue
		}
		block := blockAt(msgs, item.MsgIndex, item.BlockIndex)
		if block == nil {
			continue
		}

		evictKey := item.ToolUseID
		if evictKey == "" {
			evictKey = item.FilePath
		}
		if evictKey != "" && ev != nil && ev.IsEvicted(sid, evictKey) {
			newText, _ := collapse(item.Text, item.FilePath, "pruned", st, true)
			setBlockText(block, newText)
			continue
		}

		v, ok := verdicts[item.ID]
		if !ok {
			continue
		}

		// LLM-extraction candidate selection.
		structured := IsStructured(item.Text)
		reasonOK := false
		switch {
		case opts.LLMCompactStructuredOnly && !structured:
			reasonOK = false
		case structured:
			reasonOK = v.Reason == "unused" || v.Reason == "lossy_candidate" || v.Reason == "kept_default"
		default:
			reasonOK = v.Reason == "unused" || v.Reason == "lossy_candidate"
		}
		if opts.LLMCompact && reasonOK && item.TokenEst >= opts.LLMCompactFloor && len(markers.FindIDs(item.Text)) == 0 {
			llmCandidates = append(llmCandidates, Candidate{
				ID: item.ID, MsgIndex: item.MsgIndex, BlockIndex: item.BlockIndex,
				Text: item.Text, FilePath: item.FilePath, ToolName: item.ToolName,
				TokenEst: item.TokenEst,
			})
			continue
		}

		reduced := route(byID[item.ID], v, st, opts.EnabledReducers, opts.EnabledEncoders)
		if reduced.NewText != nil {
			setBlockText(block, *reduced.NewText)
			reducedIDs[item.ID] = struct{}{}
		} else if opts.StickyIDs != nil {
			if _, sticky := opts.StickyIDs[item.ID]; sticky {
				newText, _ := collapse(item.Text, item.FilePath, "sticky", st, false)
				if tokens.Count(newText) < tokens.Count(item.Text) {
					setBlockText(block, newText)
					reducedIDs[item.ID] = struct{}{}
				}
			}
		}
	}

	b, a, saved, ratio := measure(beforeText, serialize(req.Messages()))
	ids := make([]string, 0, len(reducedIDs))
	for id := range reducedIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return Report{
		SessionID: sid, AtCompaction: zones.AtCompaction, FrozenCount: frozen,
		TokensBefore: b, TokensAfter: a, TokensSaved: saved, Ratio: ratio,
		ReducerErrors: reducerErrors, ToolDefTokens: toolDefTokens, ToolsTotal: len(toolsList),
		ReducedIDs: ids, LLMCandidates: llmCandidates,
	}
}
