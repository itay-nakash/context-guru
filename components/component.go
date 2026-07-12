// Package components defines context-guru's component model: the abstract API
// every context-engineering operation implements, the per-component report used
// for metrics, the runtime context handed to each component, and the pipeline
// that stacks them in configured order.
//
// The API is split by lossiness (design D3, after headroom's Rust traits) so
// reversibility is type-enforced:
//
//   - Reformat: lossless repack (re-encode, skeletonize, add cache_control).
//     No information leaves the wire, so nothing needs stashing.
//   - Offload: drops bytes and MUST return a non-optional cache_key proving it
//     stashed the original in the Store — you cannot compile an Offload that
//     silently loses data.
//
// The pipeline is fail-open (any error/panic reverts that component), applies a
// never-worse guard (a component that grows the request is reverted), and emits
// one Report per component.
package components

import (
	"context"
	"time"

	"github.com/kagenti/context-guru/store"
	"github.com/maximhq/bifrost/core/schemas"
)

// Component is the common surface: identity + a per-request enable check.
type Component interface {
	Name() string
	Enabled(*Ctx) bool
}

// Reformat is a lossless component: it repacks the request denser in place and
// loses no information. Examples: format re-encode, code skeleton, cache_control
// injection.
type Reformat interface {
	Component
	Reformat(req *schemas.BifrostChatRequest, rep *Report, c *Ctx) error
}

// Offload is a lossy-but-reversible component: it drops bytes from the wire and
// returns the cache_keys under which it stashed the originals (via c.Store) —
// one per offloaded item. If it shrinks the request but returns no keys, the
// pipeline treats it as a failed offload and reverts (you cannot silently lose
// data). Returning no keys AND leaving the request unchanged is a legitimate
// no-op (set rep.Skipped). Examples: collapse, dedup, cmdfilter, extract,
// smartcrush.
type Offload interface {
	Component
	Offload(req *schemas.BifrostChatRequest, rep *Report, c *Ctx) (cacheKeys []string, err error)
}

// Optional capability interfaces a component MAY also implement.

// Configurable receives its typed config block from the registry/loader.
type Configurable interface {
	Configure(raw []byte) error
}

// NeedsModel is implemented by components that call a cheap LLM (extract,
// summarize); the pipeline injects the resolved ModelSpec via the Ctx.
type NeedsModel interface {
	NeedsModel() bool
}

// ModelSpec tells a NeedsModel component where to get an LLM: a statically
// configured client, or the proxied request's own model+credentials.
type ModelSpec struct {
	Source string // "config" | "incoming"
	// concrete client wiring is added with the extract component (P4)
}

// Ctx is the per-request runtime handed to every component.
type Ctx struct {
	Ctx     context.Context
	Session string
	Store   store.Store
	Model   ModelSpec
	// Bypass short-circuits the whole pipeline (x-context-guru-bypass header).
	Bypass bool
}

// Report is the per-component result, modelled after lean-ctx's ToolOutput
// token accounting and headroom's record_pipeline_run inputs. The pipeline
// fills TokensBefore/After/DurationMs; the component fills CacheKey and may set
// Skipped. It feeds every Emitter.
type Report struct {
	Component    string
	Kind         string // "reformat" | "offload"
	TokensBefore int
	TokensAfter  int
	DurationMs   float64
	CacheKeys    []string // set by Offload components (one per stashed original)
	Skipped      bool     // component ran but chose not to act
	Reverted     bool     // pipeline reverted it (error/panic/never-worse)
	Err          error
}

// Saved returns non-negative tokens saved by this component.
func (r Report) Saved() int {
	if r.TokensAfter > r.TokensBefore {
		return 0
	}
	return r.TokensBefore - r.TokensAfter
}

// RunReport aggregates a whole pipeline run for one request.
type RunReport struct {
	Session      string
	TokensBefore int
	TokensAfter  int
	DurationMs   float64
	Components   []Report
}

// Saved returns the net tokens saved across the run.
func (rr RunReport) Saved() int {
	if rr.TokensAfter > rr.TokensBefore {
		return 0
	}
	return rr.TokensBefore - rr.TokensAfter
}

// Emitter receives one Report per component and one RunReport per request.
// Defined here (not in metrics) so the pipeline has no dependency on any
// concrete telemetry backend; metrics provides the implementations.
type Emitter interface {
	Component(Report)
	Run(RunReport)
}

// NopEmitter discards all telemetry. Default when none is configured.
type NopEmitter struct{}

func (NopEmitter) Component(Report) {}
func (NopEmitter) Run(RunReport)    {}

// clock is injectable in tests; production uses time.Now.
var clock = time.Now
