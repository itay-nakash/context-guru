// Package store holds context-guru's cross-call state behind one interface so
// both hosts (bifrost proxy, AuthBridge plugin) share it. v1 ships an in-memory
// TTL+LRU backend; SQLite/Redis slot in behind the same interface when a
// durable or multi-replica deployment is real (see the design doc, D5).
//
// It carries three things keyed by session:
//   - Rewind: cache_key -> original bytes, so Offload components are reversible
//     (the expand(id) tool loop resolves originals from here).
//   - Sticky: the set of content ids already reduced on prior turns, so a
//     component can keep its output byte-stable across turns (cache stability).
//   - per-session token/metric rollups (added with metrics in P0/P5).
package store

import (
	"container/list"
	"sync"
	"time"
)

// Store is the interface components and adapters depend on. Implementations
// must be safe for concurrent use — one instance serves all requests.
type Store interface {
	// Put stashes an original payload under key with the store's default TTL.
	Put(key string, payload []byte)
	// Get returns a stashed payload; ok=false if absent or expired.
	Get(key string) (payload []byte, ok bool)
	// Sticky returns the per-session set of already-reduced content ids.
	Sticky(session string) map[string]struct{}
	// MarkSticky records that id was reduced in this session.
	MarkSticky(session, id string)
	// Persists reports whether Put actually retains payloads. false (the Nop
	// store) means offloads cannot be made reversible, so a full marker_mode must
	// degrade to an irreversible drop rather than leave an unresolvable marker.
	Persists() bool
}

type entry struct {
	key     string
	payload []byte
	expires time.Time
}

// Memory is an in-memory Store: a TTL+LRU cache for rewind payloads plus a
// bounded per-session sticky-id set. Defaults mirror headroom's CCR store
// (1800s TTL, 1000 entries).
type Memory struct {
	mu       sync.Mutex
	ttl      time.Duration
	max      int
	ll       *list.List               // LRU, front = most recent
	items    map[string]*list.Element // key -> element(*entry)
	sticky   map[string]map[string]struct{}
	maxStick int
	now      func() time.Time // injectable for tests
}

// Options configures a Memory store; the zero value yields sane defaults.
// yaml tags let it drop straight into the config file's store: block.
type Options struct {
	// Enabled toggles the state store. nil/absent => on (backward-compatible).
	// false => no store: reversibility is off, so offload components must run
	// marker_mode: off (a full-marker offload would leave dangling markers).
	Enabled     *bool `yaml:"enabled"`
	TTLSeconds  int   `yaml:"ttl_seconds"`
	MaxEntries  int   `yaml:"max_entries"`
	MaxSessions int   `yaml:"max_sessions"`
}

// Nop is a Store that persists nothing: Put discards, Get/Sticky always miss.
// Used when the store is disabled — the expand loop resolves nothing and lossy
// offloads become irreversible, which is why they must use marker_mode: off.
type Nop struct{}

func (Nop) Put(string, []byte)                {}
func (Nop) Get(string) ([]byte, bool)         { return nil, false }
func (Nop) Sticky(string) map[string]struct{} { return nil }
func (Nop) MarkSticky(string, string)         {}
func (Nop) Persists() bool                    { return false }

// NewMemory builds an in-memory store. Zero/negative option fields fall back to
// defaults (1800s TTL, 1000 entries, 100 sessions of sticky sets).
func NewMemory(o Options) *Memory {
	ttl := time.Duration(o.TTLSeconds) * time.Second
	if o.TTLSeconds <= 0 {
		ttl = 1800 * time.Second
	}
	max := o.MaxEntries
	if max <= 0 {
		max = 1000
	}
	stick := o.MaxSessions
	if stick <= 0 {
		stick = 100
	}
	return &Memory{
		ttl: ttl, max: max, maxStick: stick,
		ll: list.New(), items: map[string]*list.Element{},
		sticky: map[string]map[string]struct{}{},
		now:    time.Now,
	}
}

func (*Memory) Persists() bool { return true }

func (m *Memory) Put(key string, payload []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.items[key]; ok {
		e := el.Value.(*entry)
		e.payload = payload
		e.expires = m.now().Add(m.ttl)
		m.ll.MoveToFront(el)
		return
	}
	e := &entry{key: key, payload: payload, expires: m.now().Add(m.ttl)}
	m.items[key] = m.ll.PushFront(e)
	for m.ll.Len() > m.max {
		m.evictOldest()
	}
}

func (m *Memory) Get(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.items[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*entry)
	if m.now().After(e.expires) {
		m.remove(el)
		return nil, false
	}
	m.ll.MoveToFront(el)
	return e.payload, true
}

func (m *Memory) Sticky(session string) map[string]struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.sticky[session]
	out := make(map[string]struct{}, len(src))
	for k := range src {
		out[k] = struct{}{}
	}
	return out
}

func (m *Memory) MarkSticky(session, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sticky[session]
	if s == nil {
		if len(m.sticky) >= m.maxStick {
			// drop an arbitrary session to stay bounded; ponytail: good enough
			// until a real eviction policy is warranted.
			for k := range m.sticky {
				delete(m.sticky, k)
				break
			}
		}
		s = map[string]struct{}{}
		m.sticky[session] = s
	}
	s[id] = struct{}{}
}

func (m *Memory) evictOldest() {
	if el := m.ll.Back(); el != nil {
		m.remove(el)
	}
}

func (m *Memory) remove(el *list.Element) {
	m.ll.Remove(el)
	delete(m.items, el.Value.(*entry).key)
}
