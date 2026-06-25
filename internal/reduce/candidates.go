package reduce

import (
	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/internal/markers"
)

// SelectLLMCandidates returns the large structured tool outputs eligible for
// cheap-model extraction, WITHOUT mutating req. The extract compactor owns this
// detection so it is fully independent of the reduce pass (no candidate hand-off).
// It mirrors the candidate predicate the reduce loop previously applied: structured,
// not frozen, not protected, above the token floor, and not already marked.
func SelectLLMCandidates(req canon.Request, opts Opts) []Candidate {
	msgs := req.Messages()
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

	var out []Candidate
	for _, item := range items {
		if item.MsgIndex < frozen || markers.HasForeign(item.Text) || markers.Has(item.Text) {
			continue
		}
		v, ok := verdicts[item.ID]
		if !ok {
			continue
		}
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
		if reasonOK && item.TokenEst >= opts.LLMCompactFloor {
			out = append(out, Candidate{
				ID: item.ID, MsgIndex: item.MsgIndex, BlockIndex: item.BlockIndex,
				Text: item.Text, FilePath: item.FilePath, ToolName: item.ToolName,
				TokenEst: item.TokenEst,
			})
		}
	}
	return out
}
