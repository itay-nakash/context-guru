package all_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kagenti/context-guru/expand"
	"github.com/kagenti/context-guru/schema"
	bschemas "github.com/maximhq/bifrost/core/schemas"
)

func userMsg(text string) bschemas.ChatMessage {
	t := text
	return bschemas.ChatMessage{Role: bschemas.ChatMessageRoleUser, Content: &bschemas.ChatMessageContent{ContentStr: &t}}
}

func TestMaskHidesOlderToolOutputs(t *testing.T) {
	big := strings.Repeat("some older tool output content line\n", 30)
	msgs := make([]bschemas.ChatMessage, 5)
	for i := range msgs {
		msgs[i] = toolMsg(fmt.Sprintf("output %d\n%s", i, big))
	}
	req := &bschemas.BifrostChatRequest{Input: msgs}
	_, st := run(t, "pipeline: [mask]\ncomponents:\n  mask: {keep_recent: 2, min_tokens: 10}\n", req)

	for i := 0; i < 3; i++ {
		if !strings.Contains(schema.MessageText(req.Input[i]), "masked") {
			t.Fatalf("msg %d should be masked", i)
		}
	}
	if !strings.Contains(schema.MessageText(req.Input[4]), "output 4") {
		t.Fatal("most recent tool output must stay verbatim")
	}
	keys := expand.ParseMarkers(schema.MessageText(req.Input[0]))
	if orig, ok := expand.Resolve(st, keys[0]); !ok || !strings.Contains(orig, "output 0") {
		t.Fatal("masked original must be recoverable")
	}
}

func TestSmartCrushSamplesArrayKeepsErrors(t *testing.T) {
	type row struct {
		ID  int    `json:"id"`
		Msg string `json:"msg"`
	}
	rows := make([]row, 20)
	for i := range rows {
		rows[i] = row{ID: i, Msg: "routine event happened here with detail"}
	}
	rows[10].Msg = "ERROR: the thing exploded catastrophically"
	arr, _ := json.Marshal(rows)
	req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{toolMsg(string(arr))}}
	before := schema.MessagesTokens(req)
	_, st := run(t, "pipeline: [smartcrush]\ncomponents:\n  smartcrush: {min_items: 5, min_tokens: 50, keep_first: 3, keep_last: 2}\n", req)

	got := schema.MessageText(req.Input[0])
	if schema.TextTokens(got) >= before {
		t.Fatalf("smartcrush should shrink the array: %q", got)
	}
	if !strings.Contains(got, "exploded catastrophically") {
		t.Fatal("the error item must be preserved")
	}
	keys := expand.ParseMarkers(got)
	if orig, ok := expand.Resolve(st, keys[0]); !ok || !strings.Contains(orig, `"id":15`) {
		t.Fatal("dropped items must be recoverable in full")
	}
}

func TestPhiEvictTrimsToBudgetKeepsNewest(t *testing.T) {
	filler := strings.Repeat("irrelevant verbose padding content here\n", 40)
	req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
		userMsg("please summarize the deployment rollout status"),
		toolMsg(filler),
		toolMsg(filler + "unrelated"),
		toolMsg("the deployment rollout status is green and healthy\n" + filler),
	}}
	before := schema.MessagesTokens(req)
	_, st := run(t, "pipeline: [phi_evict]\ncomponents:\n  phi_evict: {budget_tokens: 400, weights: aggressive}\n", req)
	after := schema.MessagesTokens(req)
	if after >= before {
		t.Fatalf("phi_evict should trim toward budget: before=%d after=%d", before, after)
	}
	// newest tool output (index 3) must survive.
	if strings.Contains(schema.MessageText(req.Input[3]), "evicted") {
		t.Fatal("most recent tool output must not be evicted")
	}
	// at least one earlier one evicted + recoverable.
	var found bool
	for _, i := range []int{1, 2} {
		if keys := expand.ParseMarkers(schema.MessageText(req.Input[i])); len(keys) == 1 {
			if _, ok := expand.Resolve(st, keys[0]); ok {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("expected an evicted, recoverable earlier output")
	}
}

func TestExtractProjectsRelevantLines(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("noise line about unrelated internals blah blah\n")
	}
	b.WriteString("the authentication token refresh succeeded for user\n")
	for i := 0; i < 40; i++ {
		b.WriteString("more noise line about unrelated internals\n")
	}
	req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
		userMsg("why did the authentication token refresh happen"),
		toolMsg(b.String()),
	}}
	before := schema.MessagesTokens(req)
	_, st := run(t, "pipeline: [extract]\ncomponents:\n  extract: {min_tokens: 50, head_lines: 2, tail_lines: 2}\n", req)
	got := schema.MessageText(req.Input[1])
	if schema.TextTokens(got) >= before {
		t.Fatalf("extract should project down: %q", got[:min(80, len(got))])
	}
	if !strings.Contains(got, "authentication token refresh succeeded") {
		t.Fatal("the query-relevant line must be kept")
	}
	if keys := expand.ParseMarkers(got); len(keys) != 1 {
		t.Fatal("extract should leave an expand marker")
	} else if _, ok := expand.Resolve(st, keys[0]); !ok {
		t.Fatal("extract original must be recoverable")
	}
}

// TestSkeletonSkipsNonToolMessages moved to skeleton_test.go (cg_skeleton tag).

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
