// Package engine is the public entry point: it runs the stage pipeline over a
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

// ExtractFunc applies cheap-model extraction to large candidate outputs in place.
// It is injected by the host (the engine core has no model client). nil disables the
// Extract stage. It must fail open: on any error, leave candidates untouched.
type ExtractFunc func(ctx context.Context, req canon.Request, cands []reduce.Candidate) error

// Context is the per-request state a stage may read. Plain services only — no
// transport types — so the same stages run in any host.
type Context struct {
	Settings         config.Settings
	Store            store.Rewind
	Evictions        *store.Eviction
	ClientCacheFloor int                 // cache.FloorIndex of the incoming request
	StickyIDs        map[string]struct{} // ids reduced on prior turns (cache stability)
	Extract          ExtractFunc
	GoCtx            context.Context
}

// Stage is one transformation over the canonical request.
type Stage interface {
	Name() string
	Enabled(*Context) bool
	Run(req canon.Request, agg *Report, ctx *Context) error
}

// Report aggregates what the pipeline did. Embeds the reduce report and adds
// cross-stage info.
type Report struct {
	Skipped       bool
	StageErrors   int
	Candidates    []reduce.Candidate
	CacheInjected bool
	Reduce        reduce.Report
}

// Engine runs stages over requests. Construct with New.
type Engine struct {
	settings config.Settings
	store    store.Rewind
	evict    *store.Eviction
	stages   []Stage
	extract  ExtractFunc
}

// New builds an engine with the default stage pipeline (Reduce → Extract → Cache).
func New(settings config.Settings, st store.Rewind, ev *store.Eviction) *Engine {
	if st == nil {
		st = store.NewMemory(0)
	}
	if ev == nil {
		ev = store.NewEviction()
	}
	return &Engine{
		settings: settings, store: st, evict: ev,
		stages: []Stage{ReduceStage{}, ExtractStage{}, CacheStage{}},
	}
}

// SetExtract injects the cheap-model extraction function (enables the Extract stage).
func (e *Engine) SetExtract(fn ExtractFunc) { e.extract = fn }

// RegisterStage inserts a custom stage at index (appended if index < 0 or too large).
func (e *Engine) RegisterStage(s Stage, index int) {
	if index < 0 || index > len(e.stages) {
		e.stages = append(e.stages, s)
		return
	}
	e.stages = append(e.stages[:index], append([]Stage{s}, e.stages[index:]...)...)
}

// Store exposes the rewind store so the host can serve expand requests.
func (e *Engine) Store() store.Rewind { return e.store }

// Expand recovers the original content for a rewind id.
func (e *Engine) Expand(id string) (string, bool) { return e.store.Get(id) }

// Transform reduces req in place and returns a Report. It never returns an error:
// any stage failure is isolated (the request is restored to its pre-stage state) and
// counted, so the worst case is a no-op forward. The returned request is the one the
// host should forward.
func (e *Engine) Transform(goctx context.Context, req canon.Request) (canon.Request, Report) {
	if e.settings.Disabled {
		return req, Report{Skipped: true}
	}
	ctx := &Context{
		Settings: e.settings, Store: e.store, Evictions: e.evict,
		ClientCacheFloor: cache.FloorIndex(req.Root),
		Extract:          e.extract, GoCtx: goctx,
	}
	var agg Report
	for _, st := range e.stages {
		if !st.Enabled(ctx) {
			continue
		}
		if err := safeRun(st, req, &agg, ctx); err != nil {
			agg.StageErrors++
		}
	}
	return req, agg
}

// safeRun isolates a stage: it snapshots the request and restores it if the stage
// returns an error or panics, so a faulty stage cannot corrupt the forwarded request.
func safeRun(st Stage, req canon.Request, agg *Report, ctx *Context) (err error) {
	snapshot := req.Clone()
	defer func() {
		if r := recover(); r != nil {
			req.Root = snapshot.Root
			err = errFromPanic(r)
		}
	}()
	if e := st.Run(req, agg, ctx); e != nil {
		req.Root = snapshot.Root
		return e
	}
	return nil
}
