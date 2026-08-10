package offload

import (
	"math"
	"testing"

	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/internal/cheapmodel"
)

// The gate must suppress when the request is cache-aware and the output is small — the
// exact case that made extract_llm ~8x underwater on Terminal-Bench. A 400-token output
// on a caching backend is worth ~400 x $0.30/MTok x (1+reuses); against a ~$0.012 call
// that cannot pay, and the reason must say so.
func TestGateSuppressesSmallOutputWhenCacheAware(t *testing.T) {
	val := savedTokenValue(&components.Ctx{CacheAware: true})
	if val.cached != true {
		t.Fatal("cache-aware ctx must price saved tokens at the cache-read rate")
	}
	d := evaluateGate(400, defaultCompressionRatio, val, priorCallCost, false, 5)
	if d.allow {
		t.Fatalf("small cached output must be suppressed: saving=$%.5f cost=$%.5f", d.expSaving, d.expCost)
	}
	if d.reason == "" {
		t.Fatal("every suppression must carry a reason (the operator's first question)")
	}
	if d.expSaving >= d.expCost {
		t.Fatalf("suppression must be justified by the numbers: saving=%v cost=%v", d.expSaving, d.expCost)
	}
}

// On a NON-caching backend the same reduction is billed at the full fresh-input rate — 10x
// more valuable — so the same output that loses under caching can win here. This is the
// asymmetry the gate exists to exploit, so assert it on ONE fixture, not two.
func TestGatePermitsOnNonCachingBackend(t *testing.T) {
	size := 12000
	cached := evaluateGate(size, defaultCompressionRatio,
		savedTokenValue(&components.Ctx{CacheAware: true}), priorCallCost, false, 5)
	fresh := evaluateGate(size, defaultCompressionRatio,
		savedTokenValue(&components.Ctx{CacheAware: false}), priorCallCost, false, 5)

	if !fresh.allow {
		t.Fatalf("non-caching backend must permit a %d-token output: saving=$%.5f cost=$%.5f",
			size, fresh.expSaving, fresh.expCost)
	}
	// The 10x rate difference must show up in the valuation, not just the verdict.
	if ratio := fresh.expSaving / cached.expSaving; math.Abs(ratio-10) > 0.01 {
		t.Fatalf("fresh tokens must be worth 10x cached ones, got %.3fx", ratio)
	}
}

// High-reuse (recurring) content is permitted even under caching, because the saving is
// collected on every turn the frozen compaction is replayed. Recurrence was measured at
// 82/103 across sessions, so this is the common case, not an edge case.
func TestGatePermitsHighReuseContent(t *testing.T) {
	val := savedTokenValue(&components.Ctx{CacheAware: true})
	// 14000 tokens: above the ~12.7k cached-recurring break-even, below the ~17.8k
	// cached-once one. This size is the gate's whole thesis in one fixture — recurrence is
	// what tips an otherwise-losing call into profit, so the SAME size goes both ways.
	size := 14000
	once := evaluateGate(size, defaultCompressionRatio, val, priorCallCost, false, 5)
	recur := evaluateGate(size, defaultCompressionRatio, val, priorCallCost, true, 5)

	if recur.expSaving <= once.expSaving {
		t.Fatalf("recurring content must be valued higher: recur=%v once=%v", recur.expSaving, once.expSaving)
	}
	if !recur.allow {
		t.Fatalf("recurring %d-token content should be permitted: saving=$%.5f cost=$%.5f",
			size, recur.expSaving, recur.expCost)
	}
	if once.allow {
		t.Fatalf("at %d tokens, NON-recurring cached content should still be suppressed "+
			"(saving=$%.5f cost=$%.5f) — recurrence is what must tip the decision",
			size, once.expSaving, once.expCost)
	}
}

// The break-even output size is the headline economic fact of issue #28, so pin it: under
// caching an extraction call must remove ~10x more tokens than on a non-caching backend.
// If these numbers drift, the component's viability has changed and the docs' verdict
// needs revisiting — which is exactly what this test exists to catch.
func TestBreakEvenSizesMatchTheDocumentedVerdict(t *testing.T) {
	breakEven := func(cacheAware, recurring bool) int {
		val := savedTokenValue(&components.Ctx{CacheAware: cacheAware})
		for size := 200; size <= 400_000; size += 100 {
			if evaluateGate(size, defaultCompressionRatio, val, priorCallCost, recurring, 5).allow {
				return size
			}
		}
		return -1
	}
	cachedRecur := breakEven(true, true)
	freshRecur := breakEven(false, true)
	if cachedRecur < 11_000 || cachedRecur > 14_000 {
		t.Errorf("cached+recurring break-even = %d tokens, expected ~12,700 "+
			"(docs/components/extract_llm.md states this figure)", cachedRecur)
	}
	if freshRecur < 1_000 || freshRecur > 1_600 {
		t.Errorf("fresh+recurring break-even = %d tokens, expected ~1,270", freshRecur)
	}
	// The 10x rate haircut must show up as a ~10x break-even gap.
	if ratio := float64(cachedRecur) / float64(freshRecur); ratio < 8 || ratio > 12 {
		t.Errorf("cached/fresh break-even ratio = %.1fx, expected ~10x", ratio)
	}
}

// The cost model must be real arithmetic over model pricing, not a hard-coded constant —
// "~$0.012/call" was one workload's average, and the gate has to track the actual model.
func TestCostModelMatchesKnownTokensTimesKnownPrice(t *testing.T) {
	p := cheapmodel.Pricing{InputPerMTok: 1.00, OutputPerMTok: 5.00,
		CacheWritePerMTok: 1.25, CacheReadPerMTok: 0.10}
	// 1M input @ $1 + 1M output @ $5 + 1M write @ $1.25 + 1M read @ $0.10 = $7.35
	if got := p.Cost(1_000_000, 1_000_000, 1_000_000, 1_000_000); math.Abs(got-7.35) > 1e-9 {
		t.Fatalf("Cost = %v, want 7.35", got)
	}
	// A realistic single extraction call: 3000 fresh in, 200 out on haiku rates.
	// 3000/1e6*1 + 200/1e6*5 = 0.003 + 0.001 = 0.004
	if got := p.Cost(3000, 200, 0, 0); math.Abs(got-0.004) > 1e-9 {
		t.Fatalf("Cost = %v, want 0.004", got)
	}
	// Env override must be honored so an operator prices their own deployment.
	t.Setenv("CHEAP_MODEL_PRICE_IN", "2.50")
	if got := cheapmodel.PricingFromEnv().InputPerMTok; got != 2.50 {
		t.Fatalf("PricingFromEnv InputPerMTok = %v, want 2.50", got)
	}
}

// callCost must fall back to the prior only until a real observation exists; it must never
// return zero, which would make the gate permit everything.
func TestCallCostNeverZero(t *testing.T) {
	if c := callCost(cheapmodel.HaikuPricing()); c <= 0 {
		t.Fatalf("callCost must be positive, got %v", c)
	}
}

// The trigger must NOT fire on every step of a merely-growing context — firing every step
// is what produced 271 calls. Pressure gates it, and a near-static context is declined.
func TestTriggerDoesNotFireEveryStepOnGrowingContext(t *testing.T) {
	window := 1_000_000
	fired := 0
	prev := 0
	// Simulate 40 turns growing by 5k tokens each: reaches only ~20% of a 1M window.
	for turn := 1; turn <= 40; turn++ {
		cur := turn * 5000
		p := contextPressure(cur, window)
		g := growthRate(cur, prev)
		if ok, _ := shouldFire(p, g, false); ok {
			fired++
		}
		prev = cur
	}
	if fired == 40 {
		t.Fatal("trigger fired on EVERY step of a growing context — the #28 waste case")
	}
	if fired > 12 {
		t.Fatalf("trigger fired on %d/40 low-pressure steps; expected few", fired)
	}
	// It must still fire when pressure is genuinely high — a gate that never fires is
	// just as broken as one that always does.
	if ok, reason := shouldFire(0.75, 0.05, false); !ok {
		t.Fatalf("high pressure must fire, got reason %q", reason)
	}
}

// An explicitly-configured min_tokens keeps governing: existing configs must not change
// behavior silently under them.
func TestExplicitMinTokensStillGoverns(t *testing.T) {
	ok, reason := shouldFire(0.01, 0, true) // pressure so low the derived trigger declines
	if !ok {
		t.Fatal("an explicit min_tokens/trigger must still fire (backward compatibility)")
	}
	if reason == "" {
		t.Fatal("reason must be recorded even on the explicit path")
	}
}

// The derived per-output floor must fall as the window fills — no per-workload tuning.
func TestPressureFloorFallsAsContextFills(t *testing.T) {
	window := 1_000_000
	low := pressureFloor(window, 0.10)
	mid := pressureFloor(window, 0.40)
	high := pressureFloor(window, 0.70)
	full := pressureFloor(window, 0.90)
	if !(low > mid && mid > high && high > full) {
		t.Fatalf("floor must decrease monotonically with pressure: %d %d %d %d", low, mid, high, full)
	}
	if full <= 0 {
		t.Fatal("a nearly-full window must still have a positive floor")
	}
	// An unknown window must yield 0 so the caller keeps its absolute default (fail open).
	if pressureFloor(0, 0.9) != 0 {
		t.Fatal("unknown window must return 0 (fall back to absolute default)")
	}
}

// The observed compression ratio must displace the default only once there is enough
// evidence, and a run of misses must drive it toward zero (shutting the gate).
func TestRatioTrackerLearnsFromObservations(t *testing.T) {
	var r ratioTracker
	if r.ratio() != defaultCompressionRatio {
		t.Fatal("with no observations the conservative default must apply")
	}
	r.observe(100, 1000) // below the sample threshold
	if r.ratio() != defaultCompressionRatio {
		t.Fatal("a tiny sample must not displace the default")
	}
	r.observe(0, 20000) // plenty of evidence that this workload does not compress
	if got := r.ratio(); got >= 0.10 {
		t.Fatalf("repeated misses must drive the ratio down, got %v", got)
	}
}
