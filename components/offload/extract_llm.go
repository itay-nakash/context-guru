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
	"github.com/rossoctl/context-guru/internal/extract"
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
	if len(raw) > 0 {
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
	return &ExtractLLM{
		minTokens: cfg.MinTokens, strategy: cfg.Strategy,
		modelSource: cfg.Model.Source, modelClient: cfg.Model.Client(),
		trigger: cfg.Trigger, mode: parseMarkerMode(cfg.MarkerMode), rewrite: rewrite,
		llmEveryN: cfg.LLMEveryN, llmMaxPerReq: cfg.LLMMaxPerReq,
		skipFileReads: cfg.SkipFileReads, llmSeen: map[string]int{},
	}, nil
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
	floor := e.outputFloor(c.CtxWindow)
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
	var dbgTail, dbgFloor, dbgPlace, dbgReapply, dbgBigTailBlocked int
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
		if cached, hit := getResult(c, id); hit {
			summary, _ := getSummary(c, id)
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
		// Exception to the tail gate: a result-cache entry this session established and the
		// store then LOST. The provider already holds the compacted bytes for this message,
		// so re-deriving them restores the cached representation, whereas leaving the output
		// verbatim is what flips it and re-writes the suffix. (Unlike the deterministic
		// offloaders this costs a model call and the LLM may not reproduce the bytes
		// exactly — but the alternative is a GUARANTEED full-suffix cache-write, the more
		// expensive of the two by a wide margin.)
		if c.CacheAware && !c.TailOnly(i) && !repairLostResult(c, id) {
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
		cands = append(cands, cand{i, content, id})
	}
	if debugExtractLLM && len(tools) > 0 {
		slog.Info("cg.debug.extract_llm", "tools", len(tools), "cands", len(cands),
			"reapplied", dbgReapply, "skip_placeholder", dbgPlace, "skip_tail", dbgTail,
			"skip_floor", dbgFloor, "big_but_not_tail", dbgBigTailBlocked,
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
				cfg := extract.DefaultCfg()
				cfg.Mode, cfg.Floor, cfg.Rewrite = e.strategy, floor, e.rewrite
				ctx, cancel := context.WithTimeout(c.Ctx, llmCallTimeout)
				defer cancel()
				res, sum, _ := extract.RunExtractionSummary(ctx, cands[k].content, goal, keepIDs, schema.TextTokens(cands[k].content), cfg, model)
				if res != "" && res != cands[k].content {
					out[k] = outT{res, sum}
				}
			}(k)
		}
		wg.Wait()
		for k := range cands { // Phase 3 (serial): freeze + splice.
			if out[k].projected == "" {
				continue
			}
			putResult(c, cands[k].id, []byte(out[k].projected))
			if out[k].summary != "" {
				putSummary(c, cands[k].id, out[k].summary)
			}
			apply(cands[k].i, cands[k].content, out[k].projected, out[k].summary)
		}
	}

	if changed == 0 {
		rep.Skipped = true
	}
	return keys, nil
}
