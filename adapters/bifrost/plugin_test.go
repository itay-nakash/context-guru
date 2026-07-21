package bifrost

import (
	"context"
	"strings"
	"testing"
	"time"

	_ "github.com/rossoctl/context-guru/components/all"
	"github.com/rossoctl/context-guru/config"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
	bschemas "github.com/maximhq/bifrost/core/schemas"
)

func toolMsg(text string) bschemas.ChatMessage {
	t := text
	return bschemas.ChatMessage{Role: bschemas.ChatMessageRoleTool, Content: &bschemas.ChatMessageContent{ContentStr: &t}}
}

func newPlugin(t *testing.T, yaml string) *Plugin {
	t.Helper()
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := cfg.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(pipe, store.NewMemory(store.Options{}))
}

func TestPreRequestHookRunsPipeline(t *testing.T) {
	p := newPlugin(t, "pipeline: [dedup]\n")
	dump := strings.Repeat("a verbose repeated tool output line\n", 60)
	chat := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{toolMsg(dump), toolMsg(dump)}}
	req := &bschemas.BifrostRequest{ChatRequest: chat}
	ctx := bschemas.NewBifrostContext(context.Background(), time.Time{})

	before := schema.MessagesTokens(chat)
	if err := p.PreRequestHook(ctx, req); err != nil {
		t.Fatal(err)
	}
	if schema.MessagesTokens(chat) >= before {
		t.Fatal("PreRequestHook did not reduce the duplicate tool output")
	}
}

func TestNonChatRequestPassesThrough(t *testing.T) {
	p := newPlugin(t, "pipeline: [dedup]\n")
	ctx := bschemas.NewBifrostContext(context.Background(), time.Time{})
	if err := p.PreRequestHook(ctx, &bschemas.BifrostRequest{}); err != nil {
		t.Fatalf("nil ChatRequest should be a no-op, got %v", err)
	}
}

func TestSessionResolutionPrefersExplicit(t *testing.T) {
	p := newPlugin(t, "pipeline: [dedup]\n")
	ctx := bschemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(SessionContextKey, "explicit-123")
	chat := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{toolMsg("hi")}}
	if got := p.resolveSession(ctx, chat); got != "explicit-123" {
		t.Fatalf("explicit session id should win, got %q", got)
	}
}

func TestBypassSkips(t *testing.T) {
	p := newPlugin(t, "pipeline: [dedup]\n")
	dump := strings.Repeat("a verbose repeated tool output line\n", 60)
	chat := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{toolMsg(dump), toolMsg(dump)}}
	ctx := bschemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(BypassContextKey, true)
	before := schema.MessagesTokens(chat)
	_ = p.PreRequestHook(ctx, &bschemas.BifrostRequest{ChatRequest: chat})
	if schema.MessagesTokens(chat) != before {
		t.Fatal("bypass should have left the request unchanged")
	}
}
