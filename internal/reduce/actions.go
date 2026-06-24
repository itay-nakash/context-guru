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
	"github.com/kagenti/lab-context-engineering/internal/treesitter"
	toon "github.com/toon-format/toon-go"
	sitter "github.com/tree-sitter/go-tree-sitter"
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

// encoder is one named, ranked format re-encoder. The rank is the tie-break priority
// when two encoders produce the same token count (lower wins).
type encoder struct {
	name string
	rank int
	fn   func(any) (string, bool)
}

// allEncoders is the built-in re-encoder table, keyed by name so config can enable,
// disable, and reorder them purely by NAME. To ADD an encoder: append an entry here
// (give it a unique name and rank), then list that name under reduce.encoders in the
// config file — no other code change is needed.
var allEncoders = []encoder{
	{"json_compact", 0, encCompact},
	{"toon", 1, encTOON},
	{"jsonl", 2, encJSONL},
	{"markdown_kv", 3, encMarkdownKV},
	{"tsv", 4, func(d any) (string, bool) { return encDelimited(d, '\t') }},
	{"csv", 5, func(d any) (string, bool) { return encDelimited(d, ',') }},
}

// selectEncoders returns the encoders allowed by the named set, in the order the names
// were given (a config-controlled priority). An empty/nil set means "all built-ins" in
// their default order, preserving prior behavior. Unknown names are ignored.
func selectEncoders(enabled []string) []encoder {
	if len(enabled) == 0 {
		return allEncoders
	}
	byName := make(map[string]encoder, len(allEncoders))
	for _, e := range allEncoders {
		byName[e.name] = e
	}
	out := make([]encoder, 0, len(enabled))
	for i, name := range enabled {
		if e, ok := byName[name]; ok {
			// Honor the config-given order as the tie-break rank: the first listed
			// encoder wins ties, regardless of its built-in rank.
			e.rank = i
			out = append(out, e)
		}
	}
	return out
}

// bestEncoding returns the smallest faithful re-encoding strictly smaller than text,
// plus its format name, or ("","") if none helps / text isn't structured. enabled is an
// allowed-encoder set referenced by name; empty/nil means all built-in encoders.
//
// ponytail: object keys re-encode in alphabetical order (Go maps don't preserve
// source order). Lossless to the DATA — the original is stored for exact recovery.
func bestEncoding(text string, enabled []string) (string, string) {
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
	for _, e := range selectEncoders(enabled) {
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

// encTOON re-encodes data as Token-Oriented Object Notation. The decoded-JSON
// value may carry json.Number; toon-go renders those natively. Returns false on
// error or empty output so the candidate is simply skipped.
func encTOON(data any) (string, bool) {
	out, err := toon.MarshalString(data, toon.WithLengthMarkers(true))
	if err != nil || out == "" {
		return "", false
	}
	return out, true
}

func encCompact(data any) (string, bool) {
	b, err := json.Marshal(data)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// ---------- skeleton ----------

var bodyDefKinds = map[string]bool{
	"function_declaration": true, "function_definition": true, "function_item": true,
	"method_declaration": true, "method_definition": true, "method": true,
	"constructor_declaration": true,
}

// skeletonize keeps signatures and drops function/method BODIES (the "body" field),
// language-agnostic via tree-sitter. Returns ok=false for non-code, parse failure,
// no bodies, or no token savings.
func skeletonize(source, filePath string) (string, bool) {
	lang := treesitter.LangForExt(filePath)
	if lang == "" {
		return "", false
	}
	src := []byte(source)
	tree, _, ok := treesitter.Parse(lang, src)
	if !ok {
		return "", false
	}
	defer tree.Close()
	type span struct{ start, end uint }
	var bodies []span
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if bodyDefKinds[n.Kind()] {
			if b := n.ChildByFieldName("body"); b != nil {
				bodies = append(bodies, span{b.StartByte(), b.EndByte()})
				return // don't recurse into a body we're dropping (nested fns)
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())
	if len(bodies) == 0 {
		return "", false
	}
	sort.Slice(bodies, func(i, j int) bool { return bodies[i].start > bodies[j].start })
	out := append([]byte(nil), src...)
	for _, b := range bodies {
		out = append(out[:b.start], append([]byte("{ ... }"), out[b.end:]...)...)
	}
	result := string(out)
	if tokens.Count(result) >= tokens.Count(source) {
		return "", false
	}
	return result, true
}

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
	sk, ok := skeletonize(item.Text, item.FilePath)
	if !ok {
		return nil
	}
	return reversible(item, sk, "skeleton", "code body skeletonized", st)
}

func formatReduce(item ContextItem, v Verdict, st store.Rewind, enabledEncoders []string) *Reduced {
	fr, fmtName := bestEncoding(item.Text, enabledEncoders)
	if fr == "" {
		return keepItem(item)
	}
	return reversible(item, fr, "format", "reformatted as "+fmtName, st)
}

// formatReducerName is the built-in name of the lossless format re-encoder. route
// applies it specially (it threads the config-selected encoder set into bestEncoding),
// so the table entry below is a marker whose Reduce is never invoked directly.
const formatReducerName = "format"

var reducers = []Reducer{
	{"collapse", func(i ContextItem, v Verdict) bool { _, ok := collapseReasons[v.Reason]; return ok }, collapseReduce},
	{"skeleton", func(i ContextItem, v Verdict) bool { return isCodePath(i.FilePath) }, skeletonReduce},
	{formatReducerName, func(i ContextItem, v Verdict) bool { return true },
		func(i ContextItem, v Verdict, st store.Rewind) *Reduced { return formatReduce(i, v, st, nil) }},
}

// RegisterReducer inserts a custom strategy just before the format fallback. A custom
// reducer is selectable by config purely by its Reducer.Name — list it under
// reduce.reducers and it is honored; omit it and it is filtered out.
func RegisterReducer(r Reducer) {
	idx := len(reducers) - 1
	if idx < 0 {
		idx = 0
	}
	reducers = append(reducers[:idx], append([]Reducer{r}, reducers[idx:]...)...)
}

// reducerAllowed reports whether a reducer name is enabled by the named set. An
// empty/nil set means "all built-ins", preserving prior behavior.
func reducerAllowed(name string, enabled []string) bool {
	if len(enabled) == 0 {
		return true
	}
	for _, n := range enabled {
		if n == name {
			return true
		}
	}
	return false
}

// route picks the cheapest faithful action for an item. enabledReducers/enabledEncoders
// are config-selected allow-lists referenced by name; empty means "all built-ins".
func route(item ContextItem, v Verdict, st store.Rewind, enabledReducers, enabledEncoders []string) *Reduced {
	if v.Protected {
		// Recent content keeps full fidelity for lossy ops, but a lossless format
		// re-encode is safe even here — when the format reducer is enabled.
		if reducerAllowed(formatReducerName, enabledReducers) {
			return formatReduce(item, v, st, enabledEncoders)
		}
		return keepItem(item)
	}
	for _, r := range reducers {
		if !reducerAllowed(r.Name, enabledReducers) || !r.Applies(item, v) {
			continue
		}
		if r.Name == formatReducerName {
			// Thread the config-selected encoder set into the format re-encoder.
			if res := formatReduce(item, v, st, enabledEncoders); res != nil {
				return res
			}
			continue
		}
		if res := r.Reduce(item, v, st); res != nil {
			return res
		}
	}
	return keepItem(item)
}
