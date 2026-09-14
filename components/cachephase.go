package components

import "time"

// CachePhase names where a request sits in the life of the prompt-cache entry its prefix
// populated. It exists because two components now need the same fact and deriving it twice is
// how the cold decision and the dashboard came to disagree once already (see ttlTier in
// apply): one fact, one reader.
//
// THE PHASE THAT MATTERS IS PreExpiry, AND IT IS THE RESOLUTION OF A CONTRADICTION. A component
// that both ASKS the provider something over the cached transcript and REWRITES deep history
// wants opposite cache states:
//
//	the ASK needs a WARM cache — a prefix ask reads an entry that must still exist, or the call
//	pays fresh for the whole transcript, which is the cost the design exists to avoid;
//	the REWRITE wants a COLD cache — rewriting deep history invalidates a live prefix and forces
//	a cache-write of the whole suffix at 1.25x fresh.
//
// Both are cheap in the window where the entry still exists but has little life left: the ask
// still reads it, and what the rewrite invalidates is nearly worthless.
//
// THE TTL IS DERIVED, NOT ASSUMED. Ctx.CacheTTLMs is the same figure apply's cold decision uses,
// read out of the request itself: a bare `ephemeral` mark is 5 minutes, an explicit `ttl: "1h"`
// is an hour, widened to the longest lifetime this prefix has ever asked for. Zero means the
// cache-aware path did not run, which is Unknown — NOT Warm and not Cold.
//
// WHAT CALLERS DO WITH Unknown IS THEIR POLICY, NOT THIS FUNCTION'S, and the two shipped
// callers answer it oppositely on purpose. extract_llm_sweep must not fire on Unknown: it would
// invalidate live prefixes on exactly the deployments whose TTL could not be read. A size-gated
// compactor must fire on Unknown, because Unknown is not rare or exotic: CacheTTLMs is zero
// whenever the cache-aware path is off entirely (a non-Anthropic-family provider with no
// cache_control breakpoint, `cache_mode: off`, a bypassed turn), and declining there would
// disable the component on those deployments.
//
// UNKNOWN DOES NOT IMPLY THERE IS NO LIVE PREFIX, and this comment used to claim it did — that
// "on those deployments MaxCachedIdx stays -1 so every offloader is already permitted to rewrite
// deep history". The implication is false, and a live run reached the gap: a request can carry
// MaxCachedIdx >= 0 with no readable TTL, in which case there IS a cached prefix and Unknown means
// only that we cannot say how much life is left in it. Two reachable shapes —
//
//   - apply's legacy no-Tracker path (every library consumer of BodyFull/BodyOpts) sets
//     MaxCachedIdx from the store and never sets CacheTTLMs at all;
//   - `cache_mode: on` against a provider whose TTL this repo does not derive.
//
// A third, the same-millisecond concurrent turn, was the one observed live; that one is now fixed
// at its root by making IdleMs distinguish zero from unknown. The remaining two are answered by
// Trigger.CacheAllows, which refuses Unknown when MaxCachedIdx says a prefix exists.
type CachePhase int

const (
	// CachePhaseUnknown means the cache-aware path did not run, so nothing is known about a
	// cached prefix — not that there isn't one.
	CachePhaseUnknown CachePhase = iota
	// CachePhaseWarm means the entry is believed live with meaningful lifetime left.
	CachePhaseWarm
	// CachePhasePreExpiry means the entry is believed live but within the caller's window of
	// expiring, so invalidating it costs little.
	CachePhasePreExpiry
	// CachePhaseCold means the entry is believed gone, so there is no prefix to disturb.
	CachePhaseCold
)

// String names the phase, so a test failure and a log line say which one rather than an int.
func (p CachePhase) String() string {
	switch p {
	case CachePhaseWarm:
		return "warm"
	case CachePhasePreExpiry:
		return "pre_expiry"
	case CachePhaseCold:
		return "cold"
	default:
		return "unknown"
	}
}

// CacheRemaining is the cache entry's believed remaining lifetime: the TTL this request asked
// for minus this session's idle time. ok=false means unknown, and a caller must not treat that
// as zero — zero is a positive claim that the entry has expired.
//
// Exported separately from CachePhase because the arithmetic is one subtraction over two Ctx
// fields, and anything else that wants it (a dashboard row, a keep-alive deadline) must read it
// here rather than re-derive a TTL of its own.
func (c *Ctx) CacheRemaining() (time.Duration, bool) {
	// IdleMs < 0 is unknown; IdleMs == 0 is a positive claim of zero idle, and the entry then has
	// its whole TTL left. Treating zero as unknown reported "cannot tell" for the WARMEST possible
	// request, and the compaction gate permits Unknown — see Ctx.IdleMs.
	if c == nil || c.CacheTTLMs <= 0 || c.IdleMs < 0 {
		return 0, false
	}
	return time.Duration(c.CacheTTLMs-c.IdleMs) * time.Millisecond, true
}

// CachePhase classifies this request against preExpiry, the width of the window before the
// entry's believed expiry that the caller considers cheap to invalidate.
//
// ColdCache is checked FIRST: it is apply's own verdict over the same timestamps, and a turn apply
// has called cold is Cold here even if the arithmetic below would have said otherwise.
//
// It is NOT a safety check, and an earlier version of this comment implied it was ("one cheap
// agreement check costs nothing next to a wrongly invalidated prefix"). Checking ColdCache first can
// only make this classifier MORE willing to say cold, never less, so it cannot protect a live
// prefix. The reachable disagreement was the opposite one — the arithmetic calling an entry cold
// while apply called it warm — and that is answered by CertainlyColdByClock's margin, not by this
// line.
func (c *Ctx) CachePhase(preExpiry time.Duration) CachePhase {
	if c == nil {
		return CachePhaseUnknown
	}
	if c.ColdCache {
		return CachePhaseCold
	}
	remaining, ok := c.CacheRemaining()
	if !ok {
		return CachePhaseUnknown
	}
	if remaining <= 0 {
		return CachePhaseCold
	}
	if remaining <= preExpiry {
		return CachePhasePreExpiry
	}
	return CachePhaseWarm
}

// ColdMargin is the clock-skew allowance required PAST the believed expiry before this package will
// make the positive claim that an entry is GONE. It mirrors apply.coldMargin, which applies the same
// allowance to the same two timestamps when it computes Ctx.ColdCache.
//
// Duplicated as a constant rather than exported from apply because components must not import apply —
// apply imports components. The number is small, documented on both sides, and it exists for the same
// reason in both places: the gap between when a request was recorded here and when the provider last
// touched the entry, plus skew between this box's clock and the provider's.
const ColdMargin = time.Minute

// CertainlyColdByClock is the STRICT cold test: the entry's lifetime has run out by more than the
// clock-skew allowance, so rewriting its prefix destroys nothing.
//
// # Why this is not the same test as CachePhase == CachePhaseCold, deliberately
//
// The two readers of "is the cache cold" in this package answer DIFFERENT QUESTIONS, and the safe
// error runs in opposite directions for each. Collapsing them into one threshold makes one of the
// two callers less safe, which a test caught:
//
//   - extract_llm_sweep needs an entry that STILL EXISTS, because its prefix ask reads one. If it is
//     wrong about the entry being alive, the ask pays fresh for the whole transcript. So the safe
//     error is to assume the entry is already gone, and CachePhase calls nominal expiry Cold — which
//     makes the sweep stand down at exactly the right moment.
//   - summarize's gate needs an entry that is CERTAINLY GONE, because it rewrites deep history. If it
//     is wrong about the entry being dead, it invalidates a live prefix and pays a cache-write of the
//     whole suffix at 1.25x fresh. So the safe error is the opposite: assume the entry may still be
//     alive, and require the skew allowance before claiming otherwise.
//
// A REVIEW FOUND THE COMPACTION SIDE MISSING THIS. Trigger.coldByArithmetic required only
// `remaining <= 0` — the sweep's threshold — so for a full minute of every session's expiry it said
// cold and permitted a rewrite while apply still called the same entry warm and the rest of the
// pipeline treated its prefix as live. That converts the case summarize's design calls "strictly
// better, unconditionally" into the harmful one, on a window that recurs in every long session.
//
// The gap between the two thresholds is therefore a deliberate DEAD ZONE for compaction: an entry at
// or just past nominal expiry is neither PreExpiry (CachePhase calls it Cold) nor certainly cold
// (this returns false), so `cache_state: cold` and `pre_expiry_or_cold` both decline. That is the
// correct outcome for the one honest description of that window — we cannot tell whether this entry
// is alive or dead, so we must not rewrite it.
func CertainlyColdByClock(remaining time.Duration) bool { return remaining <= -ColdMargin }

// FillDenominator is what a fill FRACTION is a fraction OF: C, the point at which the conversation's
// own compaction mechanism acts, falling back to the model's context window when C is unknown.
//
// It exists as one function so that "90% full" cannot come to mean two different things in two
// callers — the failure this repo has had with cache TTLs and with token rulers, twice each.
//
// THE FALLBACK IS THE WINDOW AND THAT IS CORRECT FOR TWO OF THE FOUR CASES: a deployment where
// nothing compacts (a raw API client) genuinely has C == W, because the conversation grows until the
// provider rejects it. It is a guess for the case where a client compacts and we have not observed it
// — and those two are not currently distinguishable, which is why CompactionPointSource is carried
// rather than the number alone.
//
// A GUESSED C IS STILL ACTED ON, deliberately, where a guessed WINDOW is not (see FracResolvable).
// The asymmetry is about the direction of harm. A wrong window fires the gate at the wrong absolute
// size against a live cache: five times too early on the Opus family, invalidating prefixes that had
// most of their life left. A wrong C only moves the moment within the window — and the cache-state
// conjunct still has to agree, so the turn is one where the write was due anyway. Too-high a C means
// the component never fires (no harm, no benefit); too-low means it compacts a smaller prefix at a
// moment that was already cheap. Refusing to act on an assumed C would disable the component on every
// model whose client behaviour has not been measured, which today is all of them but haiku.
func (c *Ctx) FillDenominator() int {
	if c == nil {
		return 0
	}
	if c.CompactionPoint > 0 {
		return c.CompactionPoint
	}
	return c.CtxWindow
}
