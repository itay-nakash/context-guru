package components

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func mkMsg(s string) schemas.ChatMessage {
	t := s
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &t}}
}

func TestTriggerFires(t *testing.T) {
	// ~1 message with a lot of tokens vs several tiny messages.
	big := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{mkMsg(strings.Repeat("word ", 4000))}}
	deep := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
		mkMsg("a"), mkMsg("b"), mkMsg("c"), mkMsg("d"), mkMsg("e"),
	}}

	cases := []struct {
		name string
		tr   Trigger
		req  *schemas.BifrostChatRequest
		want bool
	}{
		{"zero fires always", Trigger{}, deep, true},
		{"token gate met", Trigger{MinRequestTokens: 1000}, big, true},
		{"token gate not met", Trigger{MinRequestTokens: 1000}, deep, false},
		{"message gate met", Trigger{MinMessages: 5}, deep, true},
		{"message gate not met", Trigger{MinMessages: 6}, deep, false},
		{"both gates ANDed (msg fails)", Trigger{MinRequestTokens: 1, MinMessages: 99}, big, false},
		{"MinOutputTokens does not affect Fires", Trigger{MinOutputTokens: 99999}, deep, true},
	}
	for _, c := range cases {
		if got := c.tr.Fires(c.req); got != c.want {
			t.Errorf("%s: Fires=%v want %v", c.name, got, c.want)
		}
	}
}
