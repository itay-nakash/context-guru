// Package modes implements context-guru's three operating modes (#31): the
// per-session compaction generation that makes an async result safe to apply or
// safe to throw away, and the bounded worker pool that computes those results off
// the request path.
//
// Why a generation at all. In async mode the expensive compaction runs after the
// request has already been forwarded, so its output lands in a session's frozen
// state at some later, unpredictable moment. Between enqueue and commit the agent
// may have taken another turn, and another job may have committed. Applying a
// result computed from a snapshot that no longer describes the session is how a
// compaction proxy corrupts a cached prefix. So every job records the generation it
// was built from, and a result whose generation is no longer current is DISCARDED —
// lost savings, never lost correctness.
//
// The generation advances only when a compaction actually LANDS. That is what makes
// the scheme non-starving: dedup on (session, generation) keeps at most one useful
// job in flight per session, a commit moves the session to the next generation, and
// the following turn enqueues a fresh job against the newer, longer transcript.
package modes

import "sync"

// Tracker holds the per-session state the modes need, each session's fields guarded
// by one lock so concurrent turns of a session cannot interleave a read and a write.
//
// Session lifetime is the bound, not an explicit end-of-session call: there is no
// session-end signal on this wire (an agent simply stops sending), so the tracker
// evicts under its own cap and a forgotten session restarts at generation 0 — correct,
// just missing the pending job's savings.
//
// It also owns prevLen — the number of normalized messages the previous turn carried,
// which is the already-cached/uncached boundary. That used to live in the TTL store
// and was read then written back in a `defer`, so two concurrent turns of one session
// raced on it (the hazard #31 calls out, overlapping with #25). Reading and writing it
// under the same lock, in one call, removes the race.
type Tracker struct {
	mu  sync.Mutex
	m   map[string]*sessState
	max int // bound on tracked sessions; 0 => default
}

type sessState struct {
	gen     uint64
	prevLen int
}

// defaultMaxSessions bounds the tracker so an unbounded stream of distinct sessions
// cannot grow it without limit. Matches the store's sticky-set bound.
const defaultMaxSessions = 1000

// NewTracker returns an empty tracker. maxSessions <= 0 uses the default bound.
func NewTracker(maxSessions int) *Tracker {
	if maxSessions <= 0 {
		maxSessions = defaultMaxSessions
	}
	return &Tracker{m: map[string]*sessState{}, max: maxSessions}
}

// get returns the session's state, creating it under the caller-held lock.
func (t *Tracker) get(session string) *sessState {
	s := t.m[session]
	if s == nil {
		if len(t.m) >= t.max {
			// ponytail: arbitrary eviction, same policy as the store's sticky sets.
			// A dropped session just re-starts at generation 0 — correct, less saving.
			for k := range t.m {
				delete(t.m, k)
				break
			}
		}
		s = &sessState{}
		t.m[session] = s
	}
	return s
}

// Turn records that this session's current turn carries n normalized messages and
// returns the snapshot the request must be built from: the PREVIOUS turn's length
// (the cached-prefix boundary) and the current compaction generation. Atomic, so two
// concurrent turns of one session each get a consistent pair and the second's write
// cannot be lost to the first's deferred write-back.
//
// prevLen only ever grows: an agent that re-sends a shorter transcript (a rewind, or
// a second, smaller request under the same session id) must not shrink the boundary,
// or content the provider already cached would fall back into the mutable tail.
func (t *Tracker) Turn(session string, n int) (prevLen int, gen uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.get(session)
	prevLen, gen = s.prevLen, s.gen
	if n > s.prevLen {
		s.prevLen = n
	}
	return prevLen, gen
}

// Gen returns the session's current compaction generation.
func (t *Tracker) Gen(session string) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.get(session).gen
}

// CommitIfCurrent runs commit and advances the generation IF the session is still at
// gen — the stale-result guard. commit is called while the session's lock is held, so
// a concurrent job for the same session cannot also observe gen as current and commit
// on top of it. Returns false when the result was stale and therefore discarded.
func (t *Tracker) CommitIfCurrent(session string, gen uint64, commit func()) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.get(session)
	if s.gen != gen {
		return false
	}
	if commit != nil {
		commit()
	}
	s.gen++
	return true
}

// Sessions reports how many sessions are tracked (test/telemetry aid).
func (t *Tracker) Sessions() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.m)
}
