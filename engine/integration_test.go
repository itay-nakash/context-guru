package engine_test

// This file imports ONLY the public packages, exactly as an external consumer (e.g.
// the Kagenti AuthBridge plugin in kagenti-extensions) would. It stands in for that
// plugin: take the raw request body the host already holds, reduce it, forward the
// rendered bytes, and recover an omitted block via Expand. If this compiles and
// passes, the public integration surface is intact.

import (
	"context"
	"strings"
	"testing"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/config"
	"github.com/kagenti/lab-context-engineering/engine"
	"github.com/kagenti/lab-context-engineering/surfaces"
)

func TestPublicAPI_AuthBridgeStyleWrap(t *testing.T) {
	// What a host plugin does, end to end.
	s := config.Default()
	s.ProtectRecent = 1
	eng := engine.New(s, nil, nil)
	surface := surfaces.Anthropic{} // pick by the request's wire format

	rawBody := []byte(`{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"read","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"` +
		strings.Repeat("a.go a fairly long line of read content goes here\\n", 12) + `"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"u2","name":"edit","input":{"file_path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u2","content":"success: edited"}]},
		{"role":"user","content":[{"type":"text","text":"thanks"}]}
	]}`)

	req, token, err := surface.ToInternal(rawBody)
	if err != nil {
		t.Fatalf("ToInternal: %v", err)
	}
	reduced, _ := eng.Transform(context.Background(), req)
	outBody, err := surface.Render(reduced, token)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(outBody) >= len(rawBody) {
		t.Fatalf("expected reduced body to be smaller: %d vs %d", len(outBody), len(rawBody))
	}

	// The host can recover any omitted block from the marker (e.g. to serve an
	// expand tool call, or to rehydrate before a summarization turn).
	ids := engine.FindMarkers(string(outBody))
	if len(ids) == 0 {
		t.Fatalf("expected a recoverable marker in the reduced body")
	}
	if _, ok := eng.Expand(ids[0]); !ok {
		t.Fatalf("Expand failed for %s", ids[0])
	}

	// A re-decode of the forwarded bytes is still a valid canonical request.
	if _, err := canon.Decode(outBody); err != nil {
		t.Fatalf("forwarded body is not valid JSON: %v", err)
	}
}
