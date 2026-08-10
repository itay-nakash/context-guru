// Package modes holds the per-session state context-guru's operating modes need.
//
// Today that is one thing: the cached-prefix boundary, i.e. how many normalized
// messages the previous turn of a session carried. Everything at or below it is already
// committed to the provider's cache, so supersession/age-based offloaders must confine
// their mutations to the tail above it (components.Ctx.MaxCachedIdx).
//
// It lives here rather than in the TTL store because it is turn accounting, not cached
// payload, and because reading it and recording the new value must be ONE atomic step.
// The previous implementation read it from the store and wrote it back in a `defer`, so
// two concurrent turns of one session raced: both read the same length, and the second's
// write-back could land before the first's, leaving the boundary describing neither turn.
// A boundary that is too high lets an offloader mutate content the provider has cached,
// which costs a full cache-write of the suffix.
package modes

import "sync"

// Tracker holds the per-session cached-prefix boundary, each session's state guarded by
// one lock so concurrent turns cannot interleave a read and a write.
type Tracker struct {
	mu  sync.Mutex
	m   map[string]int
	max int // bound on tracked sessions; 0 => default
}

// defaultMaxSessions bounds the tracker so an unbounded stream of distinct sessions
// cannot grow it without limit. Matches the store's sticky-set bound.
const defaultMaxSessions = 1000

// NewTracker returns an empty tracker. maxSessions <= 0 uses the default bound.
func NewTracker(maxSessions int) *Tracker {
	if maxSessions <= 0 {
		maxSessions = defaultMaxSessions
	}
	return &Tracker{m: map[string]int{}, max: maxSessions}
}

// Turn records that this session's current turn carries n normalized messages and
// returns the PREVIOUS turn's count — the cached-prefix boundary the request must be
// built against. Read and write happen under one lock, which is what removes the race
// described in the package comment.
//
// The boundary only ever grows: an agent that re-sends a shorter transcript (a rewind,
// or a smaller second request under the same session id) must not shrink it, or content
// the provider already cached would fall back into the mutable tail.
func (t *Tracker) Turn(session string, n int) (prevLen int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prevLen, ok := t.m[session]
	if !ok && len(t.m) >= t.max {
		// ponytail: arbitrary eviction, same policy as the store's sticky sets. A dropped
		// session restarts at 0, which means "treat everything as tail" — correct, just
		// less saving. Add an LRU only if session churn is shown to cost real savings.
		for k := range t.m {
			delete(t.m, k)
			break
		}
	}
	if n > prevLen {
		t.m[session] = n
	} else {
		t.m[session] = prevLen
	}
	return prevLen
}

// Sessions reports how many sessions are tracked (test/telemetry aid).
func (t *Tracker) Sessions() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.m)
}
