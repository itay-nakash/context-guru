// Package engine is the public entry point: it runs a pipeline of Compactors over a
// canonical request and exposes Transform (reduce a request, fail-open) and Expand
// (recover a collapsed block). A host — the proxy binary, an eval-containers adapter,
// or a Kagenti AuthBridge plugin — wraps an Engine; the engine has no transport code.
package engine

import (
	"context"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/config"
	"github.com/kagenti/lab-context-engineering/internal/cache"
	"github.com/kagenti/lab-context-engineering/internal/markers"
	"github.com/kagenti/lab-context-engineering/internal/reduce"
	"github.com/kagenti/lab-context-engineering/internal/store"
)

// FindMarkers returns the rewind ids of any reversible markers in text. A host uses
// this to serve an expand request or to rehydrate collapsed blocks before forwarding
// (e.g. ahead of a summarization turn). Pair with Engine.Expand to recover originals.
func FindMarkers(text string) []string { return markers.FindIDs(text) }

// Model is the LLM client an LLM-using Compactor calls. The host (the proxy, or a
// Kagenti AuthBridge plugin) injects a concrete implementation; the engine core has no
// model client of its own.
type Model interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// ModelSpec resolves to the Model a Compactor should use for a request. This is the
// ONE credential mechanism shared by every LLM compactor (extract, summarize, and any
// future approach): Source "config" uses Static — a client built once from the
// configured transport + credentials; Source "incoming" (UseIncoming) uses the
// per-request model the host built from the proxied request's own model + credentials
// (Context.RequestModel).
type ModelSpec struct {
	Static      Model
	UseIncoming bool
}

// Resolve returns the Model for this request, or nil if none is available (fail-open:
// the compactor then leaves the conversation untouched).
func (s ModelSpec) Resolve(c *Context) Model {
	if s.UseIncoming {
		return c.RequestModel
	}
	return s.Static
}

type reqModelKey struct{}

// WithRequestModel returns a context carrying the model built from the incoming
// request's own model + credentials. The proxy sets this per request; compactors with
// a ModelSpec{UseIncoming:true} resolve to it.
func WithRequestModel(ctx context.Context, m Model) context.Context {
	return context.WithValue(ctx, reqModelKey{}, m)
}

func requestModel(ctx context.Context) Model {
	m, _ := ctx.Value(reqModelKey{}).(Model)
	return m
}

// Context is the per-request state a compactor may read.
type Context struct {
	Settings         config.Settings
	Store            store.Rewind
	Evictions        *store.Eviction
	ClientCacheFloor int                 // cache.FloorIndex of the incoming request
	StickyIDs        map[string]struct{} // ids reduced on prior turns (cache stability)
	RequestModel     Model               // model built from the incoming request (nil if none)
	GoCtx            context.Context
}

// Compactor is the single abstraction every compaction approach implements: given the
// canonical request (messages + tools) it returns the transformed request. reduce,
// extract, summarize, truncate and cache all implement it. Add a new approach by
// implementing Compactor and registering it by name (Engine.Register), then listing
// that name in Settings.Compactors.
type Compactor interface {
	Name() string
	Enabled(*Context) bool
	// Compact transforms the conversation and returns the new request. Fail-open: the
	// engine snapshots and restores the request if Compact returns an error or panics.
	Compact(req canon.Request, agg *Report, c *Context) (canon.Request, error)
}

// Report aggregates what the pipeline did. Embeds the reduce report and adds
// cross-compactor info.
type Report struct {
	Skipped       bool
	StageErrors   int
	Candidates    []reduce.Candidate
	CacheInjected bool
	Reduce        reduce.Report
}

// Engine runs a name-resolved pipeline of Compactors over requests. Construct with New.
type Engine struct {
	settings config.Settings
	store    store.Rewind
	evict    *store.Eviction
	registry map[string]Compactor
}

// Compactor names. defaultPipeline runs extract BEFORE reduce so the extractor sees
// large structured tool outputs intact; reduce then applies lossless actions to
// whatever remains, and cache injects breakpoints last.
const (
	reduceName    = "reduce"
	extractName   = "extract"
	summarizeName = "summarize"
	truncateName  = "truncate"
	cacheName     = "cache"
)

var defaultPipeline = []string{extractName, reduceName, cacheName}

// New builds an engine with the built-in deterministic compactors registered
// (reduce, truncate, cache). LLM compactors (extract, summarize) are added by the host
// via EnableExtract / EnableSummarize. The pipeline order is Settings.Compactors (by
// name), or defaultPipeline when empty; names with no registered compactor are skipped.
func New(settings config.Settings, st store.Rewind, ev *store.Eviction) *Engine {
	if st == nil {
		st = store.NewMemory(0)
	}
	if ev == nil {
		ev = store.NewEviction()
	}
	e := &Engine{settings: settings, store: st, evict: ev, registry: map[string]Compactor{}}
	e.Register(reduceName, Reducer{})
	e.Register(truncateName, Truncator{})
	e.Register(cacheName, Cacher{})
	return e
}

// Register adds (or overrides) a compactor by name. It participates in the pipeline
// only when its name appears in the resolved order (Settings.Compactors or default).
func (e *Engine) Register(name string, c Compactor) { e.registry[name] = c }

func (e *Engine) order() []string {
	if len(e.settings.Compactors) > 0 {
		return e.settings.Compactors
	}
	return defaultPipeline
}

// Store exposes the rewind store so the host can serve expand requests.
func (e *Engine) Store() store.Rewind { return e.store }

// Expand recovers the original content for a rewind id.
func (e *Engine) Expand(id string) (string, bool) { return e.store.Get(id) }

// Transform runs the compactor pipeline over req and returns a Report. It never returns
// an error: any compactor failure is isolated (the request is restored to its pre-step
// state) and counted, so the worst case is a no-op forward. The returned request is the
// one the host should forward.
func (e *Engine) Transform(goctx context.Context, req canon.Request) (canon.Request, Report) {
	if e.settings.Disabled {
		return req, Report{Skipped: true}
	}
	ctx := &Context{
		Settings: e.settings, Store: e.store, Evictions: e.evict,
		ClientCacheFloor: cache.FloorIndex(req.Root),
		RequestModel:     requestModel(goctx), GoCtx: goctx,
	}
	var agg Report
	for _, name := range e.order() {
		c, ok := e.registry[name]
		if !ok {
			continue
		}
		if !c.Enabled(ctx) {
			continue
		}
		out, err := safeRun(c, req, &agg, ctx)
		if err != nil {
			agg.StageErrors++
			continue
		}
		req = out
	}
	return req, agg
}

// safeRun isolates a compactor: it snapshots the request and restores it if Compact
// returns an error or panics, so a faulty compactor cannot corrupt the forwarded
// request.
func safeRun(c Compactor, req canon.Request, agg *Report, ctx *Context) (out canon.Request, err error) {
	snapshot := req.Clone()
	out = req
	defer func() {
		if r := recover(); r != nil {
			out = canon.Request{Root: snapshot.Root}
			err = errFromPanic(r)
		}
	}()
	res, e := c.Compact(req, agg, ctx)
	if e != nil {
		return canon.Request{Root: snapshot.Root}, e
	}
	return res, nil
}
