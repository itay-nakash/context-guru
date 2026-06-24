package cheapmodel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicSkipsNonTextLeadingBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"thinking","text":""},{"type":"text","text":"RESULT"}]}`)
	}))
	defer srv.Close()
	got, err := Anthropic{BaseURL: srv.URL, Model: "m"}.Complete(context.Background(), "p")
	if err != nil || got != "RESULT" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestOpenAIComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"OUT"}}]}`)
	}))
	defer srv.Close()
	got, err := OpenAI{BaseURL: srv.URL, Model: "m"}.Complete(context.Background(), "p")
	if err != nil || got != "OUT" {
		t.Fatalf("got %q err %v", got, err)
	}
}
