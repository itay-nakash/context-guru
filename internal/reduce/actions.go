package reduce

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/kagenti/lab-context-engineering/internal/markers"
	"github.com/kagenti/lab-context-engineering/internal/store"
	"github.com/kagenti/lab-context-engineering/internal/tokens"
)

// ---------- collapse ----------

// collapse replaces text with a reversible marker, storing the original.
func collapse(text, filePath, reason string, st store.Rewind, terse bool) (string, string) {
	rid := st.Put(text)
	marker := markers.Make(rid)
	label := filePath
	if label == "" {
		label = "tool output"
	}
	if terse {
		return fmt.Sprintf("[winnow %s: %s] %s", reason, label, marker), rid
	}
	return fmt.Sprintf("[winnow: %s omitted (%s); call winnow_expand(%q) to restore] %s",
		label, reason, rid, marker), rid
}

// ---------- failed_run ----------

var (
	failRe = regexp.MustCompile(`(?i)\b(fail(ed|ures?)?|error|exception|traceback|assert|panic|FAILED|✗|✖)\b`)
	passRe = regexp.MustCompile(`(?i)\b(pass(ed)?|ok|success(ful)?|0 failed|all tests passed|✓|✔)\b`)
)

func isFailure(text string) bool { return failRe.MatchString(text) && !passRe.MatchString(text) }
func isSuccess(text string) bool { return passRe.MatchString(text) && !failRe.MatchString(text) }

// supersededFailedRuns returns indices of failed runs followed by a later success.
func supersededFailedRuns(texts []string) []int {
	lastSuccess := -1
	for i := len(texts) - 1; i >= 0; i-- {
		if isSuccess(texts[i]) {
			lastSuccess = i
			break
		}
	}
	if lastSuccess < 0 {
		return nil
	}
	var out []int
	for i := 0; i < lastSuccess; i++ {
		if isFailure(texts[i]) {
			out = append(out, i)
		}
	}
	return out
}

// ---------- dedup ----------

const shingleK = 5

func shingles(text string) map[string]struct{} {
	toks := strings.Fields(text)
	out := map[string]struct{}{}
	if len(toks) < shingleK {
		out[text] = struct{}{}
		return out
	}
	for i := 0; i+shingleK <= len(toks); i++ {
		out[strings.Join(toks[i:i+shingleK], " ")] = struct{}{}
	}
	return out
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	inter := 0
	for s := range a {
		if _, ok := b[s]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// nearDuplicateEarlier returns indices of earlier outputs that a later one
// near-duplicates (Jaccard >= threshold over 5-gram shingles).
//
// ponytail: exact O(n²) Jaccard instead of MinHash — candidate sets are small
// (sizeable tool outputs only); add MinHash if a transcript ever makes this hot.
func nearDuplicateEarlier(texts []string, threshold float64) []int {
	sh := make([]map[string]struct{}, len(texts))
	for i, t := range texts {
		sh[i] = shingles(t)
	}
	drop := map[int]struct{}{}
	for i := 0; i < len(texts); i++ {
		for j := i + 1; j < len(texts); j++ {
			if jaccard(sh[i], sh[j]) >= threshold {
				drop[i] = struct{}{}
				break
			}
		}
	}
	out := make([]int, 0, len(drop))
	for i := range drop {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

// ---------- format re-encoding ----------

// bestEncoding returns the smallest faithful re-encoding strictly smaller than text,
// plus its format name, or ("","") if none helps / text isn't structured.
//
// ponytail: object keys re-encode in alphabetical order (Go maps don't preserve
// source order). Lossless to the DATA — the original is stored for exact recovery.
func bestEncoding(text string) (string, string) {
	var data any
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		recs := parseNDJSON(text)
		if recs == nil {
			return "", ""
		}
		data = recs
	}
	orig := tokens.Count(text)
	type cand struct {
		enc, name string
		rank, tok int
	}
	var best *cand
	encoders := []struct {
		name string
		rank int
		fn   func(any) (string, bool)
	}{
		{"json_compact", 0, encCompact},
		{"jsonl", 1, encJSONL},
		{"markdown_kv", 2, encMarkdownKV},
		{"tsv", 3, func(d any) (string, bool) { return encDelimited(d, '\t') }},
		{"csv", 4, func(d any) (string, bool) { return encDelimited(d, ',') }},
	}
	for _, e := range encoders {
		enc, ok := e.fn(data)
		if !ok {
			continue
		}
		t := tokens.Count(enc)
		if t < orig && (best == nil || t < best.tok || (t == best.tok && e.rank < best.rank)) {
			best = &cand{enc, e.name, e.rank, t}
		}
	}
	if best == nil {
		return "", ""
	}
	return best.enc, best.name
}

func parseNDJSON(text string) []any {
	var lines []string
	for _, ln := range strings.Split(text, "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	if len(lines) < 2 {
		return nil
	}
	var recs []any
	for _, ln := range lines {
		var v any
		dec := json.NewDecoder(strings.NewReader(ln))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return nil
		}
		switch v.(type) {
		case map[string]any, []any:
			recs = append(recs, v)
		default:
			return nil
		}
	}
	return recs
}

func isScalar(v any) bool {
	switch v.(type) {
	case nil, string, bool, float64, int, json.Number:
		return true
	}
	return false
}

// uniformFlat returns sorted column names if data is a non-empty list of objects
// sharing the same key set with only scalar values; else nil.
func uniformFlat(data any) []string {
	list, ok := data.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	first, ok := list[0].(map[string]any)
	if !ok {
		return nil
	}
	cols := make([]string, 0, len(first))
	for k := range first {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	colset := map[string]struct{}{}
	for _, c := range cols {
		colset[c] = struct{}{}
	}
	for _, r := range list {
		row, ok := r.(map[string]any)
		if !ok || len(row) != len(colset) {
			return nil
		}
		for k, v := range row {
			if _, ok := colset[k]; !ok || !isScalar(v) {
				return nil
			}
		}
	}
	return cols
}

func scalarStr(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func encDelimited(data any, delim rune) (string, bool) {
	cols := uniformFlat(data)
	if cols == nil {
		return "", false
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Comma = delim
	_ = w.Write(cols)
	for _, r := range data.([]any) {
		row := r.(map[string]any)
		rec := make([]string, len(cols))
		for i, c := range cols {
			rec[i] = scalarStr(row[c])
		}
		_ = w.Write(rec)
	}
	w.Flush()
	return strings.TrimRight(buf.String(), "\n"), true
}

func encJSONL(data any) (string, bool) {
	list, ok := data.([]any)
	if !ok || len(list) == 0 {
		return "", false
	}
	for _, x := range list {
		if _, ok := x.(map[string]any); !ok {
			return "", false
		}
	}
	var lines []string
	for _, x := range list {
		b, err := json.Marshal(x)
		if err != nil {
			return "", false
		}
		lines = append(lines, string(b))
	}
	return strings.Join(lines, "\n"), true
}

func encMarkdownKV(data any) (string, bool) {
	m, ok := data.(map[string]any)
	if !ok || len(m) == 0 {
		return "", false
	}
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if !isScalar(v) {
			return "", false
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s: %s", k, scalarStr(m[k])))
	}
	return strings.Join(lines, "\n"), true
}

func encCompact(data any) (string, bool) {
	b, err := json.Marshal(data)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// ---------- skeleton ----------

// skeletonize would drop code-function bodies while keeping signatures. A faithful
// implementation needs a real parser; a regex version mangles code, so v1 declines.
//
// ponytail: stubbed — the router falls through to format re-encoding. Implement with
// tree-sitter (go-tree-sitter) when code-skeletonization is needed.
func skeletonize(source, lang string) (string, bool) { return "", false }

// ---------- is_structured ----------

// IsStructured reports whether text is machine-structured (a JSON object, a JSON
// array of length >= 2, or NDJSON) — the safe target for filter-style extraction.
func IsStructured(text string) bool {
	var v any
	if err := json.Unmarshal([]byte(text), &v); err == nil {
		switch t := v.(type) {
		case map[string]any:
			return len(t) > 0
		case []any:
			return len(t) >= 2
		}
	}
	return parseNDJSON(text) != nil
}

// ---------- router ----------

var collapseReasons = set("stale", "superseded_dup", "empty", "duplicate", "failed_run", "unused")

// Reducer is a pluggable reduction strategy.
type Reducer struct {
	Name    string
	Applies func(ContextItem, Verdict) bool
	Reduce  func(ContextItem, Verdict, store.Rewind) *Reduced
}

func keepItem(item ContextItem) *Reduced { return &Reduced{ItemID: item.ID, Action: "keep"} }

// reversible makes a lossy reduction reversible and self-advertising; falls back to
// keep if the marker overhead eats the savings.
func reversible(item ContextItem, reduced, action, what string, st store.Rewind) *Reduced {
	rid := st.Put(item.Text)
	label := item.FilePath
	if label == "" {
		label = "tool output"
	}
	newText := strings.TrimRight(reduced, "\n") + "\n" + markers.RecoveryNote(label, what, rid)
	if tokens.Count(newText) < tokens.Count(item.Text) {
		return &Reduced{ItemID: item.ID, Action: action, NewText: &newText, RewindID: rid}
	}
	return keepItem(item)
}

func collapseReduce(item ContextItem, v Verdict, st store.Rewind) *Reduced {
	newText, rid := collapse(item.Text, item.FilePath, v.Reason, st, false)
	if tokens.Count(newText) < tokens.Count(item.Text) {
		return &Reduced{ItemID: item.ID, Action: "collapse", NewText: &newText, RewindID: rid}
	}
	return keepItem(item)
}

func skeletonReduce(item ContextItem, v Verdict, st store.Rewind) *Reduced {
	if !isCodePath(item.FilePath) {
		return nil
	}
	sk, ok := skeletonize(item.Text, "")
	if !ok {
		return nil
	}
	return reversible(item, sk, "skeleton", "code body skeletonized", st)
}

func formatReduce(item ContextItem, v Verdict, st store.Rewind) *Reduced {
	fr, fmtName := bestEncoding(item.Text)
	if fr == "" {
		return keepItem(item)
	}
	return reversible(item, fr, "format", "reformatted as "+fmtName, st)
}

var reducers = []Reducer{
	{"collapse", func(i ContextItem, v Verdict) bool { _, ok := collapseReasons[v.Reason]; return ok }, collapseReduce},
	{"skeleton", func(i ContextItem, v Verdict) bool { return isCodePath(i.FilePath) }, skeletonReduce},
	{"format", func(i ContextItem, v Verdict) bool { return true }, formatReduce},
}

// RegisterReducer inserts a custom strategy just before the format fallback.
func RegisterReducer(r Reducer) {
	idx := len(reducers) - 1
	if idx < 0 {
		idx = 0
	}
	reducers = append(reducers[:idx], append([]Reducer{r}, reducers[idx:]...)...)
}

// route picks the cheapest faithful action for an item.
func route(item ContextItem, v Verdict, st store.Rewind) *Reduced {
	if v.Protected {
		// Recent content keeps full fidelity for lossy ops, but a lossless format
		// re-encode is safe even here.
		return formatReduce(item, v, st)
	}
	for _, r := range reducers {
		if r.Applies(item, v) {
			if res := r.Reduce(item, v, st); res != nil {
				return res
			}
		}
	}
	return keepItem(item)
}
