package apply_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rossoctl/context-guru/apply"
	"github.com/rossoctl/context-guru/components"
	_ "github.com/rossoctl/context-guru/components/all"
	"github.com/rossoctl/context-guru/config"
	"github.com/rossoctl/context-guru/store"
	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

func pipe(t *testing.T, yaml string) *config.Config {
	t.Helper()
	c, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestI1UnmodifiedMessagesAndFieldsPreserved is the cache-safety invariant:
// messages a component doesn't touch, and every non-messages top-level field,
// come out byte-identical (headroom I1).
func TestI1UnmodifiedMessagesAndFieldsPreserved(t *testing.T) {
	cfg := pipe(t, "pipeline: [dedup]\n")
	p, _ := cfg.Build(nil)
	st := store.NewMemory(store.Options{})

	dump := strings.Repeat("repeated tool output line with content\n", 60)
	body, _ := json.Marshal(map[string]any{
		"model":       "gpt-x",
		"temperature": 0.7,
		"top_p":       0.95,
		"metadata":    map[string]any{"user_id": "u1"},
		"messages": []map[string]any{
			{"role": "system", "content": "you are helpful"},
			{"role": "user", "content": "please help"},
			{"role": "tool", "tool_call_id": "a", "content": dump},
			{"role": "tool", "tool_call_id": "b", "content": dump}, // dedup collapses this one
		},
	})

	out, changed := apply.Body(context.Background(), p, st, bschemas.OpenAI, body, "", false)
	if !changed {
		t.Fatal("expected dedup to change the body")
	}

	// Non-messages fields: byte-identical.
	for _, path := range []string{"model", "temperature", "top_p", "metadata.user_id"} {
		if gjson.GetBytes(out, path).Raw != gjson.GetBytes(body, path).Raw {
			t.Fatalf("field %q not preserved: %q -> %q", path, gjson.GetBytes(body, path).Raw, gjson.GetBytes(out, path).Raw)
		}
	}
	// Untouched messages (system, user, first tool) are byte-identical.
	for _, i := range []string{"0", "1", "2"} {
		if gjson.GetBytes(out, "messages."+i).Raw != gjson.GetBytes(body, "messages."+i).Raw {
			t.Fatalf("message %s should be unmodified:\n old=%s\n new=%s", i,
				gjson.GetBytes(body, "messages."+i).Raw, gjson.GetBytes(out, "messages."+i).Raw)
		}
	}
	// Only the duplicate (index 3) changed.
	if gjson.GetBytes(out, "messages.3.content").Raw == gjson.GetBytes(body, "messages.3.content").Raw {
		t.Fatal("the duplicate tool output should have been collapsed")
	}
}

// TestLosslessGuardProtectsUnmodeledFields is the data-safety invariant: a
// component that modifies a message bifrost can't round-trip losslessly (here an
// Anthropic user turn carrying a tool_result block, whose payload bifrost drops)
// must NOT corrupt it. cacheinject would add cache_control to that boundary
// message; the guard discards the change rather than splice a lossy re-marshal.
func TestLosslessGuardProtectsUnmodeledFields(t *testing.T) {
	cfg := pipe(t, "pipeline: [cacheinject]\n")
	p, _ := cfg.Build(nil)
	st := store.NewMemory(store.Options{})

	body, _ := json.Marshal(map[string]any{
		"model": "claude-x",
		"messages": []any{
			map[string]any{"role": "user", "content": "please run the tool"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "CRITICAL TOOL OUTPUT that must survive"},
			}},
			map[string]any{"role": "user", "content": "now answer"},
		},
	})

	out, _ := apply.Body(context.Background(), p, st, bschemas.Anthropic, body, "", false)

	// The tool_result payload must be byte-identical — never dropped.
	if gjson.GetBytes(out, "messages.1").Raw != gjson.GetBytes(body, "messages.1").Raw {
		t.Fatalf("tool_result message corrupted:\n old=%s\n new=%s",
			gjson.GetBytes(body, "messages.1").Raw, gjson.GetBytes(out, "messages.1").Raw)
	}
	if !strings.Contains(string(out), "CRITICAL TOOL OUTPUT") {
		t.Fatalf("tool_result content was dropped: %s", out)
	}
}

// TestMixedContentNotFlattened proves an offload skips a tool message that
// carries a non-text block (an image), so the image is never silently dropped.
func TestMixedContentNotFlattened(t *testing.T) {
	cfg := pipe(t, "pipeline: [collapse]\ncomponents:\n  collapse: {max_tokens: 5, head_lines: 1, tail_lines: 1}\n")
	p, _ := cfg.Build(nil)
	st := store.NewMemory(store.Options{})

	long := strings.Repeat("verbose tool line that would normally be collapsed\n", 40)
	body, _ := json.Marshal(map[string]any{
		"model": "gpt-x",
		"messages": []any{
			map[string]any{"role": "user", "content": "go"},
			map[string]any{"role": "tool", "tool_call_id": "a", "content": []any{
				map[string]any{"type": "text", "text": long},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
			}},
		},
	})

	out, changed := apply.Body(context.Background(), p, st, bschemas.OpenAI, body, "", false)
	if changed {
		t.Fatal("collapse must skip a mixed text+image message, not rewrite it")
	}
	if gjson.GetBytes(out, "messages.1.content.1.image_url.url").String() != "data:image/png;base64,AAAA" {
		t.Fatalf("image block was dropped: %s", out)
	}
}

// TestAnthropicToolResultOffloaded is the payoff for the normalization layer:
// dedup must now fire on Anthropic tool outputs (tool_result blocks inside user
// messages), collapsing a duplicate while preserving the block's siblings
// (tool_use_id, is_error) and every other message byte-for-byte.
func TestAnthropicToolResultOffloaded(t *testing.T) {
	cfg := pipe(t, "pipeline: [dedup]\ncomponents:\n  dedup: {min_tokens: 20}\n")
	p, _ := cfg.Build(nil)
	st := store.NewMemory(store.Options{})

	long := strings.Repeat("a line of tool output that repeats verbatim across two calls\n", 30)
	body, _ := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "fix the failing test"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": long},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t2", "is_error": false, "content": long},
			}},
		},
	})

	out, changed := apply.Body(context.Background(), p, st, bschemas.Anthropic, body, "", false)
	if !changed {
		t.Fatal("dedup should have fired on the Anthropic tool_result duplicate")
	}
	// First tool_result stays verbatim; the duplicate is collapsed to a pointer.
	if gjson.GetBytes(out, "messages.1.content.0.content").String() != long {
		t.Fatal("the first tool_result must be left untouched")
	}
	dup := gjson.GetBytes(out, "messages.2.content.0.content").String()
	if !strings.Contains(dup, "identical to an earlier") || !strings.Contains(dup, "<<cg:") {
		t.Fatalf("the duplicate tool_result was not collapsed: %q", dup)
	}
	// Block siblings survive the rewrite (only the content string changed).
	if gjson.GetBytes(out, "messages.2.content.0.tool_use_id").String() != "t2" {
		t.Fatalf("tool_use_id must be preserved: %s", gjson.GetBytes(out, "messages.2").Raw)
	}
	if gjson.GetBytes(out, "messages.2.content.0.type").String() != "tool_result" {
		t.Fatal("block type must be preserved")
	}
	if gjson.GetBytes(out, "model").String() != "claude-sonnet-4-6" {
		t.Fatal("model field must be preserved")
	}
}

// TestAnthropicStructuredToolResultUntouched proves we never rewrite a
// tool_result whose content is a structured array (we can't losslessly project
// it to a string), so no data is dropped.
func TestAnthropicStructuredToolResultUntouched(t *testing.T) {
	cfg := pipe(t, "pipeline: [collapse]\ncomponents:\n  collapse: {max_tokens: 5, head_lines: 1, tail_lines: 1}\n")
	p, _ := cfg.Build(nil)
	st := store.NewMemory(store.Options{})

	body, _ := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "go"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": []any{
					map[string]any{"type": "text", "text": strings.Repeat("structured line\n", 40)},
					map[string]any{"type": "image", "source": map[string]any{"data": "AAAA"}},
				}},
			}},
		},
	})
	out, changed := apply.Body(context.Background(), p, st, bschemas.Anthropic, body, "", false)
	if changed {
		t.Fatal("structured tool_result content must not be rewritten")
	}
	if gjson.GetBytes(out, "messages.1.content.0.content.1.source.data").String() != "AAAA" {
		t.Fatalf("structured tool_result content was corrupted: %s", out)
	}
}

type stubModel struct{ resp string }

func (m stubModel) Complete(context.Context, string) (string, error) { return m.resp, nil }

// TestSummarizeCountChangeLossless: summarize restructures [system,u1,tool,final]
// into [system, <summary>, final]; apply must keep the retained messages and all
// non-message fields byte-identical while the count drops.
func TestSummarizeCountChangeLossless(t *testing.T) {
	cfg := pipe(t, "pipeline: [summarize]\ncomponents:\n  summarize: {keep_last: 1, start_from_message: 0, min_tokens: 1}\n")
	p, _ := cfg.Build(nil)
	st := store.NewMemory(store.Options{})

	body, _ := json.Marshal(map[string]any{
		"model":       "gpt-x",
		"temperature": 0.3,
		"messages": []map[string]any{
			{"role": "system", "content": "you are helpful"},
			{"role": "user", "content": "do the task"},
			{"role": "tool", "tool_call_id": "a", "content": strings.Repeat("verbose tool output\n", 50)},
			{"role": "user", "content": "the final question"},
		},
	})

	out, changed := apply.BodyWithModel(context.Background(), p, st, bschemas.OpenAI, body, "", false,
		components.ModelSpec{Incoming: stubModel{resp: "essential facts"}})
	if !changed {
		t.Fatal("summarize should have restructured the transcript")
	}
	if n := gjson.GetBytes(out, "messages.#").Int(); n != 3 {
		t.Fatalf("expected 3 messages after summarize, got %d: %s", n, out)
	}
	// Non-message fields byte-identical.
	for _, path := range []string{"model", "temperature"} {
		if gjson.GetBytes(out, path).Raw != gjson.GetBytes(body, path).Raw {
			t.Fatalf("field %q not preserved", path)
		}
	}
	// Retained messages (system msg0, final user msg) byte-identical to originals.
	if gjson.GetBytes(out, "messages.0").Raw != gjson.GetBytes(body, "messages.0").Raw {
		t.Fatal("msg0 must be byte-identical")
	}
	if gjson.GetBytes(out, "messages.2").Raw != gjson.GetBytes(body, "messages.3").Raw {
		t.Fatalf("the final message must be preserved verbatim: %s", gjson.GetBytes(out, "messages.2").Raw)
	}
	// The inserted summary carries the marker for expand recovery.
	if s := gjson.GetBytes(out, "messages.1.content").String(); !strings.Contains(s, "History Summary") || !strings.Contains(s, "<<cg:") {
		t.Fatalf("summary message missing wrapper/marker: %q", s)
	}
}

func TestNoMessagesForwardsUnchanged(t *testing.T) {
	cfg := pipe(t, "pipeline: [dedup]\n")
	p, _ := cfg.Build(nil)
	body := []byte(`{"model":"x","prompt":"legacy completion"}`)
	out, changed := apply.Body(context.Background(), p, store.NewMemory(store.Options{}), bschemas.OpenAI, body, "", false)
	if changed || string(out) != string(body) {
		t.Fatalf("no messages array => forward unchanged; got changed=%v %s", changed, out)
	}
}
