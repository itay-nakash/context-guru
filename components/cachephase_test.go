package components

import (
	"testing"
	"time"
)

// The phase classification, row by row, with the phase NAME pinned rather than just a boolean.
//
// The name matters because two of these rows are the ones a boolean cannot tell apart, and the two
// callers treat them oppositely: Cold means "there is provably no prefix to disturb", Unknown means
// "the cache-aware path did not run and nothing is known". A predicate that collapsed them would
// look correct to extract_llm_sweep (which fires on neither) and be wrong for summarize (which
// fires on Unknown and must not fire on Cold-as-Unknown).
func TestCachePhaseClassifiesEachCacheState(t *testing.T) {
	const ttl = 5 * 60 * 1000
	ms := func(d time.Duration) int64 { return d.Milliseconds() }

	cases := []struct {
		name string
		c    *Ctx
		want CachePhase
	}{
		{"no Ctx at all", nil, CachePhaseUnknown},
		{"the cache-aware path did not run, so both figures are zero", &Ctx{}, CachePhaseUnknown},
		{"a TTL with no previous turn on record is unknown, not warm",
			&Ctx{CacheTTLMs: ttl, IdleMs: -1}, CachePhaseUnknown},
		// ZERO IDLE IS A FACT, NOT A GAP, and reading it as one was a live defect. Two turns of one
		// session arriving in the same millisecond — which an agent issuing parallel sub-requests
		// produces routinely — used to classify Unknown, and the compaction gate PERMITS Unknown.
		// A run observed the gate opening over a live 8-message cached prefix on 13 of 26 turns
		// this way. Zero idle is the warmest cache there can be.
		{"zero idle is the warmest possible cache, not unknown",
			&Ctx{CacheTTLMs: ttl, IdleMs: 0}, CachePhaseWarm},
		{"idle time with no TTL is unknown, not cold",
			&Ctx{IdleMs: ms(9 * time.Minute)}, CachePhaseUnknown},
		{"a backwards clock stays unknown rather than inventing zero idle",
			&Ctx{CacheTTLMs: ttl, IdleMs: -5_000}, CachePhaseUnknown},
		{"plenty of lifetime left is warm",
			&Ctx{CacheTTLMs: ttl, IdleMs: ms(30 * time.Second)}, CachePhaseWarm},
		{"just outside the window is still warm",
			&Ctx{CacheTTLMs: ttl, IdleMs: ms(3*time.Minute + 59*time.Second)}, CachePhaseWarm},
		{"exactly one window's width left is pre-expiry",
			&Ctx{CacheTTLMs: ttl, IdleMs: ms(4 * time.Minute)}, CachePhasePreExpiry},
		{"a few seconds left is pre-expiry",
			&Ctx{CacheTTLMs: ttl, IdleMs: ms(4*time.Minute + 55*time.Second)}, CachePhasePreExpiry},
		// AT NOMINAL EXPIRY THIS CLASSIFIER SAYS COLD, which is the right answer for the reader
		// that needs a LIVE entry: extract_llm_sweep's prefix ask must stand down as soon as the
		// entry may be gone. The COMPACTION gate needs the opposite caution and therefore a
		// different threshold — see TestTheCompactionDeadZoneAtNominalExpiry.
		{"exactly at expiry is cold, not warm",
			&Ctx{CacheTTLMs: ttl, IdleMs: ttl}, CachePhaseCold},
		{"past expiry is cold",
			&Ctx{CacheTTLMs: ttl, IdleMs: ms(6 * time.Minute)}, CachePhaseCold},
		{"apply already called it cold, so it is cold whatever the arithmetic says",
			&Ctx{CacheTTLMs: ttl, IdleMs: ms(30 * time.Second), ColdCache: true}, CachePhaseCold},
		{"an hour-tier prefix an hour idle is cold",
			&Ctx{CacheTTLMs: ms(time.Hour), IdleMs: ms(61 * time.Minute)}, CachePhaseCold},
		{"an hour-tier prefix with a minute left is pre-expiry",
			&Ctx{CacheTTLMs: ms(time.Hour), IdleMs: ms(59*time.Minute + 30*time.Second)}, CachePhasePreExpiry},
		{"an hour-tier prefix five minutes idle is warm, where a 5m prefix would be cold",
			&Ctx{CacheTTLMs: ms(time.Hour), IdleMs: ms(5 * time.Minute)}, CachePhaseWarm},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.CachePhase(DefaultPreExpiry); got != tc.want {
				t.Errorf("CachePhase = %s, want %s", got, tc.want)
			}
		})
	}
}

// A wider configured window must move the boundary, or pre_expiry_seconds is inert — which is how
// a knob whose value nothing measures becomes a knob nothing can tune either.
func TestAWiderWindowMovesThePreExpiryBoundary(t *testing.T) {
	c := &Ctx{CacheTTLMs: 5 * 60 * 1000, IdleMs: (2 * time.Minute).Milliseconds()} // 3 minutes left
	if got := c.CachePhase(time.Minute); got != CachePhaseWarm {
		t.Fatalf("with a one-minute window, three minutes left must be %s, got %s", CachePhaseWarm, got)
	}
	if got := c.CachePhase(4 * time.Minute); got != CachePhasePreExpiry {
		t.Errorf("with a four-minute window, three minutes left must be %s, got %s", CachePhasePreExpiry, got)
	}
}

// THE ASYMMETRY, AS AN EXECUTABLE CLAIM. Unknown is permitted by a size-gated compactor and refused
// by the sweep, and both directions are load-bearing.
//
// Unknown is not rare: CacheTTLMs and IdleMs are both zero whenever the cache-aware path did not run
// — a non-Anthropic-family provider with no cache_control breakpoint, `cache_mode: off`, a bypassed
// turn, a session's first turn. Failing closed there would make summarize a DEAD component on those
// deployments while protecting nothing, because with cacheAware false MaxCachedIdx stays -1 and
// every offloader is already permitted to rewrite deep history. There is no live prefix whose
// invalidation the refusal would be avoiding.
//
// The sweep's own gate is tested against sweeping() in components/offload; what is asserted here is
// only that CacheAllows does not answer for it.
func TestCacheAllowsPermitsUnknownWhileTheExactPhaseTestDoesNot(t *testing.T) {
	unknown := (&Ctx{}).CachePhase(DefaultPreExpiry)
	if unknown != CachePhaseUnknown {
		t.Fatalf("fixture does not produce Unknown, it produces %s — the rest of this test is vacuous", unknown)
	}
	for _, state := range []string{CacheStatePreExpiry, CacheStateCold, CacheStatePreExpiryOrCold} {
		if !(Trigger{CacheState: state}).CacheAllows(nil, unknown) {
			t.Errorf("cache_state %q refused an UNKNOWN phase: on any deployment where the "+
				"cache-aware path does not run — a non-caching provider, cache_mode: off, a "+
				"bypassed turn, a first turn — this makes the component dead while protecting "+
				"nothing, because MaxCachedIdx is already -1 there", state)
		}
	}
	// The other half: the exact comparison the sweep uses must NOT be satisfied by Unknown, or the
	// sweep would ask a question against a prefix that may not exist and pay fresh for the whole
	// transcript.
	if unknown == CachePhasePreExpiry {
		t.Error("Unknown compares equal to PreExpiry, so the sweep's exact test would fire on a " +
			"deployment whose TTL could not be read")
	}
}

// The zero Trigger must stay permissive, in the new dimension as well as the old ones. Every config
// that never mentions cache_state depends on it, and a future refactor that made the zero value
// restrictive would silently disable three components at once.
func TestTheZeroTriggerPermitsEveryCachePhase(t *testing.T) {
	var zero Trigger
	for _, p := range []CachePhase{CachePhaseUnknown, CachePhaseWarm, CachePhasePreExpiry, CachePhaseCold} {
		if !zero.CacheAllows(nil, p) {
			t.Errorf("the zero Trigger refused phase %s; a Trigger that names no cache_state must "+
				"impose no cache constraint", p)
		}
	}
	if !zero.FracResolvable(nil) {
		t.Error("the zero Trigger declined for want of a resolvable window, but it configures no " +
			"fraction — the window's provenance cannot matter to it")
	}
}

// CacheRemaining is the shared arithmetic, and ok=false must mean unknown rather than zero: zero is
// a positive claim that the entry has expired, and a caller that conflated them would read every
// non-cache-aware turn as an expired prefix.
func TestCacheRemainingSeparatesUnknownFromExpired(t *testing.T) {
	if _, ok := (&Ctx{}).CacheRemaining(); ok {
		t.Error("a Ctx with no cache figures reported a known remaining lifetime")
	}
	d, ok := (&Ctx{CacheTTLMs: 300_000, IdleMs: 300_000}).CacheRemaining()
	if !ok || d != 0 {
		t.Errorf("an exactly-expired entry: got (%v, %v), want (0, true) — expiry is KNOWN, and "+
			"reporting it as unknown would hide it from every caller", d, ok)
	}
}

// PERMITTING COLD IS ONLY SOUND IF IT READS THE CLOCK RATHER THAN THE FLAG, and this is the test
// that says so.
//
// Ctx.ColdCache is a known false positive on a keep-alive'd session: proxy/keepalive.go never
// updates the turn tracker, so a session whose entry the keeper has been refreshing reads cold
// while the entry is alive (proxy/promexport.go:807, the −$708 mechanism). A default of
// pre_expiry_or_cold that trusted the flag would compact LIVE prefixes on exactly the sessions
// someone is paying pings to protect — turning the cheapest moment to compact into the most
// expensive one.
func TestColdIsAcceptedFromTheClockAndNotFromTheFlagAlone(t *testing.T) {
	const ttl = 5 * 60 * 1000
	for _, state := range []string{CacheStateCold, CacheStatePreExpiryOrCold} {
		// The keep-alive shape: the flag says cold, the clock says there is life left. This is
		// the case that must NOT fire.
		flagOnly := &Ctx{ColdCache: true, CacheTTLMs: ttl, IdleMs: 30 * 1000}
		if (Trigger{CacheState: state}).CacheAllows(flagOnly, flagOnly.CachePhase(time.Minute)) {
			t.Errorf("%s: permitted a session whose entry has %dms of life left because the flag "+
				"said cold; that compacts a live prefix on a keep-alive'd session",
				state, ttl-30*1000)
		}
		// Genuinely expired by the clock: this is the cheapest moment to compact, because the
		// turn is paying to create an entry either way.
		expired := &Ctx{ColdCache: true, CacheTTLMs: ttl, IdleMs: ttl + 60*1000}
		if !(Trigger{CacheState: state}).CacheAllows(expired, expired.CachePhase(time.Minute)) {
			t.Errorf("%s: refused a genuinely expired entry; that turn pays a write regardless, "+
				"so declining leaves the full-prefix rewrite on the table", state)
		}
		// Nothing known either way stays Unknown, which every state permits for its own
		// documented reasons — it must not be silently treated as cold.
		unknown := &Ctx{}
		if !(Trigger{CacheState: state}).CacheAllows(unknown, unknown.CachePhase(time.Minute)) {
			t.Errorf("%s: refused an Unknown phase; see CacheAllows on why Unknown permits", state)
		}
	}
}

// THE TWO COLD TESTS DIFFER ON PURPOSE, and the window between them is a dead zone where compaction
// declines. This is the case a review found missing.
//
// CachePhase answers "might this entry be gone?" — the sweep's question, where assuming it IS gone is
// the safe error. CertainlyColdByClock answers "is this entry definitely gone?" — the compactor's
// question, where assuming it might still be ALIVE is the safe error. At and just past nominal expiry
// those give different answers, and both are correct for their own caller.
//
// Before the fix, summarize's gate used the sweep's threshold: for a full minute of every session's
// expiry it permitted rewriting deep history while apply still called the same entry warm and the
// rest of the pipeline treated its prefix as live.
func TestTheCompactionDeadZoneAtNominalExpiry(t *testing.T) {
	const ttlMs = int64(5 * 60 * 1000)
	// Just past nominal expiry, inside the clock-skew allowance.
	c := &Ctx{CacheAware: true, MaxCachedIdx: -1, CacheTTLMs: ttlMs, IdleMs: ttlMs + 1_000}

	if got := c.CachePhase(DefaultPreExpiry); got != CachePhaseCold {
		t.Fatalf("precondition: CachePhase = %s, want %s — the sweep must stand down here",
			got, CachePhaseCold)
	}
	remaining, ok := c.CacheRemaining()
	if !ok {
		t.Fatal("precondition: the remaining lifetime must be known for this case to mean anything")
	}
	if CertainlyColdByClock(remaining) {
		t.Errorf("CertainlyColdByClock said the entry is GONE %v past nominal expiry, inside the "+
			"%v clock-skew allowance. apply still calls this entry warm, so a rewrite here "+
			"invalidates a prefix the rest of the pipeline is treating as live", -remaining, ColdMargin)
	}
	// So every cache_state that asks for cold declines, and the one that asks for pre-expiry does
	// too, because this turn is not PreExpiry either. That is the honest answer for a window where
	// we cannot tell whether the entry is alive or dead.
	for _, state := range []string{CacheStateCold, CacheStatePreExpiry, CacheStatePreExpiryOrCold} {
		tr := Trigger{CacheState: state}
		if tr.CacheAllows(c, c.CachePhase(tr.PreExpiry())) {
			t.Errorf("cache_state %q permitted compaction inside the skew allowance; the entry may "+
				"still be live and rewriting it costs a cache-write of the whole suffix", state)
		}
	}
	// And well past the allowance it IS cold, so the gate opens — or the dead zone would have
	// swallowed the case the component exists for.
	cold := &Ctx{CacheAware: true, MaxCachedIdx: -1, CacheTTLMs: ttlMs, IdleMs: ttlMs + ColdMargin.Milliseconds() + 1_000}
	tr := Trigger{CacheState: CacheStateCold}
	if !tr.CacheAllows(cold, cold.CachePhase(tr.PreExpiry())) {
		t.Error("cache_state cold declined an entry well past expiry AND past the skew allowance; " +
			"the dead zone must be one minute wide, not unbounded")
	}
}

// UNKNOWN OVER A LIVE PREFIX MUST BE REFUSED, and this is the case every existing guard in this file
// missed: they all set MaxCachedIdx: -1, which is the one value that makes Unknown safe.
//
// CachePhaseUnknown means "the cache-aware path could not tell us how much life this entry has left".
// It does NOT mean there is no entry. This package's own comment used to assert the stronger claim —
// that on Unknown deployments MaxCachedIdx stays -1, so there is no live prefix whose invalidation we
// would be avoiding — and two reachable shapes falsify it:
//
//   - apply's legacy no-Tracker path, which every library consumer of BodyFull/BodyOpts takes, reads
//     MaxCachedIdx from the store and never sets CacheTTLMs at all;
//   - `cache_mode: on` against a provider whose TTL this repo does not derive.
//
// A REVIEW OBSERVED THE GATE OPENING over a live 8-message cached prefix on 13 of 26 turns. And the
// consequence is session-persistent rather than confined to the turn: once a checkpoint exists the
// replay runs before and independently of this gate, so one wrongly-permitted turn rewrites the
// forwarded prefix for the rest of the session, warm turns included.
func TestUnknownIsRefusedWhenAPrefixIsLive(t *testing.T) {
	// No readable TTL, so the phase is Unknown — but MaxCachedIdx says eight messages are already
	// committed to the provider's cache.
	live := &Ctx{CacheAware: true, MaxCachedIdx: 8}
	if got := live.CachePhase(DefaultPreExpiry); got != CachePhaseUnknown {
		t.Fatalf("precondition: phase = %s, want %s — this case is only interesting on Unknown",
			got, CachePhaseUnknown)
	}
	for _, state := range []string{CacheStateCold, CacheStatePreExpiry, CacheStatePreExpiryOrCold} {
		tr := Trigger{CacheState: state}
		if tr.CacheAllows(live, CachePhaseUnknown) {
			t.Errorf("cache_state %q permitted compaction on an UNKNOWN phase with max_cached_idx=8. "+
				"There is a live prefix here and no evidence about its lifetime, so this rewrites "+
				"a cached prefix and pays a cache-write of the whole suffix", state)
		}
	}

	// THE COMPONENT MUST STILL WORK where Unknown genuinely means no prefix, or the guard has traded
	// one silent failure for another: declining everywhere would make summarize dead on every
	// deployment whose cache-aware path does not run.
	noPrefix := &Ctx{CacheAware: false, MaxCachedIdx: -1}
	for _, state := range []string{CacheStateCold, CacheStatePreExpiry, CacheStatePreExpiryOrCold} {
		tr := Trigger{CacheState: state}
		if !tr.CacheAllows(noPrefix, CachePhaseUnknown) {
			t.Errorf("cache_state %q refused an UNKNOWN phase with no cached prefix; there is "+
				"nothing to protect there and refusing disables the component on every "+
				"non-cache-aware deployment", state)
		}
	}

	// And a caller that never asked for a cache state is unaffected in both shapes: `""` and `any`
	// are the components that do not consult this gate at all.
	for _, state := range []string{"", CacheStateAny} {
		tr := Trigger{CacheState: state}
		if !tr.CacheAllows(live, CachePhaseUnknown) {
			t.Errorf("cache_state %q must impose no cache constraint, but it declined", state)
		}
	}
}

// The same defect through the DEPLOYED path's own arithmetic rather than through a hand-built Ctx:
// two turns of one session in the same millisecond. This is the shape a review reached live with four
// concurrent requests, and it is now Warm rather than Unknown — so the gate refuses it on the phase
// itself, before the MaxCachedIdx guard is even consulted.
func TestConcurrentTurnsAreWarmNotUnknown(t *testing.T) {
	const ttlMs = int64(5 * 60 * 1000)
	// idle == 0 is what apply now records when nowMs == prevAt.
	c := &Ctx{CacheAware: true, MaxCachedIdx: 8, CacheTTLMs: ttlMs, IdleMs: 0}
	if got := c.CachePhase(DefaultPreExpiry); got != CachePhaseWarm {
		t.Fatalf("phase = %s, want %s: a turn arriving in the same millisecond as the previous one "+
			"has the entry's whole lifetime ahead of it, which is the WARMEST state there is — "+
			"reading it as Unknown is what let the compaction gate open over a live prefix", got, CachePhaseWarm)
	}
	tr := Trigger{CacheState: CacheStatePreExpiryOrCold}
	if tr.CacheAllows(c, c.CachePhase(tr.PreExpiry())) {
		t.Error("the shipped cache_state permitted compaction on a prefix cached 0 ms ago")
	}
}
