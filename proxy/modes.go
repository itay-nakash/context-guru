package proxy

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/apply"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/store"
)

// Operating modes on the request path (#31).
//
// One function decides what the client's request becomes, per mode:
//
//	sync    — run the pipeline inline and forward its output. Unchanged from before
//	          modes existed, down to the bytes; this is the default.
//	async   — run the pipeline inline WITHOUT model clients, so it costs deterministic
//	          time only and replays whatever a previous turn's off-path job already
//	          froze. Then enqueue the expensive full compaction for the NEXT turn.
//	          While a compaction is pending, no cache breakpoint is placed at or beyond
//	          the tail that compaction will replace (see apply.Opts).
//	observe — forward the ORIGINAL body, byte for byte, and run the pipeline off-path
//	          on a copy purely to record what it WOULD have saved.
//
// Fail-open is per mode: sync and async forward the best body they have, observe
// forwards the input by construction, and any panic in an off-path job is contained by
// the pool (nothing was riding on it).

// applyMode rewrites body for forwarding according to the handler's mode, and returns
// the body to forward plus the wall time to charge to the request path. Never returns
// a nil body.
func (h *Handler) applyMode(r *httpReqInfo) ([]byte, time.Duration) {
	mode := h.mode()
	start := time.Now()

	// Observe: the enforced path does nothing at all. Not "runs and discards" — the
	// request path never touches the pipeline, which is what makes the byte-identity
	// guarantee structural rather than a property of careful copying. The measurement
	// happens on the pool, on a copy, and the request pays only the enqueue.
	if mode == components.ModeObserve && !r.bypassed {
		h.enqueueObserve(r)
		return r.body, time.Since(start)
	}

	res := apply.BodyOpts(r.ctx, h.pipe, h.store, apply.Opts{
		Provider: r.provider, Body: r.body, Session: r.session, Bypass: r.bypassed,
		Models: r.models, Window: r.window, CacheMode: h.opts.CacheMode,
		Mode: mode, Tracker: h.tracker,
		CacheUncompactedTail: h.opts.Async.CacheUncompactedTail,
	})
	added := time.Since(start)

	if mode == components.ModeAsync && !r.bypassed {
		// The savings this turn came from replaying an EARLIER turn's off-path work.
		// Attributing them is the only way deferred value stops looking invisible.
		if h.agg != nil && res.Run != nil && res.Run.Saved() > 0 {
			h.agg.RecordRealized(res.Run.Saved())
		}
		h.enqueueAsync(r, res)
	}
	if res.Body == nil {
		return r.body, added
	}
	return res.Body, added
}

// httpReqInfo is the per-request input both the inline pass and an off-path job need.
// Bundled because an off-path job outlives the *http.Request it came from and must
// therefore hold a copy of everything, never a pointer into request-scoped state.
type httpReqInfo struct {
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

// jobKey is the dedup key: one useful job per (session, generation). A second turn
// arriving at the same generation coalesces onto the queued job instead of adding a
// second one, and once that job commits the generation advances so the next turn
// enqueues fresh work against the longer transcript.
func jobKey(session string, gen uint64) string {
	return session + "@" + strconv.FormatUint(gen, 10)
}

// enqueueAsync queues the expensive compaction for a session, to benefit later turns.
// The result is written into a store.Buffer and committed ONLY if the session is still
// at the generation the job was built from — the stale-result guard. Committing a stale
// result would replace content the provider has already cached against a newer turn's
// prefix, which is the failure mode async exists to avoid.
func (h *Handler) enqueueAsync(r *httpReqInfo, inline apply.Result) {
	if h.pool == nil {
		return
	}
	// Copies: the job runs after the response is written, so nothing may alias
	// request-scoped memory. The context, likewise, must be the pool's, not the
	// request's — the request's is cancelled the moment the handler returns.
	body := append([]byte(nil), r.body...)
	info := *r
	info.body = body

	// The inline pass already resolved the session id (a content hash when the host
	// supplied none), so reuse it: the dedup key and the generation check must use the
	// SAME id apply used, and recomputing invites the two to drift.
	sess, gen, prevLen := inline.Session, inline.Generation, inline.PrevLen
	if sess == "" {
		return // the pipeline never ran (no messages array) — nothing to defer
	}
	key := jobKey(sess, gen)

	h.pool.Enqueue(key, func(ctx context.Context) {
		start := time.Now()
		buf := store.NewBuffer(h.store)
		info.ctx = ctx
		res := apply.BodyOpts(ctx, h.pipe, buf, apply.Opts{
			Provider: info.provider, Body: info.body, Session: info.session,
			Models: info.models, Window: info.window, CacheMode: h.opts.CacheMode,
			// Deferred: this run's BODY is thrown away — only the frozen decisions it
			// writes into the buffer matter, and those are what later turns replay. So it
			// gets the model clients the inline async pass withheld.
			Mode: components.ModeAsync, Deferred: true,
			// PrevLen instead of a Tracker: an off-path run must reuse the boundary its
			// own turn was built with, and must not advance it — the boundary belongs to
			// real turns. By now the tracker has moved past this turn, so re-resolving
			// would gate the run against a boundary its body never had.
			PrevLen: &prevLen,
		})
		committed := false
		if res.Changed && buf.Writes() > 0 {
			committed = h.tracker.CommitIfCurrent(sess, gen, buf.Commit)
			if !committed {
				h.pool.RecordStale()
				slog.Debug("context-guru: discarded stale async compaction",
					"session", sess, "generation", gen)
			}
		}
		if h.agg != nil {
			h.agg.RecordDeferred(float64(time.Since(start).Microseconds())/1000.0, committed)
		}
	})
}

// enqueueObserve runs the pipeline off-path on a COPY of the request, against a store
// Buffer that is never committed, and records the result into the hypothetical metric
// namespace. Two independent reasons the enforced request cannot be affected: it was
// already forwarded from the untouched original, and every state write this run makes
// is thrown away with the buffer.
func (h *Handler) enqueueObserve(r *httpReqInfo) {
	if h.pool == nil {
		return
	}
	body := append([]byte(nil), r.body...)
	info := *r
	info.body = body
	// A plain counter, not (session, generation): an observe run never commits, so its
	// generation never advances, and keying on it would dedup every turn after the first
	// out of existence. The counter is also why observe needs no session resolve on the
	// request path — one more thing the enforced path does not pay for.
	key := "observe:" + strconv.FormatUint(h.observeSeq.Add(1), 10)

	h.pool.Enqueue(key, func(ctx context.Context) {
		apply.BodyOpts(ctx, h.pipe, store.NewBuffer(h.store), apply.Opts{
			Provider: info.provider, Body: info.body, Session: info.session,
			Models: info.models, Window: info.window, CacheMode: h.opts.CacheMode,
			Mode: components.ModeObserve,
			// The Tracker, so the projection is measured under the SAME cached-prefix
			// boundary an enforcing mode would use. Without it the boundary is unknown,
			// MaxCachedIdx is -1, the tail gate never fires, and every message in the
			// transcript looks compactable — which inflates the projection against what
			// sync actually achieves. Measured on SWE-bench: 9.5% projected against 0.8%
			// enforced, because 50 candidates passed the gate instead of 5.
			//
			// Safe off-path despite jobs finishing out of order: prevLen only ever grows,
			// so a late job for a shorter turn cannot move the boundary backwards. And
			// observe never commits, so the generation stays put and nothing reads it.
			Tracker: h.tracker,
		})
		// The pipeline already emitted mode-stamped reports through the emitter; the
		// Aggregator routes anything stamped observe into the potential_* namespace. No
		// separate recording call here, which is what keeps the two namespaces from
		// drifting apart.
	})
}
