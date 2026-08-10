package offload

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/internal/cheapmodel"
	"github.com/rossoctl/context-guru/internal/extract"
	"github.com/rossoctl/context-guru/metrics"
	"github.com/rossoctl/context-guru/schema"
	"gopkg.in/yaml.v3"
)

// debugExtractLLM logs per-request candidate accounting when CONTEXT_GURU_DEBUG is set.
var debugExtractLLM = os.Getenv("CONTEXT_GURU_DEBUG") != ""

// llmCallTimeout bounds a SINGLE in-request extract model call. Kept tight so a slow
// or rate-limited compaction model fails open FAST (leave the output verbatim this
// turn) instead of stalling the agent's request — synchronous compaction is on the
// hot path, so a long timeout here can push the agent's own request past its deadline.
const llmCallTimeout = 15 * time.Second

// llmConcurrency bounds how many of a request's candidate compactions run at once.
// Independent per-output calls run concurrently so a turn's parallel tool outputs cost
// ~one call's wall time instead of the sum. Bounded so a burst can't overwhelm the
// cheap-model endpoint. (A single-call batch alternative was measured ~3× worse on
// tokens saved with no latency win — see docs/CACHE_AWARE_ITERATIONS.md.)
const llmConcurrency = 4

func init() { components.Register("extract_llm", newExtractLLM) }

// ExtractLLM is the relevance-aware, LLM-driven tool-output reducer. A cheap model
// writes a small Starlark program that trims ONE tool output down to what the agent
// needs next (it may delete OR rewrite via regex, preserving ids/paths/errors
// verbatim, and may emit a one-line SUMMARY that goes into the marker). The program
// runs in a sandbox (no imports/IO, step+time limited) and the result must pass a
// sanity check (non-empty, strictly smaller, keep-ids present); on any miss the item
// is left verbatim. The full original is always stashed (reversible via expand).
//
// It is the EXPENSIVE pass, so it is throttled (per-session cadence + per-request
// cap) and — in cache-aware mode — only rewrites tool outputs in the uncached tail
// that are still medium/large AFTER the deterministic components ran. Prior
// compactions are reused byte-for-byte from state so the request prefix stays stable.
type ExtractLLM struct {
	minTokens     int
	strategy      string
	modelSource   string
	modelClient   components.Model
	trigger       components.Trigger
	mode          markerMode
	rewrite       bool
	llmEveryN     int
	llmMaxPerReq  int
	skipFileReads *bool // nil = auto (skip when cache-aware); true/false = force
	mu            sync.Mutex
	llmSeen       map[string]int // session -> count of qualifying (LLM-eligible) requests

	// minTokensSet records whether the operator pinned min_tokens / trigger explicitly.
	// When they did, their threshold governs (backward compatibility). When they did not,
	// the derived pressure-based trigger is the default — no per-workload tuning (#28 E).
	minTokensSet bool
	// gate enables the economic gate (#28 D). Default on; `economic_gate: false` restores
	// the old spend-on-size behavior for anyone who needs to reproduce old numbers.
	gate bool
	// allowCached permits extraction on prompt-caching backends. Default FALSE — see
	// extractLLMConfig.AllowOnCachingBackend for why the default ships disabled there.
	allowCached bool
	// pricing prices the extraction model's tokens for the gate's cost side (#28 D).
	pricing cheapmodel.Pricing
	// ratios learns this workload's real compression ratio instead of assuming one.
	ratios ratioTracker
	// prevTokens tracks per-session request size so growth rate is measurable (#28 E).
	prevTokens map[string]int
	// modelName identifies the extraction model in the global cache key, so switching
	// models misses rather than serving another model's extraction (#28 C).
	modelName string
}

type extractLLMConfig struct {
	MinTokens    int                `yaml:"min_tokens"`
	Strategy     string             `yaml:"strategy"`             // code (default) | single | rlm | auto
	LLMEveryN    int                `yaml:"llm_every_n_requests"` // throttle LLM path: fire once per N requests/session
	LLMMaxPerReq int                `yaml:"llm_max_per_request"`  // cap LLM calls per firing request (0 = unlimited)
	Model        modelConfig        `yaml:"model"`
	Trigger      components.Trigger `yaml:"trigger"`
	MarkerMode   string             `yaml:"marker_mode"` // full (default) | summary | off
	// Rewrite lets the program reword/summarize/collapse (not just delete), dropping
	// the strict deletion-only containment proof; ids/paths/errors/keep-ids are still
	// required verbatim by the sanity check. Default true (the powerful mode) — set
	// false to force verified deletion-only.
	Rewrite *bool `yaml:"rewrite"`
	// AllowOnCachingBackend re-enables extraction on prompt-caching backends. Unset =
	// FALSE: the component is disabled by default there, because every caching workload
	// measured in #28 came out net-negative even with the gate working correctly
	// (break-even ~30,500 tokens/output against a largest-observed 2,053). Shipping a
	// component our own numbers say loses money, guarded only by a doc note, is not a
	// defensible default. Set true if your outputs are genuinely huge; the gate's
	// economics then decide each call as normal.
	AllowOnCachingBackend *bool `yaml:"allow_on_caching_backend"`
	// EconomicGate opts out of the expected-value gate (#28 D). Unset = ON (the default):
	// only call the LLM when the expected saving exceeds the expected call cost, priced
	// from real model rates and the cache-awareness of the traffic. Set false to restore
	// the pre-#28 spend-on-size behavior — needed only to reproduce old benchmark numbers.
	EconomicGate *bool `yaml:"economic_gate"`
	// SkipFileReads controls whether line-numbered source-file dumps are left verbatim.
	// Tri-state: unset = AUTO (skip when the request is prompt-cached, reduce otherwise);
	// true = always skip; false = always reduce. Rationale (measured, SWE-bench 50):
	// on a ~98%-cached agent, file reads already bill at the cheap cache-read rate, so
	// skeletonizing them saves almost nothing yet costs the compaction LLM + one-time
	// cache-write transitions → +30% billed cost. On a NON-caching backend the same
	// reduction is a direct saving. So AUTO skips file reads exactly when caching makes
	// them cheap. See docs/CACHE_AWARE_ITERATIONS.md.
	SkipFileReads *bool `yaml:"skip_file_reads"`
}

func newExtractLLM(raw []byte) (components.Component, error) {
	cfg := extractLLMConfig{MinTokens: 300, Strategy: "code"}
	// Detect whether the operator pinned a threshold BEFORE defaults are applied: the
	// distinction between "unset" and "set to the default value" is what decides whether
	// the smart trigger or their number governs, so it must be read from the raw YAML.
	explicit := false
	if len(raw) > 0 {
		var probe struct {
			MinTokens *int `yaml:"min_tokens"`
			Trigger   *struct {
				MinRequestTokens *int `yaml:"min_request_tokens"`
				MinOutputTokens  *int `yaml:"min_output_tokens"`
			} `yaml:"trigger"`
		}
		if err := yaml.Unmarshal(raw, &probe); err == nil {
			explicit = probe.MinTokens != nil ||
				(probe.Trigger != nil &&
					(probe.Trigger.MinRequestTokens != nil || probe.Trigger.MinOutputTokens != nil))
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	rewrite := true
	if cfg.Rewrite != nil {
		rewrite = *cfg.Rewrite
	}
	if cfg.Strategy == "" {
		cfg.Strategy = "code"
	}
	gate := true // economic gate on by default (#28 D)
	if cfg.EconomicGate != nil {
		gate = *cfg.EconomicGate
	}
	// Off by default on caching backends (see AllowOnCachingBackend). Disabling the gate
	// entirely is an explicit request for pre-#28 behavior, so honor it here too — otherwise
	// `economic_gate: false` would still be silently blocked on caching traffic.
	allowCached := !gate
	if cfg.AllowOnCachingBackend != nil {
		allowCached = *cfg.AllowOnCachingBackend
	}
	return &ExtractLLM{
		minTokens: cfg.MinTokens, strategy: cfg.Strategy,
		modelSource: cfg.Model.Source, modelClient: cfg.Model.Client(),
		trigger: cfg.Trigger, mode: parseMarkerMode(cfg.MarkerMode), rewrite: rewrite,
		llmEveryN: cfg.LLMEveryN, llmMaxPerReq: cfg.LLMMaxPerReq,
		skipFileReads: cfg.SkipFileReads, llmSeen: map[string]int{},
		minTokensSet: explicit, gate: gate, allowCached: allowCached,
		pricing:    cheapmodel.PricingFromEnv(),
		prevTokens: map[string]int{}, modelName: cfg.Model.Model,
	}, nil
}

// noteRequestSize records this request's size for the session and returns the previous
// one, so the trigger can measure context growth rate (#28 E).
func (e *ExtractLLM) noteRequestSize(session string, tokens int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	prev := e.prevTokens[session]
	e.prevTokens[session] = tokens
	return prev
}

func (*ExtractLLM) Name() string                 { return "extract_llm" }
func (*ExtractLLM) Enabled(*components.Ctx) bool { return true }

func (e *ExtractLLM) outputFloor(window int) int {
	return e.trigger.OutputFloor(window, e.minTokens)
}

// llmAllowedThisRequest applies the per-session cadence: true on the 1st qualifying
// request and every Nth after, so the LLM path fires "every multiple steps".
func (e *ExtractLLM) llmAllowedThisRequest(session string) bool {
	if e.llmEveryN <= 1 {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.llmSeen[session]++
	return (e.llmSeen[session]-1)%e.llmEveryN == 0
}

var lineNumberedRe = regexp.MustCompile(`^\s{0,6}\d+[\t ]`)

// looksLikeFileRead reports whether content is a line-numbered source-file dump (a
// read/cat -n output): most non-empty lines begin with a line number. Such outputs
// are whole files the agent is working with — irreducible — so skip the model call.
func looksLikeFileRead(content string) bool {
	checked, numbered := 0, 0
	for _, ln := range strings.Split(content, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		checked++
		if lineNumberedRe.MatchString(ln) {
			numbered++
		}
		if checked >= 40 {
			break
		}
	}
	return checked >= 8 && numbered*100/checked >= 60
}

func (e *ExtractLLM) Offload(req *bschemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	fires := e.trigger.Fires(req, c.CtxWindow)
	goal := conversationGoal(req)
	query := keywords(goal)
	if len(query) == 0 {
		rep.Skipped = true
		return nil, nil
	}
	model := e.modelClient
	if model == nil {
		model = c.Model.For(e.modelSource)
	}
	// Per-session cadence: on throttled steps drop the model (skip this request).
	if model != nil && fires && !e.llmAllowedThisRequest(c.Session) {
		model = nil
	}
	// Derived trigger (#28 E): context pressure + growth rate replace a hand-tuned
	// threshold. When min_tokens/trigger is set explicitly the operator's value governs.
	reqTokens := schema.MessagesTokens(req)
	prevTokens := e.noteRequestSize(c.Session, reqTokens)
	pressure := contextPressure(reqTokens, c.CtxWindow)
	growth := growthRate(reqTokens, prevTokens)
	pressureFires, triggerReason := shouldFire(pressure, growth, e.minTokensSet)
	// An unknown context window (0) makes pressure meaningless; fall back to the
	// configured Trigger alone, the same fail-open convention Trigger itself uses.
	if c.CtxWindow <= 0 {
		pressureFires, triggerReason = fires, "context window unknown; absolute trigger only"
	}
	if model != nil && !pressureFires {
		model = nil // no model call this request; frozen reapplications still run below
	}
	metrics.RecordExtractionReason(triggerReason)

	floor := e.outputFloor(c.CtxWindow)
	// Without an explicit min_tokens, derive the per-output floor from context pressure so
	// there is no per-workload number to pick (#28 E).
	if !e.minTokensSet {
		if pf := pressureFloor(c.CtxWindow, pressure); pf > 0 {
			floor = pf
		}
	}
	// Gate inputs shared by every candidate this request.
	val := savedTokenValue(c)
	ratio := e.ratios.ratio()
	turnsSoFar := len(req.Input)
	extCfg := extract.DefaultCfg()
	extCfg.Mode, extCfg.Floor, extCfg.Rewrite = e.strategy, floor, e.rewrite

	keepIDs := extract.HarvestIdentifiers(goal, 40)
	tools := toolIndices(req)
	var keys []string
	changed := 0

	// apply splices a compacted projection + marker into message i (serial: store writes
	// and message mutation are not concurrency-safe).
	apply := func(i int, content, projected, summary string) {
		if projected == "" || schema.TextTokens(projected) >= schema.TextTokens(content) {
			return
		}
		hint := " [full output: call " + expand.ToolName + "]"
		newText, key, eff, ok := tryMark(c, e.mode, content, hint, func(tok string) string {
			if summary != "" {
				return projected + "\n[" + summary + "] " + tok
			}
			return projected + "\n" + tok
		})
		if !ok {
			return
		}
		commitMark(c, rep, eff, key, content)
		schema.SetMessageText(&req.Input[i], newText)
		changed++
		if key != "" {
			keys = append(keys, key)
		}
	}

	// Phase 1 (serial, cheap): reapply frozen compactions on every turn (keeps the
	// request prefix byte-stable so the provider cache stays warm), and collect the NEW
	// candidates that still need a model call.
	type cand struct {
		i       int
		content string
		id      string
	}
	var cands []cand
	skipFR := false
	if e.skipFileReads != nil {
		skipFR = *e.skipFileReads
	}
	var dbgTail, dbgFloor, dbgPlace, dbgReapply, dbgBigTailBlocked, dbgMaxSz int
	for _, i := range tools {
		msg := &req.Input[i]
		if !schema.Rewritable(*msg) {
			continue
		}
		content := schema.MessageText(*msg)
		if content == "" || expand.HasPlaceholder(content) {
			dbgPlace++
			continue
		}
		id := extract.ContentKey(content)
		// If the agent recently EXPANDED this content, leave it verbatim (re-compacting it
		// would just trigger another expand — a loop). The expand handler marks it.
		if isKeptVerbatim(c, id) {
			continue
		}
		// Global, content-hash result cache (#28 C). Keyed on content + prompt version +
		// model + config fingerprint, with NO session prefix — an identical output in a
		// different session reuses the reduction instead of paying for it again (measured:
		// 82 of 103 unique contents recurred across sessions). A version/model/config
		// change misses rather than serving a stale extraction.
		//
		// Cross-session reuse is gated on RECOVERABILITY, not verification. The result was
		// derived toward the goal of whichever session produced it, and in the default rewrite
		// mode the containment proof is deliberately skipped — so a reused result can be a lossy
		// rewrite steered by an unrelated task. That is acceptable only while the agent can get
		// the original back: with a full (reversible) marker the stash is refreshed and `expand`
		// recovers it. Without one (marker_mode summary/off, or a non-persisting store) the drop
		// is permanent, and reusing another session's lossy rewrite could silently lose content
		// THIS task needed. There, fall back to same-session reuse only.
		gkey := extract.ResultKey(id, e.modelName, extCfg)
		var cached []byte
		hit := false
		if !e.rewrite || effectiveMode(c, e.mode) == markerFull {
			cached, hit = getResultGlobal(c, gkey)
		}
		if !hit {
			// One-time migration read: honor a pre-#28 session-scoped entry so upgrading
			// does not re-pay for work already done in this session.
			cached, hit = getResult(c, id)
		}
		metrics.RecordExtractionCacheLookup(hit)
		if hit {
			summary, _ := getSummaryGlobal(c, gkey)
			if summary == "" {
				summary, _ = getSummary(c, id)
			}
			apply(i, content, string(cached), summary)
			dbgReapply++
			continue
		}
		// A NEW compaction, on the UNCACHED region only (cache-safe): when cache-aware that
		// is every message newer than last turn (index > MaxCachedIdx) — catching ALL of a
		// turn's tool outputs including PARALLEL tool calls, never the cached prefix. When
		// caching is off, any message is fair game. File reads included (largest mass);
		// safe because we never touch already-cached content and freeze+reapply the result.
		sz := schema.TextTokens(content)
		if sz > dbgMaxSz {
			dbgMaxSz = sz
		}
		if c.CacheAware && !c.TailOnly(i) {
			dbgTail++
			if sz >= floor {
				dbgBigTailBlocked++ // a large output we skipped ONLY because it's not in the tail
			}
			continue
		}
		if sz < floor {
			dbgFloor++
			continue // only medium/large outputs are worth a model call
		}
		if huge := e.trigger.IsHuge(sz, c.CtxWindow); !c.CacheAware && !fires && !huge {
			continue
		}
		if model == nil {
			continue
		}
		if skipFR && looksLikeFileRead(content) {
			continue
		}
		// The economic gate (#28 D). This is the check the component never had: is one
		// LLM call worth it for THIS output, given that a saved token in a cached region
		// is worth 10x less? Where caching makes extraction pointless, suppress it; on a
		// non-caching backend or for recurring content, allow it.
		// Record the sighting BEFORE the gate reads it, and read the PRIOR value. The flag
		// means "seen on an earlier turn/session", so marking it after the gate allowed a
		// call made first sight reclassify itself as recurring and collect a 50% valuation
		// bump (6 expected reuses vs 4) it had not earned — the gate over-firing in the
		// opposite direction from the two pessimistic priors fixed earlier. Marking on
		// OBSERVATION also means a suppressed candidate still counts as seen, which is
		// correct: recurrence is a property of the content, not of what we decided to spend.
		seenBefore := markSeenContent(c, id)
		if e.gate {
			// Stop exploring once calls are observed to be slow: exploration spends wall
			// clock as well as money, and an agent on a task deadline feels the former more.
			explore := !tooSlowToExplore(metrics.ExtractionAvgLatencyMs()) &&
				e.ratios.exploring(c.Session)
			d := evaluateGate(sz, ratio, val, callCost(e.pricing, sz), seenBefore, turnsSoFar,
				explore, e.allowCached)
			if !d.allow {
				metrics.RecordExtractionSuppressed(d.reason)
				if debugExtractLLM {
					slog.Info("cg.debug.extract_llm.gate", "decision", "suppress",
						"reason", d.reason, "size", sz, "exp_saving_usd", d.expSaving,
						"exp_cost_usd", d.expCost, "cacheAware", c.CacheAware)
				}
				continue
			}
			metrics.RecordExtractionReason(d.reason)
		}
		cands = append(cands, cand{i, content, id})
	}
	if debugExtractLLM && len(tools) > 0 {
		slog.Info("cg.debug.extract_llm", "tools", len(tools), "cands", len(cands),
			"reapplied", dbgReapply, "skip_placeholder", dbgPlace, "skip_tail", dbgTail,
			"skip_floor", dbgFloor, "max_output_tokens", dbgMaxSz, "big_but_not_tail", dbgBigTailBlocked,
			"cacheAware", c.CacheAware, "maxCachedIdx", c.MaxCachedIdx, "floor", floor,
			"nInput", len(req.Input))
	}
	if e.llmMaxPerReq > 0 && len(cands) > e.llmMaxPerReq {
		cands = cands[:e.llmMaxPerReq] // cap model calls per request
	}

	// Phase 2 (parallel): the candidate compactions are independent. A focused per-output
	// prompt ("trim THIS one output") gives a much better reduction than a single program
	// over the whole heterogeneous batch (measured on SWE: ~3× more tokens saved), and
	// running them concurrently (bounded) keeps a turn's cost to ~one call's wall time —
	// so parallel beats a single-call batch on tokens AND latency. Each output fails open
	// independently (a miss leaves that one verbatim).
	if len(cands) > 0 {
		type outT struct{ projected, summary string }
		out := make([]outT, len(cands))
		sem := make(chan struct{}, llmConcurrency)
		var wg sync.WaitGroup
		for k := range cands {
			wg.Add(1)
			go func(k int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				ctx, cancel := context.WithTimeout(c.Ctx, llmCallTimeout)
				defer cancel()
				before := schema.TextTokens(cands[k].content)
				start := time.Now()
				res, sum, _ := extract.RunExtractionSummary(ctx, cands[k].content, goal, keepIDs, before, extCfg, model)
				metrics.RecordExtractionCall(float64(time.Since(start).Milliseconds()))
				if res != "" && res != cands[k].content {
					out[k] = outT{res, sum}
					// Feed the observed ratio so the gate prices future calls on what this
					// workload actually achieves, not on an assumption.
					e.ratios.observe(before-schema.TextTokens(res), before)
					metrics.RecordExtractionSaving(before - schema.TextTokens(res))
				} else {
					e.ratios.observe(0, before) // a miss is real evidence: ratio 0
				}
			}(k)
		}
		wg.Wait()
		for k := range cands { // Phase 3 (serial): freeze + splice.
			if out[k].projected == "" {
				continue
			}
			// Publish to the GLOBAL namespace only when the result is recoverable (or verified
			// deletion-only) — the same condition the read side checks. An unverified, lossy
			// rewrite with no way back must not become another session's starting point; keep it
			// session-scoped so this session still benefits across its own turns.
			gkey := extract.ResultKey(cands[k].id, e.modelName, extCfg)
			if !e.rewrite || effectiveMode(c, e.mode) == markerFull {
				putResultGlobal(c, gkey, []byte(out[k].projected))
				if out[k].summary != "" {
					putSummaryGlobal(c, gkey, out[k].summary)
				}
			} else {
				putResult(c, cands[k].id, []byte(out[k].projected))
				if out[k].summary != "" {
					putSummary(c, cands[k].id, out[k].summary)
				}
			}
			apply(cands[k].i, cands[k].content, out[k].projected, out[k].summary)
		}
	}

	if changed == 0 {
		rep.Skipped = true
	}
	return keys, nil
}
