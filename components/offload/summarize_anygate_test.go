package offload

import (
	"sync/atomic"
	"testing"
	"time"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
)

// `cache_state: any` IS THE OTHER SHIPPED CONFIGURATION, and this pins the one sentence a deployment
// choosing it relies on: it fires at the fill threshold WHILE THE PROMPT CACHE IS LIVE, on the same
// turn the default declines.
//
// Why this needs a test when the opt-out is already covered: every existing subtest exercises `any`
// against phase UNKNOWN (sumCtx's "unknown"), which is the one phase where CacheAllows has an early
// return. Warm is the phase an operator picking `any` is actually buying — they have decided that
// shrinking every later turn is worth invalidating a prefix that is still alive — and nothing
// asserted that warm and unknown agree. They do, because `p` is not read after that early return;
// but "they agree because of the control flow" is exactly the kind of claim that stops being true
// under an edit, and the scoping of that early return has already drifted once in this file.
//
// THE PAIR IS THE POINT. A test that only showed `any` firing would still pass if the cache gate
// were deleted outright, and one that only showed the default declining would still pass if `any`
// were broken into a no-op. Both subtests run the SAME transcript against the SAME warm Ctx and must
// disagree, which is the only shape that pins the difference to the single key that differs.
func TestCacheStateAnyFiresOnALiveCacheAndTheDefaultDoesNot(t *testing.T) {
	msgs := sumTranscript(6)
	const window = 1_000_000

	assertBilledFigureOpenedTheFill(t, msgs, window)

	for _, tc := range []struct {
		name       string
		cacheState string
		wantFires  bool
		wantGate   string
	}{
		{"cache_state: any fires on a live cache", "any", true, ""},
		// No cache_state written at all, so this exercises the SHIPPED default through the
		// registered constructor rather than a string a test picked.
		{"the shipped default declines the same turn", "", false, "cache_state_declined_warm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCacheGatedSummarizeWithFrac(t, anyGateTrigger(tc.cacheState))
			// min_request_frac is deliberately absent above: the 0.9 default is part of what is
			// under test, because it is what the two configurations SHARE.
			if s.trigger.MinRequestFrac != summarizeDefaultRequestFrac {
				t.Fatalf("precondition: min_request_frac is %v, want the shipped %v — the two "+
					"configurations must differ only in cache_state",
					s.trigger.MinRequestFrac, summarizeDefaultRequestFrac)
			}
			model := &countingModel{out: "SUMMARY: explored the handler, 3 tests fail."}
			s.modelClient = model

			ctx := sumCtx(store.NewMemory(store.Options{MaxEntries: 400}), "warm", window, true, 950_000)
			ctx.Session = "warm-" + tc.name // see anyGateTrigger's note on the single-flight registry
			// Assert the fixture really is warm. If sumCtx's TTL arithmetic or DefaultPreExpiry
			// moved so that 270s of remaining life read as pre-expiry, the default would fire too
			// and this test would report an agreement it never demonstrated.
			if phase := ctx.CachePhase(components.DefaultPreExpiry); phase != components.CachePhaseWarm {
				t.Fatalf("precondition: fixture phase is %s, want Warm", phase)
			}

			in := &bschemas.BifrostChatRequest{Input: append([]bschemas.ChatMessage(nil), msgs...)}
			var rep components.Report
			if _, err := s.Offload(in, &rep, ctx); err != nil {
				t.Fatalf("Offload must fail open: %v", err)
			}
			// Commissioned, not called inline, so drain before counting: a declined turn started
			// nothing and this returns at once, a fired turn started exactly one call.
			WaitForSummaryForTest(ctx.Session, 5*time.Second)
			if fired := atomic.LoadInt64(&model.calls) == 1; fired != tc.wantFires {
				t.Errorf("fired=%v want %v (gates: %v). The two configurations differ in one key, so "+
					"one of them has stopped doing what its documentation promises",
					fired, tc.wantFires, rep.Gates)
			}
			if tc.wantGate != "" && rep.Gates[tc.wantGate] == 0 {
				t.Errorf("gate %q not filed; gates: %v", tc.wantGate, rep.Gates)
			}
		})
	}
}

// AND `any` MUST SURVIVE THE UNKNOWN-PHASE GUARD OVER A LIVE PREFIX. This is the regression the test
// above cannot catch, and the reason CacheAllows' guard names `any` explicitly.
//
// That guard declines an UNKNOWN phase whenever a cached prefix demonstrably exists (CacheAware with
// MaxCachedIdx >= 0) — the state a review found the gate opening over a live 8-message prefix on 13
// of 26 turns. It is scoped to exclude `""` and `any`, because those callers never asked for a cache
// constraint and declining there would make the component DEAD on the very deployments the opt-out
// exists for: a raw-API harness with nothing to cap its context is precisely one that can present a
// cached prefix and no readable TTL.
//
// Drop `t.CacheState != CacheStateAny` from that condition and configuration B silently stops firing
// on those deployments, with no other test in the repo failing.
func TestCacheStateAnyStillFiresWhenAnUnknownPhaseHidesALivePrefix(t *testing.T) {
	msgs := sumTranscript(6)
	const window = 1_000_000

	// Not inherited from the test above even though both use sumTranscript(6): this test's own
	// declining row asserts a SPECIFIC gate, which is only meaningful if the fill conjunct is open.
	assertBilledFigureOpenedTheFill(t, msgs, window)

	for _, tc := range []struct {
		name       string
		cacheState string
		wantFires  bool
		wantGate   string
	}{
		{"any fires despite the live-prefix guard", "any", true, ""},
		{"the shipped default is declined by it", "", false, "cache_state_declined_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCacheGatedSummarizeWithFrac(t, anyGateTrigger(tc.cacheState))
			model := &countingModel{out: "SUMMARY: explored the handler, 3 tests fail."}
			s.modelClient = model

			// An unknown phase — no readable TTL — over a prefix that provably exists.
			ctx := sumCtx(store.NewMemory(store.Options{MaxEntries: 400}), "unknown", window, true, 950_000)
			ctx.Session = "unknown-" + tc.name // see anyGateTrigger's note on the single-flight registry
			ctx.CacheAware, ctx.MaxCachedIdx = true, 8
			if phase := ctx.CachePhase(components.DefaultPreExpiry); phase != components.CachePhaseUnknown {
				t.Fatalf("precondition: fixture phase is %s, want Unknown", phase)
			}

			in := &bschemas.BifrostChatRequest{Input: append([]bschemas.ChatMessage(nil), msgs...)}
			var rep components.Report
			if _, err := s.Offload(in, &rep, ctx); err != nil {
				t.Fatalf("Offload must fail open: %v", err)
			}
			WaitForSummaryForTest(ctx.Session, 5*time.Second)
			if fired := atomic.LoadInt64(&model.calls) == 1; fired != tc.wantFires {
				t.Errorf("fired=%v want %v (gates: %v). `any` must impose no cache constraint even "+
					"where the phase is unreadable and a prefix is known to be live",
					fired, tc.wantFires, rep.Gates)
			}
			// NAMING THE GATE IS WHAT MAKES THE DECLINING ROW MEAN ANYTHING. Asserting only that
			// the default did not fire would pass on any decline at all — window_not_exact,
			// below_request_trigger, a gate nobody has written yet — while this test's whole
			// purpose is that the LIVE-PREFIX GUARD is what declined it. A negative half that
			// cannot fail for the thing it names reads as coverage without being any.
			if tc.wantGate != "" && rep.Gates[tc.wantGate] == 0 {
				t.Errorf("gate %q not filed; gates: %v. The default declined for some other "+
					"reason, so this subtest is not exercising the unknown-phase guard",
					tc.wantGate, rep.Gates)
			}
		})
	}
}

// assertBilledFigureOpenedTheFill pins that the fill conjunct is open because of the PROVIDER's
// figure rather than our own count of the transcript.
//
// Both tests need it, and for the same reason: the fill gate is the half the two configurations
// SHARE, so if it were closed — or if it were opened by MessagesTokens, which runs a median 3.38x
// below billed input — then neither a fired turn nor a named gate would be attributable to
// cache_state, and both tests would be measuring the wrong conjunct.
func assertBilledFigureOpenedTheFill(t *testing.T, msgs []bschemas.ChatMessage, window int) {
	t.Helper()
	tokens := schema.MessagesTokens(&bschemas.BifrostChatRequest{Input: msgs})
	if float64(tokens) >= summarizeDefaultRequestFrac*float64(window) {
		t.Fatalf("fixture counts %d message-text tokens, which already clears the %.0f floor; these "+
			"tests can no longer show that the billed figure is what opened the fill gate",
			tokens, summarizeDefaultRequestFrac*float64(window))
	}
}

// anyGateTrigger writes the trigger block for one of the two shipped configurations, omitting
// cache_state entirely for the default so the constructor supplies it. min_request_frac is left out
// of both, because the two configurations must differ in exactly one key.
//
// EACH SUBTEST ALSO GETS ITS OWN SESSION ID, which sumCtx does not give it. The async summarizer
// keys in-flight calls on the session in a package-global single-flight registry, so subtests
// sharing sumCtx's "s" are only safe while they run sequentially — a later t.Parallel() here would
// have one subtest's commissioned call satisfy another's wait, and the firing counts would silently
// stop belonging to the turns that produced them.
func anyGateTrigger(cacheState string) string {
	trigger := "trigger:\n  min_messages: 2\n"
	if cacheState != "" {
		trigger += "  cache_state: " + cacheState + "\n"
	}
	return trigger
}
