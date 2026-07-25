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
		if got := c.tr.Fires(c.req, 0); got != c.want { // window 0 = only absolutes apply
			t.Errorf("%s: Fires=%v want %v", c.name, got, c.want)
		}
	}
}

func TestTriggerFractions(t *testing.T) {
	// big ~ 4000 "word " tokens; window 200k.
	big := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{mkMsg(strings.Repeat("word ", 4000))}}
	const W = 200000

	// MinRequestFrac 0.6*200k=120k > big(~4k) => does not fire.
	if (Trigger{MinRequestFrac: 0.6}).Fires(big, W) {
		t.Error("frac 0.6 of 200k should not fire on a ~4k request")
	}
	// A tiny frac fires.
	if !(Trigger{MinRequestFrac: 0.001}).Fires(big, W) {
		t.Error("frac 0.001 of 200k (=200) should fire on a ~4k request")
	}
	// Unknown window (0) => fraction ignored, so a big frac still fires.
	if !(Trigger{MinRequestFrac: 0.9}).Fires(big, 0) {
		t.Error("fraction must be ignored when window unknown (0)")
	}
	// Absolute wins when larger than the fraction term.
	if !(Trigger{MinRequestTokens: 1, MinRequestFrac: 0.9}).Fires(big, 0) {
		t.Error("with window 0, only the (met) absolute applies")
	}

	// OutputFloor precedence: absolute > frac > legacy.
	if got := (Trigger{MinOutputTokens: 500}).OutputFloor(W, 300); got != 500 {
		t.Errorf("absolute floor should win: got %d", got)
	}
	if got := (Trigger{MinOutputFrac: 0.01}).OutputFloor(W, 300); got != 2000 {
		t.Errorf("frac floor 0.01*200k=2000: got %d", got)
	}
	if got := (Trigger{}).OutputFloor(0, 300); got != 300 {
		t.Errorf("legacy default when nothing set / window unknown: got %d", got)
	}

	// IsHuge: 0.15*200k=30k threshold.
	if !(Trigger{HugeOutputFrac: 0.15}).IsHuge(31000, W) {
		t.Error("31k output should be huge at 0.15*200k")
	}
	if (Trigger{HugeOutputFrac: 0.15}).IsHuge(31000, 0) {
		t.Error("huge must be false when window unknown")
	}
	if (Trigger{}).IsHuge(999999, W) {
		t.Error("huge must be false when HugeOutputFrac unset")
	}
}
