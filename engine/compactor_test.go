package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/config"
)

// fakeModel returns a fixed completion — enough to drive summarize without a network.
type fakeModel struct{ out string }

func (f fakeModel) Complete(_ context.Context, _ string) (string, error) { return f.out, nil }

// sixMessages builds a request with one system field and six alternating turns.
func sixMessages(t *testing.T) canon.Request {
	t.Helper()
	body := []byte(`{"system":"be helpful","messages":[
		{"role":"user","content":[{"type":"text","text":"task: fix the bug"}]},
		{"role":"assistant","content":[{"type":"text","text":"looking"}]},
		{"role":"user","content":[{"type":"text","text":"more context"}]},
		{"role":"assistant","content":[{"type":"text","text":"investigating"}]},
		{"role":"user","content":[{"type":"text","text":"any progress"}]},
		{"role":"assistant","content":[{"type":"text","text":"almost done"}]}
	]}`)
	req, err := canon.Decode(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return req
}

// TestTruncateKeepsLastAndRecovers: naive truncate keeps the last N messages behind one
// recoverable note.
func TestTruncateKeepsLastAndRecovers(t *testing.T) {
	s := config.Default()
	s.Compactors = []string{truncateName}
	s.TruncateEnabled = true
	s.TruncateKeepLast = 2
	e := New(s, nil, nil)

	out, _ := e.Transform(context.Background(), sixMessages(t))
	msgs := out.Messages()
	if len(msgs) != 3 { // 1 note + last 2
		t.Fatalf("want 3 messages (note + last 2), got %d", len(msgs))
	}
	note, _ := msgs[0]["content"].(string)
	if !strings.Contains(note, "Truncated history") {
		t.Fatalf("first message is not the truncation note: %q", note)
	}
	body, _ := out.Encode()
	ids := FindMarkers(string(body))
	if len(ids) == 0 {
		t.Fatalf("truncation left no recoverable marker")
	}
	if _, ok := e.Expand(ids[0]); !ok {
		t.Fatalf("Expand could not recover the truncated span %s", ids[0])
	}
}

// TestSummarizeReplacesHistory: summarize replaces older turns with one <summary> and
// keeps the last KeepLast verbatim; the dropped span is recoverable.
func TestSummarizeReplacesHistory(t *testing.T) {
	s := config.Default()
	s.Compactors = []string{summarizeName}
	s.SummarizeKeepLast = 1
	e := New(s, nil, nil)
	e.EnableSummarize(fakeModel{out: "KEYFACTS about the bug"}, DefaultSummarizeConfig())

	out, _ := e.Transform(context.Background(), sixMessages(t))
	msgs := out.Messages()
	if len(msgs) != 2 { // 1 summary note + last 1
		t.Fatalf("want 2 messages (summary + last 1), got %d", len(msgs))
	}
	note, _ := msgs[0]["content"].(string)
	if !strings.Contains(note, "<summary>") || !strings.Contains(note, "KEYFACTS") {
		t.Fatalf("summary note missing summary/content: %q", note)
	}
	body, _ := out.Encode()
	if ids := FindMarkers(string(body)); len(ids) == 0 {
		t.Fatalf("summarize left no recoverable marker")
	}
}

// taggerCompactor is a trivial custom Compactor used to prove a new approach plugs in
// purely by name, with no engine edits.
type taggerCompactor struct{}

func (taggerCompactor) Name() string          { return "tagger" }
func (taggerCompactor) Enabled(*Context) bool { return true }
func (taggerCompactor) Compact(req canon.Request, _ *Report, _ *Context) (canon.Request, error) {
	msgs := req.Messages()
	msgs = append(msgs, map[string]any{"role": "user", "content": "TAGGED"})
	req.SetMessages(msgs)
	return req, nil
}

// TestCustomCompactorByName: registering a Compactor by name and listing it in
// Settings.Compactors runs it — the generic-abstraction extension path.
func TestCustomCompactorByName(t *testing.T) {
	s := config.Default()
	s.Compactors = []string{"tagger"}
	e := New(s, nil, nil)
	e.Register("tagger", taggerCompactor{})

	out, _ := e.Transform(context.Background(), sixMessages(t))
	msgs := out.Messages()
	last, _ := msgs[len(msgs)-1]["content"].(string)
	if last != "TAGGED" {
		t.Fatalf("custom compactor did not run; last message = %q", last)
	}
}

// TestIncomingModelSpec: a compactor with ModelSpec{UseIncoming:true} no-ops without a
// request model and runs when the host supplies one via WithRequestModel.
func TestIncomingModelSpec(t *testing.T) {
	s := config.Default()
	s.Compactors = []string{summarizeName}
	s.SummarizeKeepLast = 1
	e := New(s, nil, nil)
	e.EnableSummarizeSpec(ModelSpec{UseIncoming: true}, DefaultSummarizeConfig())

	// No request model → fail-open no-op (all six messages survive).
	out, _ := e.Transform(context.Background(), sixMessages(t))
	if len(out.Messages()) != 6 {
		t.Fatalf("without a request model, summarize should no-op; got %d messages", len(out.Messages()))
	}

	// With a request model → summarized down to note + last 1.
	ctx := WithRequestModel(context.Background(), fakeModel{out: "<summary>incoming</summary>"})
	out2, _ := e.Transform(ctx, sixMessages(t))
	if len(out2.Messages()) != 2 {
		t.Fatalf("with a request model, summarize should run; got %d messages", len(out2.Messages()))
	}
}
