package apply_test

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/apply"
	"github.com/rossoctl/context-guru/store"
	"github.com/tidwall/gjson"
)

// testdata/terminus2_request.json is a REAL request body reconstructed from a
// terminal-bench trial (runs-tb50-terminus, task adaptive-rejection-sampler): the
// system prompt as the first user message, then alternating assistant/user turns
// where each user turn is a captured terminal screen behind a "New Terminal
// Output:" marker. Two of those screens are ~10 KB apt-get logs.
//
// Before embedded-tool-output support this body produced acted=0 on every
// component and tokens_before == tokens_after. These tests pin the fix: the
// components now see the terminal output, and everything around it survives
// byte-identical.
func terminus2Body(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/terminus2_request.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestEmbeddedTerminalOutputIsCompacted is the regression test for the whole
// point of this change: a terminus-2 request must actually shrink. `mask` is
// age-based, so it fires on terminal output regardless of content.
func TestEmbeddedTerminalOutputIsCompacted(t *testing.T) {
	body := terminus2Body(t)
	cfg := pipe(t, "pipeline: [mask]\ncomponents:\n  mask:\n    keep_recent: 1\n    min_tokens: 50\n")
	p, _ := cfg.Build(nil)
	st := store.NewMemory(store.Options{})

	out, changed := apply.Body(context.Background(), p, st, bschemas.OpenAI, body, "s1", false)
	if !changed {
		t.Fatal("terminus-2 embedded terminal output must be compacted; got no change (the acted=0 bug)")
	}
	if len(out) >= len(body) {
		t.Fatalf("expected the body to shrink: %d -> %d bytes", len(body), len(out))
	}

	// The preamble marker and the surrounding envelope are prose the agent needs;
	// only the captured output between them may be rewritten.
	first := gjson.GetBytes(out, "messages.2.content").String()
	if !strings.Contains(first, "New Terminal Output:\n") {
		t.Errorf("the preamble marker must survive verbatim, got:\n%q", clipStr(first))
	}
	if !strings.Contains(first, "Previous response had warnings:") {
		t.Errorf("instruction prose before the marker must survive, got:\n%q", clipStr(first))
	}
}

// TestEmbeddedSpliceIsByteLossless is the I1 (cache-safety) invariant for the new
// slot kind: non-messages fields, the untouched system/assistant turns, and the
// most-recent (unmasked) terminal output all come back byte-identical.
func TestEmbeddedSpliceIsByteLossless(t *testing.T) {
	body := terminus2Body(t)
	cfg := pipe(t, "pipeline: [mask]\ncomponents:\n  mask:\n    keep_recent: 1\n    min_tokens: 50\n")
	p, _ := cfg.Build(nil)
	st := store.NewMemory(store.Options{})

	out, changed := apply.Body(context.Background(), p, st, bschemas.OpenAI, body, "s1", false)
	if !changed {
		t.Fatal("expected a change")
	}

	for _, path := range []string{"model", "temperature"} {
		if gjson.GetBytes(out, path).Raw != gjson.GetBytes(body, path).Raw {
			t.Errorf("field %q not preserved: %s -> %s", path,
				gjson.GetBytes(body, path).Raw, gjson.GetBytes(out, path).Raw)
		}
	}
	// The first user message is the system prompt (it QUOTES the marker strings
	// while describing the output format) plus every assistant turn: all untouched.
	if gjson.GetBytes(out, "messages.0").Raw != gjson.GetBytes(body, "messages.0").Raw {
		t.Error("the instruction prompt must not be rewritten (marker mentioned mid-prose)")
	}
	n := len(gjson.GetBytes(body, "messages").Array())
	for i := 1; i < n; i += 2 {
		if gjson.GetBytes(out, "messages."+strconv.Itoa(i)).Raw != gjson.GetBytes(body, "messages."+strconv.Itoa(i)).Raw {
			t.Errorf("assistant message %d must be byte-identical", i)
		}
	}
	// keep_recent: 1 => the newest terminal output stays verbatim.
	last := strconv.Itoa(n - 1)
	if gjson.GetBytes(out, "messages."+last).Raw != gjson.GetBytes(body, "messages."+last).Raw {
		t.Error("the most recent terminal output must be left verbatim by mask")
	}
	// Still valid JSON with the same message count.
	if got := len(gjson.GetBytes(out, "messages").Array()); got != n {
		t.Fatalf("message count changed: %d -> %d", n, got)
	}
}

// TestEmbeddedMarkerNotMatchedInProse guards the false-positive direction: a user
// message that merely TALKS about the markers (as terminus-2's own system prompt
// does) must not be treated as tool output. Without the line-start + length
// requirements this is where the feature would start corrupting instructions.
func TestEmbeddedMarkerNotMatchedInProse(t *testing.T) {
	prose := "Your response will be shown under Current Terminal Screen: which is " +
		"the label used for terminal state. " + strings.Repeat("Describe the plan carefully. ", 40)
	body, _ := json.Marshal(map[string]any{
		"model": "m",
		"messages": []map[string]any{
			{"role": "user", "content": prose},
			{"role": "assistant", "content": "ok"},
			{"role": "user", "content": prose},
		},
	})
	cfg := pipe(t, "pipeline: [mask]\ncomponents:\n  mask:\n    keep_recent: 0\n    min_tokens: 10\n")
	p, _ := cfg.Build(nil)

	out, changed := apply.Body(context.Background(), p, store.NewMemory(store.Options{}),
		bschemas.OpenAI, body, "s2", false)
	if changed {
		t.Fatalf("a mid-sentence marker must not qualify as tool output; body was rewritten:\n%s", clipStr(string(out)))
	}
}

// TestEmbeddedNestedMarkerTakesInnermost pins the nesting rule. terminus-2's
// completion-confirmation prompt wraps a screen that itself starts with a marker,
// and appends a trailing question. The extracted span must start after the LAST
// marker and stop before the trailing instruction, so both stay readable.
func TestEmbeddedNestedMarkerTakesInnermost(t *testing.T) {
	screen := strings.Repeat("root@host:/app# ls -la\ntotal 42\n", 30)
	content := "Current terminal state:\nNew Terminal Output:\n" + screen +
		"\nAre you sure you want to mark the task as complete? " +
		`If so, include "task_complete": true in your JSON response again.`
	body, _ := json.Marshal(map[string]any{
		"model": "m",
		"messages": []map[string]any{
			{"role": "user", "content": content},
			{"role": "assistant", "content": "ok"},
			{"role": "user", "content": content},
		},
	})
	cfg := pipe(t, "pipeline: [mask]\ncomponents:\n  mask:\n    keep_recent: 1\n    min_tokens: 10\n")
	p, _ := cfg.Build(nil)

	out, changed := apply.Body(context.Background(), p, store.NewMemory(store.Options{}),
		bschemas.OpenAI, body, "s3", false)
	if !changed {
		t.Fatal("expected the nested terminal screen to be compacted")
	}
	got := gjson.GetBytes(out, "messages.0.content").String()
	for _, keep := range []string{
		"Current terminal state:\n",
		"New Terminal Output:\n",
		"Are you sure you want to mark the task as complete?",
		`include "task_complete": true`,
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("must preserve %q around the compacted span, got:\n%q", keep, clipStr(got))
		}
	}
	if strings.Contains(got, screen) {
		t.Error("the terminal screen itself should have been replaced")
	}
}

// TestEmbeddedSummaryMarkerLeavesNoToolReference pins the recommended pairing for
// tool-less agents. They cannot call context_guru_expand, so `marker_mode: summary`
// must leave a self-contained marker that does not invite an impossible call.
func TestEmbeddedSummaryMarkerLeavesNoToolReference(t *testing.T) {
	body := terminus2Body(t)
	if gjson.GetBytes(body, "tools").Exists() {
		t.Fatal("fixture invariant: terminus-2 declares no tools")
	}
	cfg := pipe(t, "pipeline: [mask]\ncomponents:\n  mask:\n    keep_recent: 1\n    min_tokens: 50\n    marker_mode: summary\n")
	p, _ := cfg.Build(nil)

	out, changed := apply.Body(context.Background(), p, store.NewMemory(store.Options{}),
		bschemas.OpenAI, body, "s4", false)
	if !changed {
		t.Fatal("expected compaction")
	}
	if got := string(out); strings.Contains(got, "context_guru_expand") {
		t.Error("summary mode must not reference the expand tool an agent with no tools cannot call")
	}
}

func clipStr(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
