package offload

import (
	"sync"

	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/internal/cheapmodel"
)

// The economic gate. extract_llm is the only component that SPENDS money to SAVE money,
// so it is the only one that can be net-negative — and on Terminal-Bench it was, by ~8x:
// 271 calls / $3.26 / ~1,592s of latency against ~197,548 unique tokens saved.
//
// The arithmetic behind that loss is the whole point. A saved token is only worth the
// rate it WOULD have been billed at. On a prompt-caching backend the request is ~99.95%
// cached, so a token removed from a cached region is worth the cache-READ rate
// ($0.20/MTok on the agent model), not the fresh-input rate ($2/MTok) — a 10x haircut.
// An extraction call costing ~$0.012 must therefore remove ~60,000 cache-read tokens to
// break even, versus ~6,000 fresh ones. Most tool outputs are nowhere near that, which is
// why "compress everything" loses on a caching backend and wins on a non-caching one.
//
// The component already contained this insight as a comment on skip_file_reads
// (extract_llm.go, on AUTO mode). This turns that reasoning into an actual gate that
// applies to EVERY candidate, not just line-numbered file reads.

// --- Issue #28 part B: reusing the AGENT's cached prefix — PROTOTYPED AND REJECTED ---
//
// The proposal was to append the extraction instruction as a final user message after the
// agent's existing stable prefix, so extraction reads an already-cached context instead of
// paying fresh input on its own prompt. Prototyped against the live gateway
// (aws/claude-sonnet-5, a ~103k-token cached prefix). It works mechanically — the
// extraction turn read the full prefix from cache, no cache-write, no prefix invalidation:
//
//	agent turn 1 (writes cache)        in=1   write=103,019  read=0
//	agent turn 2 (reads cache)         in=9   write=0        read=103,019
//	extraction reusing agent prefix    in=25  write=0        read=103,019
//
// But cache-read is cheap, not free, and the bill scales with the WHOLE context:
//
//	dedicated haiku call (~3k prompt)      $0.00400
//	reuse @   103,019-token prefix         $0.03398    8.5x
//	reuse @   500,000-token prefix         $0.15307   38.3x
//	reuse @ 1,700,000-token prefix         $0.51307  128.3x
//
// At the ~1.7M contexts this workload actually reaches, one extraction costs ~128x a
// dedicated cheap-model call — and the component issues up to llmConcurrency=4 per turn,
// so ~$2.04/turn against ~$0.016. That is the opposite of the direction this issue exists
// to push. Paying 1.7M cache-read tokens to answer a question about ONE tool output is
// structurally wrong regardless of the rate.
//
// DECISION: NOT IMPLEMENTED. Three independent reasons, any one sufficient:
//  1. Cost: 8.5x-128x a dedicated call, worsening as context grows (measured above).
//  2. Cache-write risk on the agent's own prefix: a write is 11.5x a read, and putting
//     extraction traffic on the agent's cache key risks exactly the mistake this whole
//     workstream is about. The prototype did not trigger one, but it only takes one
//     divergent breakpoint or an eviction between turns.
//  3. Coupling: it ties the compaction model to the agent model, so extraction quality,
//     latency, and spend all move whenever someone changes the agent's model.
//
// The dedicated cheap model stays. Re-open only if a provider prices in-context follow-up
// questions at a flat rate rather than per cache-read token.

// tokenValue is the dollars-per-token a SAVED token is worth, and the reason the gate
// exists. Both rates are per single token (not per million).
type tokenValue struct {
	perToken float64
	cached   bool // true when priced at the cache-read rate
}

// Default agent-model rates (claude-sonnet-5 class, $3/$15 per MTok, cache read 0.1x).
// The gate is a comparison, so what matters is the RATIO between a saved token's value
// and the extraction call's cost — both scale together if an operator's contract differs.
const (
	agentFreshPerMTok     = 3.00
	agentCacheReadPerMTok = 0.30 // 0.1x fresh, the standard Anthropic cache-read multiplier
)

// savedTokenValue prices one saved token for THIS request. When the request goes to a
// prompt-caching backend, content the agent re-sends every turn is already in the cached
// prefix, so removing it saves the cache-read rate — the 10x haircut that sinks the
// component's economics.
func savedTokenValue(c *components.Ctx) tokenValue {
	if c != nil && c.CacheAware {
		return tokenValue{perToken: agentCacheReadPerMTok / 1_000_000, cached: true}
	}
	return tokenValue{perToken: agentFreshPerMTok / 1_000_000, cached: false}
}

// priorCallCost is the fallback per-call cost estimate used before this process has
// observed any extraction call. ~$0.012 was the Terminal-Bench measurement; it is a PRIOR
// to be replaced by observation, never a constant to compute against — which is why
// callCost prefers cheapmodel.AvgCallCost as soon as one call has been made.
const priorCallCost = 0.012

// callCost returns the expected dollar cost of one extraction call. Observed-mean first
// (it reflects this deployment's real prompt sizes, model pricing, and whether the
// preamble cache is working), prior second.
func callCost(pricing cheapmodel.Pricing) float64 {
	if avg, ok := cheapmodel.AvgCallCost(pricing); ok && avg > 0 {
		return avg
	}
	return priorCallCost
}

// gateDecision records why the gate allowed or suppressed a call. The reason string is
// the operator's answer to "why did this run?" / "why didn't it?", surfaced in metrics —
// a gate whose decisions you cannot explain is a gate nobody will trust enough to leave on.
type gateDecision struct {
	allow  bool
	reason string
	// expSaving/expCost are the dollar figures the decision compared, so a surprising
	// suppression can be audited rather than guessed at.
	expSaving float64
	expCost   float64
}

// expectedReuses estimates how many future turns this compaction will be re-applied on.
// This is what makes extraction ever worthwhile under caching: the reduction is frozen and
// replayed on every subsequent turn (see state.go's freeze/reapply), so one call's saving
// is collected repeatedly. Recurrence is the strongest available signal — content the
// system has seen before in ANY session is likely to be seen again.
//
// ponytail: a flat prior per recurrence class, not a fitted model. Two observations
// (seen-before, request-position) capture most of the signal; upgrade to a per-session
// decay fit if the benchmark shows the estimate is what's mispricing calls.
func expectedReuses(seenBefore bool, turnsSoFar int) float64 {
	if seenBefore {
		// Recurred at least once already; the measured cross-session recurrence rate was
		// 82/103 (~80%), so expect several more replays.
		return 6
	}
	if turnsSoFar >= 20 {
		return 3 // late in a long session: fewer turns remain to amortize over
	}
	return 4
}

// evaluateGate decides whether one candidate output is worth an extraction call.
//
// expected saving = tokens we expect to remove x (1 + expected future reuses) x per-token value
// expected cost   = observed mean cost of one extraction call
//
// Allow only when saving strictly exceeds cost. Every suppression carries a reason.
func evaluateGate(sizeTokens int, ratio float64, val tokenValue, cost float64,
	seenBefore bool, turnsSoFar int) gateDecision {

	expectedRemoved := float64(sizeTokens) * ratio
	reuses := expectedReuses(seenBefore, turnsSoFar)
	// The compaction is applied on this turn AND replayed on each expected future turn.
	saving := expectedRemoved * (1 + reuses) * val.perToken

	d := gateDecision{expSaving: saving, expCost: cost}
	if saving <= cost {
		// The honest message: on a caching backend a small output CANNOT pay for a call.
		if val.cached {
			d.reason = "suppressed: cache-aware, saving below call cost"
		} else {
			d.reason = "suppressed: saving below call cost"
		}
		return d
	}
	d.allow = true
	switch {
	case seenBefore:
		d.reason = "allow: recurring content, amortized over reuses"
	case !val.cached:
		d.reason = "allow: non-caching backend, saved tokens at full rate"
	default:
		d.reason = "allow: expected saving exceeds call cost"
	}
	return d
}

// defaultCompressionRatio is the fraction of an output an accepted extraction removes,
// used before this component has observed its own results. Deliberately CONSERVATIVE:
// over-estimating the ratio is how the component talked itself into 271 losing calls.
const defaultCompressionRatio = 0.45

// ratioTracker learns this workload's ACTUAL compression ratio from accepted results, so
// the gate stops guessing after the first few calls. A call that produced nothing counts
// as ratio 0 — a model that keeps failing to reduce this workload's outputs should drive
// the estimate down and shut the gate, which is precisely the feedback the old
// fixed-threshold design lacked.
type ratioTracker struct {
	mu      sync.Mutex
	removed int64
	total   int64
}

// observe records one attempted extraction: removedTok of totalTok (0 removed on a miss).
func (r *ratioTracker) observe(removedTok, totalTok int) {
	if totalTok <= 0 {
		return
	}
	r.mu.Lock()
	r.removed += int64(removedTok)
	r.total += int64(totalTok)
	r.mu.Unlock()
}

// ratio returns the observed compression ratio, or the conservative default until enough
// tokens have been seen for the estimate to mean anything.
func (r *ratioTracker) ratio() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.total < minRatioSampleTokens {
		return defaultCompressionRatio
	}
	return float64(r.removed) / float64(r.total)
}

// minRatioSampleTokens is how much observed content the ratio estimate needs before it
// displaces the default — roughly a couple of medium outputs.
const minRatioSampleTokens = 4000

// --- Triggering (issue #28 part E) -----------------------------------------------
//
// The old trigger was a raw token threshold (min_tokens) that had to be re-picked per
// workload — the component's worst ergonomic problem. The replacement asks the only
// question that generalizes: is this request under enough CONTEXT PRESSURE that removing
// tokens matters, and is there enough evidence that a call will pay?
//
// min_tokens stays honored when set explicitly (backward compatibility); the derived
// trigger is the DEFAULT when it is not.

// contextPressure is the fraction of the model's context window the request occupies.
// 0 when the window is unknown, in which case pressure-based logic is skipped and the
// absolute floors apply — the same fail-open convention Trigger already uses.
func contextPressure(requestTokens, window int) float64 {
	if window <= 0 || requestTokens <= 0 {
		return 0
	}
	return float64(requestTokens) / float64(window)
}

// pressureFloor derives the per-output token floor from context pressure, replacing a
// hand-tuned min_tokens. The shape: when the context is nearly empty compaction buys
// nothing worth an LLM call, so demand a big output; as the window fills, the floor drops
// and smaller outputs become worth reducing. Returns an absolute token count.
//
// The numbers are chosen so a 1M-window model behaves sanely without tuning:
//
//	<25% full  -> 0.6% of window (~6000 tok on 1M): only large outputs
//	 25-60%    -> 0.3% of window (~3000 tok)
//	 60-80%    -> 0.15% of window (~1500 tok)
//	  >80%     -> 0.05% of window (~500 tok): window pressure dominates, compact freely
func pressureFloor(window int, pressure float64) int {
	if window <= 0 {
		return 0 // unknown window: caller falls back to its absolute default
	}
	var frac float64
	switch {
	case pressure > 0.80:
		frac = 0.0005
	case pressure > 0.60:
		frac = 0.0015
	case pressure > 0.25:
		frac = 0.0030
	default:
		frac = 0.0060
	}
	if f := int(frac * float64(window)); f > 0 {
		return f
	}
	return 0
}

// growthRate is tokens added since the previous turn, over the current size — how fast
// this session is accumulating context. A fast-growing request is where compaction has
// the most to work on; a static one has nothing new to reduce and should not re-fire.
func growthRate(currentTokens, prevTokens int) float64 {
	if currentTokens <= 0 || prevTokens <= 0 || currentTokens <= prevTokens {
		return 0
	}
	return float64(currentTokens-prevTokens) / float64(currentTokens)
}

// shouldFire decides whether the LLM path runs on this request at all, and why. It must
// NOT fire on every step of a merely-growing context — that was the old behavior's waste.
//
// minTokensSet reports whether the operator pinned min_tokens explicitly; when they did,
// their threshold governs and this stays out of the way.
func shouldFire(pressure, growth float64, minTokensSet bool) (bool, string) {
	if minTokensSet {
		return true, "explicit min_tokens/trigger configured"
	}
	switch {
	case pressure > 0.60:
		return true, "high context pressure"
	case pressure > 0.25 && growth > 0.10:
		return true, "moderate pressure with fast context growth"
	case pressure > 0.25:
		// Growing slowly at moderate pressure: the per-output floor still gates
		// individual candidates, but do not spend a call on a static context.
		return false, "moderate pressure but context near-static"
	default:
		return false, "low context pressure"
	}
}
