package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/config"
)

// firstTwoModel returns the first two records of the JSON array embedded in the
// prompt — a content-aware, always-contained selection.
type firstTwoModel struct{}

func (firstTwoModel) Complete(_ context.Context, prompt string) (string, error) {
	const marker = "INPUT (return a smaller value of this same shape):\n"
	i := strings.Index(prompt, marker)
	if i < 0 {
		return "", nil
	}
	rest := prompt[i+len(marker):]
	if e := strings.Index(rest, "\n\n"); e >= 0 {
		rest = rest[:e]
	}
	var arr []any
	if json.Unmarshal([]byte(rest), &arr) == nil && len(arr) >= 2 {
		b, _ := json.Marshal(arr[:2])
		return string(b), nil
	}
	return "", nil
}

func TestExtractionEndToEnd(t *testing.T) {
	// A large structured tool output (well over the 3000-token floor) that the
	// deterministic path would only re-encode; extraction selects 2 records.
	var recs []string
	for i := 0; i < 400; i++ {
		recs = append(recs, `{"id":`+itoa(i)+`,"name":"item_`+itoa(i)+`","detail":"some detail text for this record"}`)
	}
	arr := "[" + strings.Join(recs, ",") + "]"

	body := []byte(`{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"list_items","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":` + jsonStr(arr) + `}]},
		{"role":"user","content":[{"type":"text","text":"now inspect the results"}]}
	]}`)
	req, err := canon.Decode(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	s := config.Default()
	s.ProtectRecent = 1
	eng := New(s, nil, nil)
	eng.EnableExtract(firstTwoModel{}, DefaultExtractConfig())

	out, rep := eng.Transform(context.Background(), req)
	if len(rep.Candidates) == 0 {
		t.Fatalf("expected at least one extraction candidate")
	}
	outBody, _ := out.Encode()
	ids := FindMarkers(string(outBody))
	if len(ids) == 0 {
		t.Fatalf("expected extracted block to carry a recoverable marker")
	}
	if _, ok := eng.Expand(ids[0]); !ok {
		t.Fatalf("Expand failed for extracted marker")
	}
	if len(outBody) >= len(body) {
		t.Fatalf("extraction did not shrink the request: %d vs %d", len(outBody), len(body))
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
