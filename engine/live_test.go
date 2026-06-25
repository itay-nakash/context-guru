package engine

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/config"
	"github.com/kagenti/lab-context-engineering/internal/cheapmodel"
)

// These are opt-in live checks of the summarize/truncate compactors against a real
// claude-haiku-4-5. They skip unless LABCX_LIVE=1 (+ gateway env), so CI never calls out.
//
//	LABCX_LIVE=1 CGO_ENABLED=1 go test ./engine -run TestLive -v

func liveModel(t *testing.T) Model {
	t.Helper()
	base, key := os.Getenv("ANTHROPIC_BASE_URL"), os.Getenv("ANTHROPIC_AUTH_TOKEN")
	if os.Getenv("LABCX_LIVE") != "1" || base == "" || key == "" {
		t.Skip("set LABCX_LIVE=1 + ANTHROPIC_BASE_URL + ANTHROPIC_AUTH_TOKEN to run the live check")
	}
	return cheapmodel.Anthropic{BaseURL: base, APIKey: key, Model: "claude-haiku-4-5", AuthScheme: "bearer"}
}

func multiTurn(t *testing.T) canon.Request {
	t.Helper()
	body := []byte(`{"system":"You are a coding agent.","messages":[
	  {"role":"user","content":[{"type":"text","text":"Bug: parse_config crashes on empty files. Repo myapp, file src/config.py."}]},
	  {"role":"assistant","content":[{"type":"text","text":"I will read src/config.py."}]},
	  {"role":"user","content":[{"type":"text","text":"src/config.py defines parse_config(path): it opens the file and calls json.load without handling empty content."}]},
	  {"role":"assistant","content":[{"type":"text","text":"I will add an empty-file guard returning {}."}]},
	  {"role":"user","content":[{"type":"text","text":"Tests live in tests/test_config.py; add a case for the empty file."}]},
	  {"role":"assistant","content":[{"type":"text","text":"Patch drafted; running tests."}]},
	  {"role":"user","content":[{"type":"text","text":"All tests pass now. Summarize what changed."}]}
	]}`)
	req, err := canon.Decode(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return req
}

func TestLiveSummarize(t *testing.T) {
	model := liveModel(t)
	s := config.Default()
	s.Compactors = []string{summarizeName}
	s.SummarizeKeepLast = 2
	e := New(s, nil, nil)
	e.EnableSummarize(model, DefaultSummarizeConfig())

	out, _ := e.Transform(context.Background(), multiTurn(t))
	msgs := out.Messages()
	if len(msgs) != 3 { // summary note + last 2
		t.Fatalf("want 3 messages (summary + last 2), got %d", len(msgs))
	}
	note, _ := msgs[0]["content"].(string)
	if !strings.Contains(note, "<summary>") {
		t.Fatalf("summary note missing <summary>: %q", note)
	}
	body, _ := out.Encode()
	ids := FindMarkers(string(body))
	if len(ids) == 0 {
		t.Fatal("no recovery marker on summarized request")
	}
	if _, ok := e.Expand(ids[0]); !ok {
		t.Fatal("Expand failed for summarized span")
	}
	t.Logf("7 messages -> %d (1 summary + last 2); dropped span recoverable via id %s", len(msgs), ids[0])
	t.Logf("LIVE SUMMARY NOTE:\n%s", note)
}

func TestLiveTruncate(t *testing.T) {
	if os.Getenv("LABCX_LIVE") != "1" {
		t.Skip("set LABCX_LIVE=1 to run")
	}
	s := config.Default()
	s.Compactors = []string{truncateName}
	s.TruncateEnabled = true
	s.TruncateKeepLast = 2
	e := New(s, nil, nil)

	out, _ := e.Transform(context.Background(), multiTurn(t))
	msgs := out.Messages()
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages (note + last 2), got %d", len(msgs))
	}
	t.Logf("7 messages -> %d (1 note + last 2)", len(msgs))
	t.Logf("TRUNCATE NOTE:\n%s", msgs[0]["content"].(string))
}
