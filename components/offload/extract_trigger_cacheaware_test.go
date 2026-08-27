package offload

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/store"
)

// An operator's REQUEST-level trigger must be honored on a prompt-caching backend.
//
// The guard on this check used to read `!c.CacheAware && !fires && !huge`, so with CACHE_MODE=on
// (or any caching provider) a configured trigger.min_request_tokens had no effect at all. That is
// not a cache-awareness carve-out protecting a heuristic: Trigger's zero value fires always (see
// components/trigger.go), so `!fires` is reachable ONLY when the operator configured a request
// threshold that was not met. The condition could therefore only ever void explicit
// configuration, silently.
//
// Both arms run the SAME config against the SAME request and differ ONLY in CacheAware, which is
// what makes this an assertion about the carve-out rather than about triggers in general.
//
// The COLD SWEEP is the part of the old carve-out that carried real weight and is deliberately
// still exempt: on a cold turn the whole transcript re-bills whatever the request's size, so a
// request-size threshold answers the wrong question there and the sweep brings its own floor.
// Both arms here run warm (ColdCache unset), and the exemption is pinned separately by
// TestHousellmColdSweepActuallyFires in components/all — which is what caught the first version
// of this fix removing it.
//
// economic_gate: false is required setup, not a convenience: it also sets
// allow_on_caching_backend, without which the component disables itself entirely on the
// CacheAware arm and the arm would pass for a completely unrelated reason.
func TestExplicitRequestTriggerIsHonoredOnCachingBackends(t *testing.T) {
	// One output far above any floor, in a request far below the configured request threshold.
	big := strings.Repeat("2024-01-01 GET /users/42 200 12ms src/api/users.py\n", 400)
	newReq := func() *bschemas.BifrostChatRequest {
		return &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
			userMsg("Find the auth timeout in src/api/users.py and fix it."),
			assistantMsg("Reading the log."),
			toolResultMsg(big),
			userMsg("keep going"),
		}}
	}

	for _, cacheAware := range []bool{false, true} {
		name := "cache_aware=false"
		if cacheAware {
			name = "cache_aware=true"
		}
		t.Run(name, func(t *testing.T) {
			// A threshold no request in this test can meet, so `fires` is false in BOTH arms.
			comp, err := newExtractLLM([]byte(
				"economic_gate: false\ntrigger:\n  min_request_tokens: 10000000\n"))
			if err != nil {
				t.Fatalf("config: %v", err)
			}
			e := comp.(*ExtractLLM)
			model := &silentModel{}
			req, rep := newReq(), components.Report{}
			ctx := &components.Ctx{
				Session: "s", Ctx: context.Background(),
				Store: store.NewMemory(store.Options{}), CtxWindow: 1_000_000,
				CacheAware: cacheAware, MaxCachedIdx: -1,
				Model: components.ModelSpec{Static: model, Incoming: model},
			}
			if _, err := e.Offload(req, &rep, ctx); err != nil {
				t.Fatalf("offload: %v", err)
			}
			// Precondition: the candidate must have REACHED this gate. Filtered earlier by the
			// floor or the cached-prefix tail gate, the assertion below would be vacuous.
			if rep.Gates["below_output_floor"] > 0 || rep.Gates["cached_prefix"] > 0 {
				t.Fatalf("candidate never reached the request trigger; gates=%v", rep.Gates)
			}
			if rep.Gates["request_trigger_not_fired"] == 0 {
				t.Errorf("configured min_request_tokens was ignored: expected the "+
					"request_trigger_not_fired gate, got gates=%v", rep.Gates)
			}
			if n := atomic.LoadInt64(&model.calls); n != 0 {
				t.Errorf("model called %d time(s) despite an unmet configured request trigger", n)
			}
		})
	}
}
