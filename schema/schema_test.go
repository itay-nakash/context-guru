package schema

import (
	"testing"

	bschemas "github.com/maximhq/bifrost/core/schemas"
)

func strp(s string) *string { return &s }

func TestRewritable(t *testing.T) {
	text := "hello"
	cases := []struct {
		name string
		msg  bschemas.ChatMessage
		want bool
	}{
		{"nil content", bschemas.ChatMessage{Role: bschemas.ChatMessageRoleTool}, true},
		{"string content", bschemas.ChatMessage{Content: &bschemas.ChatMessageContent{ContentStr: strp("x")}}, true},
		{"all text blocks", bschemas.ChatMessage{Content: &bschemas.ChatMessageContent{ContentBlocks: []bschemas.ChatContentBlock{
			{Type: bschemas.ChatContentBlockTypeText, Text: &text},
		}}}, true},
		{"has image block", bschemas.ChatMessage{Content: &bschemas.ChatMessageContent{ContentBlocks: []bschemas.ChatContentBlock{
			{Type: bschemas.ChatContentBlockTypeText, Text: &text},
			{Type: bschemas.ChatContentBlockTypeImage},
		}}}, false},
		{"unmodeled tool_result block", bschemas.ChatMessage{Content: &bschemas.ChatMessageContent{ContentBlocks: []bschemas.ChatContentBlock{
			{Type: bschemas.ChatContentBlockType("tool_result")},
		}}}, false},
	}
	for _, c := range cases {
		if got := Rewritable(c.msg); got != c.want {
			t.Errorf("%s: Rewritable=%v want %v", c.name, got, c.want)
		}
	}
}

func TestMessagesTokens(t *testing.T) {
	if MessagesTokens(nil) != 0 {
		t.Fatal("nil request => 0 tokens")
	}
	req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
		{Content: &bschemas.ChatMessageContent{ContentStr: strp("hello world this is some text")}},
	}}
	if MessagesTokens(req) <= 0 {
		t.Fatal("expected a positive token count for non-empty content")
	}
	if TextTokens("") != 0 {
		t.Fatal("empty string => 0 tokens")
	}
}

func TestCloneMessagesIndependent(t *testing.T) {
	orig := []bschemas.ChatMessage{{
		Role:    bschemas.ChatMessageRoleTool,
		Content: &bschemas.ChatMessageContent{ContentStr: strp("x")},
	}}
	clone := CloneMessages(orig)
	SetMessageText(&clone[0], "mutated")
	if MessageText(orig[0]) != "x" {
		t.Fatal("clone must be independent of the original")
	}
	if CloneMessages(nil) != nil {
		t.Fatal("nil in => nil out")
	}
}

func TestBlockTextNonText(t *testing.T) {
	if BlockText(bschemas.ChatContentBlock{Type: bschemas.ChatContentBlockTypeImage}) != "" {
		t.Fatal("a non-text (image) block has no text")
	}
	if MessageText(bschemas.ChatMessage{}) != "" {
		t.Fatal("nil content => empty text")
	}
}

func TestMessageTextBlocks(t *testing.T) {
	a, b := "foo", "bar"
	m := bschemas.ChatMessage{Content: &bschemas.ChatMessageContent{ContentBlocks: []bschemas.ChatContentBlock{
		{Type: bschemas.ChatContentBlockTypeText, Text: &a},
		{Type: bschemas.ChatContentBlockTypeText, Text: &b},
	}}}
	if got := MessageText(m); got != "foobar" {
		t.Fatalf("MessageText=%q want foobar", got)
	}
}
