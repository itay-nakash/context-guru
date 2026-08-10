package all_test

import (
	"context"
	"strings"
	"testing"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
)

// TestExtractResultCacheHitsAcrossSessions is the headline acceptance criterion for issue
// #28 part C: identical content in a DIFFERENT session must reuse the prior extraction
// instead of paying for it again. Before the global re-key the result cache carried a
// session prefix, so the second session re-derived a result the system already had —
// measured wasteful on 82 of 103 unique contents.
func TestExtractResultCacheHitsAcrossSessions(t *testing.T) {
	// economic_gate: false isolates the CACHE behavior under test from the gate's
	// (separately tested) spending decision.
	off := newComp(t, "extract_llm", "strategy: code\nmin_tokens: 1\neconomic_gate: false\nmodel:\n  source: config\n")
	st := store.NewMemory(store.Options{}) // one store, as a real proxy has
	filter := "data = json.decode(INPUT)\nOUTPUT = json.encode([r for r in data if \"keep\" in r[\"name\"]])\n"
	cm := &countingModel{resp: filter}
	pad := strings.Repeat("padding ", 40)
	body := `[{"id":1,"name":"keep this ` + pad + `"},{"id":2,"name":"drop this ` + pad + `"}]`

	runIn := func(session string) *bschemas.BifrostChatRequest {
		req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
			userMsg("find the keep records"), toolMsg(body),
		}}
		c := &components.Ctx{Ctx: context.Background(), Session: session, Store: st,
			Model: components.ModelSpec{Static: cm}}
		var rep components.Report
		if _, err := off.Offload(req, &rep, c); err != nil {
			t.Fatal(err)
		}
		return req
	}

	req1 := runIn("session-A")
	if cm.calls != 1 {
		t.Fatalf("first session must call the model once, calls=%d", cm.calls)
	}
	out1 := schema.MessageText(req1.Input[1])
	if strings.Contains(out1, "drop this") {
		t.Fatal("first session should have reduced the output")
	}

	// A DIFFERENT session, same content. This is the case the session-scoped key missed.
	req2 := runIn("session-B-completely-different")
	if cm.calls != 1 {
		t.Fatalf("a different session must REUSE the cached extraction (no new model call), calls=%d", cm.calls)
	}
	out2 := schema.MessageText(req2.Input[1])
	if strings.Contains(out2, "drop this") {
		t.Fatal("cross-session reuse must still drop the non-keep record")
	}

	// A third, also free.
	runIn("session-C")
	if cm.calls != 1 {
		t.Fatalf("every later session must reuse, calls=%d", cm.calls)
	}
}

// The gate must actually suppress in a real pipeline run on a cache-aware request with a
// small output — the Terminal-Bench losing case, end to end rather than in unit isolation.
func TestExtractGateSuppressesInPipelineWhenCacheAware(t *testing.T) {
	off := newComp(t, "extract_llm", "strategy: code\nmodel:\n  source: config\n")
	st := store.NewMemory(store.Options{})
	filter := "data = json.decode(INPUT)\nOUTPUT = json.encode([r for r in data if \"keep\" in r[\"name\"]])\n"
	cm := &countingModel{resp: filter}
	pad := strings.Repeat("padding ", 40) // ~400 tokens: far below the ~12.7k cached break-even
	body := `[{"id":1,"name":"keep this ` + pad + `"},{"id":2,"name":"drop this ` + pad + `"}]`

	req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
		userMsg("find the keep records"), toolMsg(body),
	}}
	c := &components.Ctx{Ctx: context.Background(), Session: "s1", Store: st,
		Model: components.ModelSpec{Static: cm}, CacheAware: true, MaxCachedIdx: -1,
		CtxWindow: 1_000_000}
	var rep components.Report
	if _, err := off.Offload(req, &rep, c); err != nil {
		t.Fatal(err)
	}
	if cm.calls != 0 {
		t.Fatalf("a small output on a cache-aware request must not be worth a call, calls=%d", cm.calls)
	}
	if schema.MessageText(req.Input[1]) != body {
		t.Fatal("a suppressed candidate must be left verbatim (fail open)")
	}
}

// Backward compatibility: an existing config that pins min_tokens must keep working
// unchanged — the smarter trigger is the DEFAULT only when nothing was configured.
func TestExplicitMinTokensConfigStillReduces(t *testing.T) {
	// A pinned min_tokens plus the pre-#28 gate setting reproduces old behavior exactly.
	off := newComp(t, "extract_llm", "strategy: code\nmin_tokens: 1\neconomic_gate: false\nmodel:\n  source: config\n")
	st := store.NewMemory(store.Options{})
	filter := "data = json.decode(INPUT)\nOUTPUT = json.encode([r for r in data if \"keep\" in r[\"name\"]])\n"
	cm := &countingModel{resp: filter}
	pad := strings.Repeat("padding ", 40)
	body := `[{"id":1,"name":"keep this ` + pad + `"},{"id":2,"name":"drop this ` + pad + `"}]`
	req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
		userMsg("find the keep records"), toolMsg(body),
	}}
	// A tiny context window would make the derived trigger decline; an explicit
	// min_tokens must override that.
	c := &components.Ctx{Ctx: context.Background(), Session: "s1", Store: st,
		Model: components.ModelSpec{Static: cm}, CtxWindow: 1_000_000}
	var rep components.Report
	keys, err := off.Offload(req, &rep, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("explicit min_tokens must still reduce (skipped=%v calls=%d)", rep.Skipped, cm.calls)
	}
}

// Cross-session reuse must be gated on RECOVERABILITY. In the default rewrite mode the
// containment proof is deliberately skipped, so a cached result can be a lossy rewrite
// steered by ANOTHER session's goal. That is tolerable only while `expand` can recover the
// original — with marker_mode: off there is no way back, so a second session must NOT
// inherit the first session's lossy rewrite.
func TestNoCrossSessionReuseOfIrreversibleRewrite(t *testing.T) {
	off := newComp(t, "extract_llm",
		"strategy: code\nmin_tokens: 1\neconomic_gate: false\nmarker_mode: off\nmodel:\n  source: config\n")
	st := store.NewMemory(store.Options{})
	filter := "data = json.decode(INPUT)\nOUTPUT = json.encode([r for r in data if \"keep\" in r[\"name\"]])\n"
	cm := &countingModel{resp: filter}
	pad := strings.Repeat("padding ", 40)
	body := `[{"id":1,"name":"keep this ` + pad + `"},{"id":2,"name":"drop this ` + pad + `"}]`

	runIn := func(session string) {
		req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
			userMsg("find the keep records"), toolMsg(body),
		}}
		c := &components.Ctx{Ctx: context.Background(), Session: session, Store: st,
			Model: components.ModelSpec{Static: cm}}
		var rep components.Report
		if _, err := off.Offload(req, &rep, c); err != nil {
			t.Fatal(err)
		}
	}

	runIn("session-A")
	first := cm.calls
	if first == 0 {
		t.Fatal("first session should have called the model")
	}
	runIn("session-B")
	if cm.calls == first {
		t.Fatal("an irreversible lossy rewrite must NOT be reused across sessions " +
			"(no expand path to recover the original)")
	}
}

// The same content in the SAME session must still be reused even when irreversible — that
// costs nothing extra and keeps the request prefix byte-stable.
func TestSameSessionReuseStillWorksWhenIrreversible(t *testing.T) {
	off := newComp(t, "extract_llm",
		"strategy: code\nmin_tokens: 1\neconomic_gate: false\nmarker_mode: off\nmodel:\n  source: config\n")
	st := store.NewMemory(store.Options{})
	filter := "data = json.decode(INPUT)\nOUTPUT = json.encode([r for r in data if \"keep\" in r[\"name\"]])\n"
	cm := &countingModel{resp: filter}
	pad := strings.Repeat("padding ", 40)
	body := `[{"id":1,"name":"keep this ` + pad + `"},{"id":2,"name":"drop this ` + pad + `"}]`

	run := func() {
		req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
			userMsg("find the keep records"), toolMsg(body),
		}}
		c := &components.Ctx{Ctx: context.Background(), Session: "one-session", Store: st,
			Model: components.ModelSpec{Static: cm}}
		var rep components.Report
		if _, err := off.Offload(req, &rep, c); err != nil {
			t.Fatal(err)
		}
	}
	run()
	after := cm.calls
	run()
	if cm.calls != after {
		t.Fatalf("same-session reuse must avoid a second call, calls went %d -> %d", after, cm.calls)
	}
}

// SHIPPING DECISION, end to end: on a cache-aware request the component must decline by
// default however attractive the economics look, and `allow_on_caching_backend: true` must
// hand control back to the gate.
func TestCachingBackendDisabledByDefaultInPipeline(t *testing.T) {
	filter := "data = json.decode(INPUT)\nOUTPUT = json.encode([r for r in data if \"keep\" in r[\"name\"]])\n"
	// A big output, well above any break-even, so only the default can be declining it.
	pad := strings.Repeat("padding word here ", 4000)
	body := `[{"id":1,"name":"keep this ` + pad + `"},{"id":2,"name":"drop this ` + pad + `"}]`

	runWith := func(cfg string) int {
		off := newComp(t, "extract_llm", cfg)
		cm := &countingModel{resp: filter}
		req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
			userMsg("find the keep records"), toolMsg(body),
		}}
		c := &components.Ctx{Ctx: context.Background(), Session: "s", Store: store.NewMemory(store.Options{}),
			Model: components.ModelSpec{Static: cm}, CacheAware: true, MaxCachedIdx: -1,
			CtxWindow: 200_000}
		var rep components.Report
		if _, err := off.Offload(req, &rep, c); err != nil {
			t.Fatal(err)
		}
		return cm.calls
	}

	if n := runWith("strategy: code\nmin_tokens: 1\nmodel:\n  source: config\n"); n != 0 {
		t.Errorf("caching backend must be off by default, got %d calls", n)
	}
	if n := runWith("strategy: code\nmin_tokens: 1\nallow_on_caching_backend: true\nmodel:\n  source: config\n"); n == 0 {
		t.Error("allow_on_caching_backend: true must permit a clearly-economic call")
	}
}
