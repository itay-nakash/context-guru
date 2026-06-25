package reduce

import (
	"regexp"
	"strings"
	"unicode"
)

// Relevance scoring — the core contribution. For each reducible read/tool_result
// item, decide whether it is still relevant using deterministic transcript signals.
// Ported from the reference prototype's relevance.py.

var (
	salientRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_./-]{7,}`)
	idTokenRe = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_.\-/]*`)
)

// ScoreOpts configures relevance scoring. Zero value is not the default; callers
// pass DefaultScoreOpts() and adjust.
type ScoreOpts struct {
	ProtectRecent         int
	CollapseOutputs       bool
	MinOutputTokens       int
	ProtectRecentToolUses int
	ProvableOnly          bool
	LiteralSignal         bool
}

// DefaultScoreOpts mirrors the reference prototype's score_relevance defaults.
func DefaultScoreOpts(protectRecent int) ScoreOpts {
	return ScoreOpts{
		ProtectRecent: protectRecent, CollapseOutputs: true,
		MinOutputTokens: 200, LiteralSignal: true,
	}
}

func salientTokens(text string) map[string]struct{} {
	return tokenSet(salientRe, text)
}

func shortIDTokens(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range idTokenRe.FindAllString(text, -1) {
		if len(t) < 2 || len(t) > 7 {
			continue
		}
		hasDigit := strings.ContainsFunc(t, unicode.IsDigit)
		if hasDigit || t == strings.ToUpper(t) {
			out[t] = struct{}{}
		}
	}
	return out
}

func outputReferencedLater(item ContextItem, items []ContextItem) bool {
	salient := salientTokens(item.Text)
	ids := shortIDTokens(item.Text)
	if len(salient) == 0 && len(ids) == 0 {
		return true // nothing distinctive -> don't risk collapsing
	}
	for _, t := range items {
		if t.MsgIndex <= item.MsgIndex {
			continue
		}
		for s := range salient {
			if strings.Contains(t.Text, s) {
				return true
			}
		}
		if len(ids) > 0 {
			for tok := range shortIDTokens(t.Text) {
				if _, ok := ids[tok]; ok {
					return true
				}
			}
		}
	}
	return false
}

func isEmptyResult(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	switch t {
	case "", "none", "null", "[]", "{}", `""`, "{ }", "[ ]":
		return true
	}
	return false
}

// ScoreRelevance returns one verdict per reducible item.
func ScoreRelevance(items []ContextItem, opts ScoreOpts) []Verdict {
	if len(items) == 0 {
		return nil
	}
	maxMI := 0
	for _, i := range items {
		if i.MsgIndex > maxMI {
			maxMI = i.MsgIndex
		}
	}
	protectFrom := maxMI - opts.ProtectRecent + 1
	if opts.ProtectRecentToolUses > 0 {
		var tuMIs []int
		seen := map[int]struct{}{}
		for _, i := range items {
			if i.Kind == "tool_use" {
				if _, ok := seen[i.MsgIndex]; !ok {
					seen[i.MsgIndex] = struct{}{}
					tuMIs = append(tuMIs, i.MsgIndex)
				}
			}
		}
		sortInts(tuMIs)
		if len(tuMIs) >= opts.ProtectRecentToolUses {
			if cand := tuMIs[len(tuMIs)-opts.ProtectRecentToolUses]; cand < protectFrom {
				protectFrom = cand
			}
		}
	}

	// Hot path: the file the agent is touching right now is never predictively dropped.
	hotPath := ""
	lastMI := -1
	for _, i := range items {
		if i.Kind == "tool_use" && i.FilePath != "" && i.MsgIndex >= lastMI {
			lastMI = i.MsgIndex
			hotPath = i.FilePath
		}
	}
	unusedReason := "unused"
	if opts.ProvableOnly {
		unusedReason = "lossy_candidate"
	}

	resultText := map[string]string{}
	for _, it := range items {
		if it.Kind == "tool_result" && it.ToolUseID != "" {
			resultText[it.ToolUseID] = it.Text
		}
	}

	fullReadMIs := map[string][]int{}
	mutateMIs := map[string][]int{}
	for _, it := range items {
		if it.FilePath != "" && isMutateTool(it.ToolName) {
			if !isFailure(resultText[it.ToolUseID]) {
				mutateMIs[it.FilePath] = append(mutateMIs[it.FilePath], it.MsgIndex)
			}
		}
		isReadLike := it.FilePath != "" && (isReadTool(it.ToolName) || it.Kind == "tool_result")
		if isReadLike && it.ReadOffset == nil && it.ReadLimit == nil {
			fullReadMIs[it.FilePath] = append(fullReadMIs[it.FilePath], it.MsgIndex)
		}
	}

	referencedLater := func(item ContextItem) bool {
		fp := item.FilePath
		if fp == "" {
			return false
		}
		for _, mi := range mutateMIs[fp] {
			if mi > item.MsgIndex {
				return true
			}
		}
		syms := definedSymbols(item.Text, fp)
		var lits map[string]struct{}
		if opts.LiteralSignal {
			lits = salientLiterals(item.Text)
		}
		for _, t := range items {
			if t.MsgIndex <= item.MsgIndex {
				continue
			}
			if pathReferenced(fp, t.Text) || symbolsUsed(syms, t.Text) {
				return true
			}
			if len(lits) > 0 && literalsUsed(lits, t.Text) {
				return true
			}
		}
		return false
	}

	var verdicts []Verdict
	for _, it := range items {
		protected := it.MsgIndex >= protectFrom
		onHotPath := opts.ProvableOnly && hotPath != "" && it.FilePath == hotPath
		unusedHere := unusedReason
		if onHotPath {
			unusedHere = "kept_default"
		}
		isFileRead := it.FilePath != "" && (isReadTool(it.ToolName) || it.Kind == "tool_result")

		if protected {
			verdicts = append(verdicts, Verdict{it.ID, 1.0, "protected_recent", true})
			continue
		}
		if opts.CollapseOutputs && it.Kind == "tool_result" && isEmptyResult(it.Text) {
			verdicts = append(verdicts, Verdict{it.ID, 0.1, "empty", false})
			continue
		}
		if !isFileRead {
			if opts.CollapseOutputs && it.Kind == "tool_result" &&
				it.TokenEst >= opts.MinOutputTokens && !outputReferencedLater(it, items) {
				verdicts = append(verdicts, Verdict{it.ID, 0.2, unusedHere, false})
			} else {
				verdicts = append(verdicts, Verdict{it.ID, 0.7, "kept_default", false})
			}
			continue
		}

		fp := it.FilePath
		laterMutate := false
		for _, mi := range mutateMIs[fp] {
			if mi > it.MsgIndex {
				laterMutate = true
				break
			}
		}
		laterFullRead := false
		for _, mi := range fullReadMIs[fp] {
			if mi > it.MsgIndex {
				laterFullRead = true
				break
			}
		}
		switch {
		case laterMutate:
			verdicts = append(verdicts, Verdict{it.ID, 0.1, "stale", false})
		case laterFullRead:
			verdicts = append(verdicts, Verdict{it.ID, 0.15, "superseded_dup", false})
		case referencedLater(it):
			verdicts = append(verdicts, Verdict{it.ID, 0.9, "referenced", false})
		default:
			verdicts = append(verdicts, Verdict{it.ID, 0.2, unusedHere, false})
		}
	}
	return verdicts
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
