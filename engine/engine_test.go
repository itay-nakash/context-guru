package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/config"
	"github.com/kagenti/lab-context-engineering/internal/markers"
)

func bigText(n int) string {
	return strings.Repeat("a.go some reasonably long line of file content here\n", n)
}

// TestTransformReducesAndCaches: a stale read is collapsed (reduce) and the request
// gets cache breakpoints (cache stage, since the client didn't self-cache).
func TestTransformReducesAndCaches(t *testing.T) {
	body := []byte(`{"system":"be helpful","tools":[{"name":"read"}],"messages":[
		{"role":"user","content":[{"type":"text","text":"fix bug"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"read","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"` + strings.ReplaceAll(bigText(12), "\n", "\\n") + `"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"u2","name":"edit","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u2","content":"success: edited"}]},
		{"role":"user","content":[{"type":"text","text":"thanks"}]}
	]}`)
	req, err := canon.Decode(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	s := config.Default()
	s.ProtectRecent = 1
	e := New(s, nil, nil)

	out, rep := e.Transform(context.Background(), req)

	if rep.Reduce.TokensSaved <= 0 {
		t.Fatalf("expected reduction; before=%d after=%d", rep.Reduce.TokensBefore, rep.Reduce.TokensAfter)
	}
	if !rep.CacheInjected {
		t.Fatalf("expected cache stage to run on a non-self-caching client")
	}
	// The collapsed block carries a marker recoverable via Expand.
	out2, _ := out.Encode()
	ids := markers.FindIDs(string(out2))
	if len(ids) == 0 {
		t.Fatalf("expected a winnow marker in the reduced request")
	}
	if _, ok := e.Expand(ids[0]); !ok {
		t.Fatalf("Expand could not recover marker %s", ids[0])
	}
}

// TestTransformDisabledIsNoop verifies the global kill switch forwards untouched.
func TestTransformDisabledIsNoop(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	req, _ := canon.Decode(body)
	s := config.Default()
	s.Disabled = true
	e := New(s, nil, nil)
	_, rep := e.Transform(context.Background(), req)
	if !rep.Skipped {
		t.Fatalf("disabled engine should skip")
	}
}

// TestSelfCachingClientSkipsCacheStage: when the client already set cache_control,
// the cache stage stands down (ClientCacheFloor >= 0).
func TestSelfCachingClientSkipsCacheStage(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]},
		{"role":"assistant","content":[{"type":"text","text":"hello"}]}
	]}`)
	req, _ := canon.Decode(body)
	e := New(config.Default(), nil, nil)
	_, rep := e.Transform(context.Background(), req)
	if rep.CacheInjected {
		t.Fatalf("cache stage should stand down for a self-caching client")
	}
}
