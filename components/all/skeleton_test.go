//go:build cg_skeleton

// Skeleton tests live here (behind the cg_skeleton tag) because the skeleton
// component is only registered when built with that tag. The shared helpers
// (run, toolMsg, userMsg) come from the untagged test files in this package,
// which also compile under the tag.

package all_test

import (
	"strings"
	"testing"

	"github.com/kagenti/context-guru/expand"
	"github.com/kagenti/context-guru/schema"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestSkeletonElidesBodies(t *testing.T) {
	code := "```go\n" +
		"package main\n\n" +
		"func Add(a, b int) int {\n" +
		"\tsum := 0\n\tsum += a\n\tsum += b\n\tfor i := 0; i < 10; i++ { sum += i }\n\treturn sum\n}\n\n" +
		"func Mul(a, b int) int {\n\tp := 0\n\tfor i := 0; i < b; i++ { p += a }\n\treturn p\n}\n" +
		"```"
	req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{toolMsg(code)}}
	_, st := run(t, "pipeline: [skeleton]\ncomponents:\n  skeleton: {min_tokens: 5}\n", req)
	got := schema.MessageText(req.Input[0])
	if !strings.Contains(got, "func Add(a, b int) int") || !strings.Contains(got, "{ … }") {
		t.Fatalf("skeleton should keep signatures and elide bodies: %q", got)
	}
	if strings.Contains(got, "sum += a") {
		t.Fatalf("skeleton should have removed body statements: %q", got)
	}
	keys := expand.ParseMarkers(got)
	if len(keys) != 1 {
		t.Fatalf("expected expand marker, got %q", got)
	}
	if orig, ok := expand.Resolve(st, keys[0]); !ok || !strings.Contains(orig, "sum += a") {
		t.Fatal("expand did not recover original source")
	}
}

// TestSkeletonSkipsNonToolMessages locks the role-scope fix: skeleton must
// leave a user/assistant message's own code untouched (only tool outputs are
// offloaded), otherwise it would mangle the caller's code with no live recovery.
func TestSkeletonSkipsNonToolMessages(t *testing.T) {
	code := "```go\nfunc Add(a, b int) int {\n\tsum := 0\n\tsum += a\n\tsum += b\n\tfor i := 0; i < 10; i++ { sum += i }\n\treturn sum\n}\n```"
	req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{userMsg(code)}}
	run(t, "pipeline: [skeleton]\ncomponents:\n  skeleton: {min_tokens: 5}\n", req)
	if got := schema.MessageText(req.Input[0]); got != code {
		t.Fatalf("skeleton must not rewrite a non-tool message; got %q", got)
	}
}
