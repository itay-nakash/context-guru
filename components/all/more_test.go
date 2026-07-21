package all_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	_ "github.com/rossoctl/context-guru/components/all"
	"github.com/rossoctl/context-guru/config"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
)

func run(t *testing.T, yaml string, req *schemas.BifrostChatRequest) (*components.RunReport, store.Store) {
	t.Helper()
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := cfg.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory(store.Options{})
	c := &components.Ctx{Ctx: context.Background(), Session: "s", Store: st}
	return pipe.Run(req, c), st
}

func TestFormatCompactsJSON(t *testing.T) {
	type row struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	rows := make([]row, 20)
	for i := range rows {
		rows[i] = row{ID: i, Name: "item-with-a-longish-name"}
	}
	pretty, _ := json.MarshalIndent(rows, "", "    ")
	req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{toolMsg(string(pretty))}}
	before := schema.MessagesTokens(req)
	run(t, "pipeline: [format]\n", req)
	after := schema.MessagesTokens(req)
	if after >= before {
		t.Fatalf("format did not compact JSON: before=%d after=%d", before, after)
	}
	// still valid JSON (lossless)
	var back []row
	if err := json.Unmarshal([]byte(schema.MessageText(req.Input[0])), &back); err != nil || len(back) != 20 {
		t.Fatalf("format broke JSON validity: %v", err)
	}
}

// TestSkeletonElidesBodies moved to skeleton_test.go (cg_skeleton build tag).

func TestFailedRunSupersedes(t *testing.T) {
	run1 := "=== test session starts ===\n" + strings.Repeat("detail line about the failing run\n", 20) + "3 failed, 2 passed in 1.2s\n"
	run2 := "=== test session starts ===\n" + strings.Repeat("detail line about the passing run\n", 20) + "0 failed, 5 passed in 1.0s\n"
	req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{toolMsg(run1), toolMsg("some unrelated note"), toolMsg(run2)}}
	_, st := run(t, "pipeline: [failed_run]\n", req)
	first := schema.MessageText(req.Input[0])
	if !strings.Contains(first, "superseded") {
		t.Fatalf("earlier run should be collapsed: %q", first)
	}
	if !strings.Contains(schema.MessageText(req.Input[2]), "0 failed") {
		t.Fatal("latest run must be kept in full")
	}
	if keys := expand.ParseMarkers(first); len(keys) != 1 {
		t.Fatal("collapsed run should carry an expand marker")
	} else if orig, ok := expand.Resolve(st, keys[0]); !ok || !strings.Contains(orig, "3 failed") {
		t.Fatal("expand must recover the superseded run")
	}
}

func TestCollapseHeadTail(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString("log line number with some content ")
		b.WriteString(strings.Repeat("x", 5))
		b.WriteByte('\n')
	}
	req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{toolMsg(b.String())}}
	before := schema.MessagesTokens(req)
	_, st := run(t, "pipeline: [collapse]\ncomponents:\n  collapse: {max_tokens: 20, head_lines: 3, tail_lines: 3}\n", req)
	got := schema.MessageText(req.Input[0])
	if schema.TextTokens(got) >= before || !strings.Contains(got, "lines omitted") {
		t.Fatalf("collapse should head/tail truncate: %q", got)
	}
	if keys := expand.ParseMarkers(got); len(keys) != 1 {
		t.Fatal("collapse should leave an expand marker")
	} else if _, ok := expand.Resolve(st, keys[0]); !ok {
		t.Fatal("collapse original must be recoverable")
	}
}
