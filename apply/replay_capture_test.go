package apply_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/apply"
	"github.com/rossoctl/context-guru/store"
	"github.com/tidwall/gjson"
)

// TestReplayCapturedTrafficBreakpointsReachTheWire replays real captured Claude
// Code requests (a CONTEXT_GURU_CAPTURE jsonl: {provider, model, body}) through the
// pipeline and asserts the three things #32 is about:
//
//  1. cacheinject's breakpoints appear on the wire (before the fix: 0 of 46 did);
//  2. the total never exceeds the provider's cap of 4;
//  3. the output stays valid JSON with every tool_use provider field intact.
//
// Skipped unless CONTEXT_GURU_CAPTURE names a readable capture, so CI does not need
// the fixture. Run with:
//
//	CONTEXT_GURU_CAPTURE=/path/to/capture.jsonl go test ./apply/ -run ReplayCaptured
func TestReplayCapturedTrafficBreakpointsReachTheWire(t *testing.T) {
	path := os.Getenv("CONTEXT_GURU_CAPTURE")
	if path == "" {
		t.Skip("set CONTEXT_GURU_CAPTURE to a captured-traffic jsonl to run this")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("cannot read capture %q: %v", path, err)
	}
	defer f.Close()

	cfg := pipe(t, "pipeline: [cacheinject]\n")
	p, _ := cfg.Build(nil)
	st := store.NewMemory(store.Options{})

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	requests, withMarks := 0, 0
	for sc.Scan() {
		var rec struct {
			Provider string          `json:"provider"`
			Body     json.RawMessage `json:"body"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil || len(rec.Body) == 0 {
			continue
		}
		requests++
		before := wireMarks(rec.Body)
		out, _ := apply.Body(context.Background(), p, st,
			bschemas.ModelProvider(rec.Provider), rec.Body, "", false)

		if !gjson.ValidBytes(out) {
			t.Fatalf("request %d: output is not valid JSON", requests)
		}
		after := wireMarks(out)
		if after > 4 {
			t.Fatalf("request %d: %d breakpoints on the wire, provider caps at 4", requests, after)
		}
		if after > before {
			withMarks++
		}
		// Every tool_use block must keep id/name/input — the fields bifrost drops.
		gjson.GetBytes(out, `messages.#.content|@flatten`).ForEach(func(_, blk gjson.Result) bool {
			if blk.Get("type").String() != "tool_use" {
				return true
			}
			if !blk.Get("id").Exists() || !blk.Get("name").Exists() || !blk.Get("input").Exists() {
				t.Fatalf("request %d: tool_use lost a provider field: %s", requests, blk.Raw)
			}
			return true
		})
	}
	if requests == 0 {
		t.Skipf("capture %q held no usable requests", path)
	}
	// The bug: 46 breakpoints applied, 0 forwarded, across 40 real requests. On these
	// captures the unfixed code reaches the wire on 1 of 19 requests (the one message
	// bifrost happens to round-trip), so "nonzero" is not a gate — require a majority.
	if withMarks*2 <= requests {
		t.Fatalf("breakpoints reached the wire on only %d of %d captured requests — still suppressed",
			withMarks, requests)
	}
	t.Logf("replayed %d captured requests; breakpoints reached the wire on %d", requests, withMarks)
}

// wireMarks counts cache_control across system, tools and messages, independently of
// the implementation's own counter.
func wireMarks(body []byte) int {
	n := 0
	for _, p := range []string{"system", "tools"} {
		gjson.GetBytes(body, p).ForEach(func(_, v gjson.Result) bool {
			if v.Get("cache_control").Exists() {
				n++
			}
			return true
		})
	}
	gjson.GetBytes(body, "messages").ForEach(func(_, m gjson.Result) bool {
		if m.Get("cache_control").Exists() {
			n++
		}
		m.Get("content").ForEach(func(_, blk gjson.Result) bool {
			if blk.Get("cache_control").Exists() {
				n++
			}
			return true
		})
		return true
	})
	return n
}
