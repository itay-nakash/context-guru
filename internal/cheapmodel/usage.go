package cheapmodel

import (
	"os"
	"strconv"
	"sync/atomic"
)

// Usage tracks cumulative token usage of the cheap (config-source) model across
// all NeedsModel component calls in this process. It is the basis for reporting
// the CONTEXT-GURU components' OWN LLM cost (e.g. extract:code's Starlark-writer
// calls) separately from the agent's cost — the proxy exposes it at /stats and the
// benchmark prices it. Process-global (there is a single cheap model per proxy);
// per-component attribution would need the Model interface to carry a label, a
// deferred refinement — today the LLM component in a config is extract, so the
// global total is that component's cost.
//
// The cache tiers are tracked separately because they are the whole question behind
// issue #28's part A: a preamble breakpoint below the model's minimum cacheable prefix
// is silently ignored, so cacheRead staying at 0 across many calls is the ONLY
// evidence that the split is not paying off. Never infer a cache win from placement.
var (
	llmCalls        atomic.Int64
	llmInputTokens  atomic.Int64
	llmOutputTokens atomic.Int64
	llmCacheWrite   atomic.Int64
	llmCacheRead    atomic.Int64
)

// recordUsageCache adds one call's token usage to the process totals, split by cache
// tier. inTok is FRESH (uncached) input on both backends — see openai.go for why that
// needs normalizing there.
func recordUsageCache(inTok, outTok, cacheWrite, cacheRead int) {
	llmCalls.Add(1)
	llmInputTokens.Add(int64(inTok))
	llmOutputTokens.Add(int64(outTok))
	llmCacheWrite.Add(int64(cacheWrite))
	llmCacheRead.Add(int64(cacheRead))
}

// Usage returns the cumulative cheap-model usage (calls, input tokens, output
// tokens) since process start. Kept at this exact signature for backward
// compatibility — /stats' existing three fields are parsed by deploy/harbor/*.py.
func Usage() (calls, inTokens, outTokens int64) {
	return llmCalls.Load(), llmInputTokens.Load(), llmOutputTokens.Load()
}

// CacheUsage returns the cumulative cache-tier token counts (write, read) for the
// cheap model. read==0 after many calls means the preamble breakpoint is inert.
func CacheUsage() (cacheWrite, cacheRead int64) {
	return llmCacheWrite.Load(), llmCacheRead.Load()
}

// Pricing is the per-million-token price of the extraction model, in dollars. The
// economic gate needs the real cost of a call, not a hard-coded "$0.012" — that figure
// was one workload's average, and it changes with the model, the gateway's contract, and
// the prompt size. Rates come from the environment so an operator prices their own
// deployment without a rebuild; the defaults are claude-haiku-4-5 list rates.
type Pricing struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheWritePerMTok float64
	CacheReadPerMTok  float64
}

// HaikuPricing is the default: claude-haiku-4-5 list rates ($1/$5 per MTok), with the
// standard Anthropic cache multipliers (write 1.25x input, read 0.1x input).
func HaikuPricing() Pricing {
	return Pricing{InputPerMTok: 1.00, OutputPerMTok: 5.00, CacheWritePerMTok: 1.25, CacheReadPerMTok: 0.10}
}

// PricingFromEnv returns HaikuPricing overridden by any of CHEAP_MODEL_PRICE_IN,
// _OUT, _CACHE_WRITE, _CACHE_READ (dollars per million tokens). An unparseable or
// absent value leaves the default — pricing must never fail a request.
func PricingFromEnv() Pricing {
	p := HaikuPricing()
	for _, f := range []struct {
		env string
		dst *float64
	}{
		{"CHEAP_MODEL_PRICE_IN", &p.InputPerMTok},
		{"CHEAP_MODEL_PRICE_OUT", &p.OutputPerMTok},
		{"CHEAP_MODEL_PRICE_CACHE_WRITE", &p.CacheWritePerMTok},
		{"CHEAP_MODEL_PRICE_CACHE_READ", &p.CacheReadPerMTok},
	} {
		if v, err := strconv.ParseFloat(os.Getenv(f.env), 64); err == nil && v >= 0 {
			*f.dst = v
		}
	}
	return p
}

// Cost prices one call's token usage in dollars.
func (p Pricing) Cost(inTok, outTok, cacheWrite, cacheRead int64) float64 {
	const perM = 1_000_000.0
	return (float64(inTok)*p.InputPerMTok +
		float64(outTok)*p.OutputPerMTok +
		float64(cacheWrite)*p.CacheWritePerMTok +
		float64(cacheRead)*p.CacheReadPerMTok) / perM
}

// AvgCallCost returns the OBSERVED mean dollar cost of one extraction call so far, and
// whether there is any observation yet. This is what the economic gate should spend
// against: it reflects this deployment's real prompt sizes, this model's real pricing,
// and whether the preamble cache is actually working — none of which a constant can.
// Callers must handle ok==false (no calls yet) with a prior estimate.
func AvgCallCost(p Pricing) (float64, bool) {
	calls := llmCalls.Load()
	if calls == 0 {
		return 0, false
	}
	total := p.Cost(llmInputTokens.Load(), llmOutputTokens.Load(),
		llmCacheWrite.Load(), llmCacheRead.Load())
	return total / float64(calls), true
}
