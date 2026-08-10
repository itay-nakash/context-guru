package proxy

import (
	"context"
	"strconv"
	"time"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/apply"
	"github.com/rossoctl/context-guru/components"
)

// Operating modes on the request path (#31).
//
//	sync    — run the pipeline inline and forward its output. Unchanged from before modes
//	          existed, down to the bytes; this is the default.
//	observe — forward the ORIGINAL body, byte for byte, and run the pipeline off-path on a
//	          copy purely to record what it WOULD have saved.
//
// Byte-identity in observe mode is structural, not a property of careful copying: the
// request path never touches the pipeline at all. Fail-open follows from that too — there
// is nothing for a failure to damage, because the forwarded body is the input.

// applyMode rewrites body for forwarding according to the handler's mode, and returns the
// body to forward plus the wall time to charge to the request path. Never returns a nil
// body.
func (h *Handler) applyMode(r *reqInfo) ([]byte, time.Duration) {
	mode := h.mode()
	start := time.Now()

	// Observe: the enforced path does nothing at all. Not "runs and discards" — it never
	// runs, which is what makes the byte-identity guarantee structural. The measurement
	// happens off-path, on a copy, and the request pays only the enqueue.
	if mode == components.ModeObserve && !r.bypassed {
		h.observe(r)
		return r.body, time.Since(start)
	}

	res := apply.BodyOpts(r.ctx, h.pipe, h.store, apply.Opts{
		Provider: r.provider, Body: r.body, Session: r.session, Bypass: r.bypassed,
		Models: r.models, Window: r.window, CacheMode: h.opts.CacheMode,
		Mode: mode, Tracker: h.tracker,
	})
	added := time.Since(start)
	if res.Body == nil {
		return r.body, added
	}
	return res.Body, added
}

// reqInfo is the per-request input both the inline pass and an off-path observation need.
// Bundled because an off-path run outlives the *http.Request it came from and must
// therefore hold a copy of everything, never a pointer into request-scoped state.
type reqInfo struct {
	ctx      context.Context
	provider bschemas.ModelProvider
	body     []byte
	session  string
	bypassed bool
	models   components.ModelSpec
	window   int
}

func (h *Handler) mode() components.Mode {
	if h.opts.Mode == "" {
		return components.ModeSync
	}
	return h.opts.Mode
}

// observe runs the pipeline off-path on a COPY of the request, against observe's own
// disjoint store, and records the result into the hypothetical metric namespace. Two
// independent reasons the enforced request cannot be affected: it was already forwarded
// from the untouched original, and this run touches no state the live path reads.
func (h *Handler) observe(r *reqInfo) {
	if h.pool == nil {
		return
	}
	// The job runs after the response is written, so nothing may alias request-scoped
	// memory — and the context must be the pool's, not the request's, which is cancelled
	// the moment the handler returns.
	info := *r
	info.body = append([]byte(nil), r.body...)
	// A plain counter as the dedup key: one observation per call, coalescing nothing. Also
	// why observe needs no session resolve on the request path — one more thing the
	// enforced path does not pay for.
	key := "observe:" + strconv.FormatUint(h.observeSeq.Add(1), 10)

	h.pool.Enqueue(key, func(ctx context.Context) {
		apply.BodyOpts(ctx, h.pipe, h.shadow, apply.Opts{
			Provider: info.provider, Body: info.body, Session: info.session,
			Models: info.models, Window: info.window, CacheMode: h.opts.CacheMode,
			Mode: components.ModeObserve,
			// The Tracker, so the projection is measured under the SAME cached-prefix
			// boundary an enforcing mode would use. Without it the boundary is unknown,
			// MaxCachedIdx is -1, the tail gate never fires, and every message in the
			// transcript looks compactable — which inflates the projection against what
			// sync actually achieves. Measured on SWE-bench: 9.5% projected against 0.8%
			// enforced, because 50 extract_llm candidates passed the gate instead of 5.
			//
			// Safe off-path despite jobs finishing out of order: the boundary only ever
			// grows, so a late job for a shorter turn cannot move it backwards.
			Tracker: h.tracker,
			// h.shadow, not the live store: see Handler.shadow. The live store must stay
			// clean (a real request must never replay a decision that was never enforced),
			// but the frozen decisions still have to accumulate across turns or the
			// projection under-reports what enforcing would achieve.
		})
		// The pipeline already emitted mode-stamped reports through the emitter; the
		// Aggregator routes anything stamped observe into the potential_* namespace. No
		// separate recording call here, which is what keeps the two namespaces from
		// drifting apart.
	})
}
