// Package store holds reversible reduction state: the content-addressed rewind
// store (original text keyed by content hash, so a collapsed/reduced block can be
// expanded back), the per-session eviction set, and session identity.
//
// The default Rewind implementation is in-memory — correct for a single proxy or
// sidecar process. A Redis/SQLite-backed implementation can satisfy the same
// interface later for multi-replica recovery (the plan's deferred option).
// Ported from winnow's rewind.py / session.py.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// ContentID is the deterministic id for a piece of content (first 24 hex chars of
// its SHA-256). Exported because item ids elsewhere use the same scheme.
func ContentID(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:24]
}

// Rewind stores originals so reductions are reversible.
type Rewind interface {
	// Put stores original and returns its content id.
	Put(original string) string
	// Get returns the original for id, or ("", false) if absent/expired.
	Get(id string) (string, bool)
}

type entry struct {
	original  string
	createdAt time.Time
}

// Memory is an in-memory Rewind with TTL and touch-on-sight (a marker still being
// recovered must not expire mid-session).
type Memory struct {
	mu  sync.Mutex
	m   map[string]entry
	ttl time.Duration
	now func() time.Time // injectable for tests
}

// NewMemory returns an in-memory store. ttl <= 0 means no expiry.
func NewMemory(ttl time.Duration) *Memory {
	return &Memory{m: map[string]entry{}, ttl: ttl, now: time.Now}
}

func (s *Memory) Put(original string) string {
	id := ContentID(original)
	s.mu.Lock()
	s.m[id] = entry{original: original, createdAt: s.now()}
	s.mu.Unlock()
	return id
}

func (s *Memory) Get(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[id]
	if !ok {
		return "", false
	}
	if s.ttl > 0 && s.now().Sub(e.createdAt) > s.ttl {
		delete(s.m, id)
		return "", false
	}
	e.createdAt = s.now() // touch-on-sight
	s.m[id] = e
	return e.original, true
}

// Eviction tracks user-directed prune targets per session.
type Eviction struct {
	mu   sync.Mutex
	sets map[string]map[string]struct{}
}

// NewEviction returns an empty eviction store.
func NewEviction() *Eviction { return &Eviction{sets: map[string]map[string]struct{}{}} }

func (e *Eviction) Evict(sid, target string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sets[sid] == nil {
		e.sets[sid] = map[string]struct{}{}
	}
	e.sets[sid][target] = struct{}{}
}

func (e *Eviction) IsEvicted(sid, target string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.sets[sid][target]
	return ok
}

// SessionID is a stable hash of (system, first user text).
func SessionID(system, firstUserText string) string {
	h := sha256.New()
	h.Write([]byte(system))
	h.Write([]byte{0})
	h.Write([]byte(firstUserText))
	return hex.EncodeToString(h.Sum(nil))[:16]
}
