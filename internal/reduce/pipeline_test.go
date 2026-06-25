package reduce

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/internal/markers"
	"github.com/kagenti/lab-context-engineering/internal/store"
)

// jsonStr returns text as a JSON string literal for embedding in a request body.
func jsonStr(text string) string {
	b, _ := json.Marshal(text)
	return string(b)
}

// bigUniformArray builds a uniform flat JSON array large enough to clear the
// reducer's min-output gate and where CSV is strictly smaller than JSON.
func bigUniformArray(n int) string {
	var recs []string
	for i := 0; i < n; i++ {
		recs = append(recs, fmt.Sprintf(
			`{"id":%d,"name":"item_%d","status":"active","note":"a short note here"}`, 1001+i, i))
	}
	return "[" + strings.Join(recs, ",") + "]"
}

func bigText(prefix string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(prefix)
		b.WriteString(" line of content that is reasonably long to exceed the marker\n")
	}
	return b.String()
}

func reduceFixture(t *testing.T, body string, opts Opts) (canon.Request, Report, store.Rewind) {
	t.Helper()
	req, err := canon.Decode([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	st := store.NewMemory(0)
	rep := ReduceRequest(req, st, store.NewEviction(), opts)
	return req, rep, st
}

// markerOriginal returns the stored original for the first lab-cx marker found in text.
func markerOriginal(t *testing.T, st store.Rewind, text string) (string, bool) {
	ids := markers.FindIDs(text)
	if len(ids) == 0 {
		return "", false
	}
	return st.Get(ids[0])
}

func TestStaleReadCollapsesAndIsReversible(t *testing.T) {
	original := bigText("a.go", 12)
	body := `{"messages":[
		{"role":"user","content":[{"type":"text","text":"fix the bug"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"read","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":` + jsonStr(original) + `}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"u2","name":"edit","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u2","content":"success: file edited"}]},
		{"role":"user","content":[{"type":"text","text":"thanks"}]}
	]}`
	opts := DefaultOpts()
	opts.ProtectRecent = 1
	req, rep, st := reduceFixture(t, body, opts)

	if rep.TokensSaved <= 0 {
		t.Fatalf("expected token savings, got %d (before=%d after=%d)", rep.TokensSaved, rep.TokensBefore, rep.TokensAfter)
	}
	// The stale read block should now hold a marker recoverable to the original.
	block := blockAt(req.Messages(), 2, 0)
	got, ok := markerOriginal(t, st, block["content"].(string))
	if !ok {
		t.Fatalf("stale read was not collapsed to a recoverable marker: %v", block["content"])
	}
	if got != original {
		t.Fatalf("recovered content mismatch")
	}
}

func TestEmptyResultCollapsed(t *testing.T) {
	body := `{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"grep","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"[]"}]},
		{"role":"user","content":[{"type":"text","text":"next"}]}
	]}`
	opts := DefaultOpts()
	opts.ProtectRecent = 1
	req, _, _ := reduceFixture(t, body, opts)
	block := blockAt(req.Messages(), 1, 0)
	// "[]" is tiny so the marker can't beat it (never-inflate); verdict is still
	// "empty" — assert the scorer agrees rather than forcing an inflating collapse.
	items := ExtractItems(req)
	var emptyVerdict bool
	for _, v := range ScoreRelevance(items, DefaultScoreOpts(1)) {
		if v.Reason == "empty" {
			emptyVerdict = true
		}
	}
	if !emptyVerdict {
		t.Fatalf("expected an 'empty' verdict for the [] result; block=%v", block["content"])
	}
}

func TestRecentReadProtected(t *testing.T) {
	original := bigText("hot.go", 12)
	body := `{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"read","input":{"file_path":"hot.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":` + jsonStr(original) + `}]}
	]}`
	opts := DefaultOpts()
	opts.ProtectRecent = 5 // protects everything
	req, _, _ := reduceFixture(t, body, opts)
	block := blockAt(req.Messages(), 1, 0)
	if block["content"].(string) != original {
		t.Fatalf("recent read should be untouched")
	}
}

func TestProvableFormatReencode(t *testing.T) {
	// A uniform JSON array as generic (non-file) output, never referenced later.
	arr := bigUniformArray(30)
	body := `{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"list_items","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":` + jsonStr(arr) + `}]},
		{"role":"user","content":[{"type":"text","text":"ok"}]}
	]}`
	opts := DefaultOpts()
	opts.ProtectRecent = 1
	opts.ProvableOnly = true // predicted-unused becomes a lossless re-encode, not a drop
	req, rep, st := reduceFixture(t, body, opts)
	if rep.TokensSaved <= 0 {
		t.Fatalf("expected savings from re-encoding; before=%d after=%d", rep.TokensBefore, rep.TokensAfter)
	}
	block := blockAt(req.Messages(), 1, 0)
	if _, ok := markerOriginal(t, st, block["content"].(string)); !ok {
		t.Fatalf("re-encoded block should carry a recoverable marker: %v", block["content"])
	}
}
