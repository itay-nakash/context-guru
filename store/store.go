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
	"strings"
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

// FrozenLoser is an OPTIONAL Store capability: reporting that a frozen decision
// under key was dropped (TTL expiry / pin cap) rather than never taken. A bare Get
// miss cannot tell those apart, and they call for opposite behavior — "never frozen"
// means obey the tail gate, "was frozen, now lost" means re-derive the same bytes so
// the cached prefix does not flip. Stores that don't implement it degrade to the
// legacy indistinguishable behavior.
type FrozenLoser interface {
	// FrozenLost reports whether a frozen entry under key existed and was dropped.
	FrozenLost(key string) bool
}

// FrozenPrefix namespaces a component's FROZEN decision — the exact replacement
// bytes it must replay on every later turn to keep an already-cached message
// byte-identical (see components/offload/state.go). Entries under this prefix are
// PINNED: exempt from LRU eviction, because losing one is not a cache miss, it is a
// cache-DESTRUCTIVE event (the message flips representation inside the provider's
// cached prefix and the whole suffix is re-written at 11.5x the read price). They are
// small (a marker line), still honor the sliding TTL, and the exemption is capped at
// half the entry cap so a pathological session can never pin the whole cache.
const FrozenPrefix = "cg:frz:"

type entry struct {
	key     string
	payload []byte
	expires time.Time
	pinned  bool // exempt from LRU eviction (frozen decision); TTL still applies
}

// Memory is an in-memory Store: a TTL+LRU cache for rewind payloads plus a
// bounded per-session sticky-id set. The TTL is SLIDING (refreshed on Get), and
// the default (DefaultTTL) is sized past a long-horizon agent task rather than
// mirroring headroom's 1800s CCR store: a frozen compaction that dies mid-task is
// a cache-destructive event, not a saving.
type Memory struct {
	mu       sync.Mutex
	ttl      time.Duration
	max      int
	ll       *list.List               // LRU, front = most recent
	items    map[string]*list.Element // key -> element(*entry)
	sticky   map[string]map[string]struct{}
	maxStick int
	now      func() time.Time // injectable for tests
	pinnedN  int              // live pinned (frozen) entries, capped at max/2
	// lostFrozen remembers keys whose FROZEN entry was dropped anyway (TTL expiry, or
	// the pin cap). It is the "was frozen, now LOST" signal a caller cannot otherwise
	// distinguish from "never frozen" — see FrozenLost. Bounded like sticky.
	lostFrozen map[string]struct{}
	lostN      int64
	repairedN  int64
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

// DefaultTTL is the store's default (sliding) entry lifetime. Terminal-Bench tasks
// averaged 1975s of wall clock and run up to 4h, so the old 1800s default expired
// live frozen decisions mid-task; ~2.8h covers a long-horizon task's idle gaps
// (test suites, training runs) with the sliding refresh doing the rest.
const DefaultTTL = 10000 * time.Second

// NewMemory builds an in-memory store. Zero/negative option fields fall back to
// defaults (DefaultTTL, 1000 entries, 100 sessions of sticky sets).
func NewMemory(o Options) *Memory {
	ttl := time.Duration(o.TTLSeconds) * time.Second
	if o.TTLSeconds <= 0 {
		ttl = DefaultTTL
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
		sticky:     map[string]map[string]struct{}{},
		lostFrozen: map[string]struct{}{},
		now:        time.Now,
	}
}

func (*Memory) Persists() bool { return true }

// SetClock replaces the store's time source. For TESTS only — TTL behavior over a
// multi-hour agent session is not testable in real time, and the freeze lifetime is
// exactly what this store gets wrong when it's wrong.
func (m *Memory) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

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
	// Pin frozen decisions, but never more than half the cache: past that the marginal
	// pin protects one message while starving the rewind stashes the expand loop needs.
	if strings.HasPrefix(key, FrozenPrefix) {
		if m.pinnedN < m.max/2 {
			e.pinned = true
			m.pinnedN++
		} else {
			m.noteLost(key) // pin cap reached: this decision is evictable, and losing it is visible
		}
	}
	if _, wasLost := m.lostFrozen[key]; wasLost {
		delete(m.lostFrozen, key) // re-frozen: the dropped decision was repaired
		m.repairedN++
	}
	m.items[key] = m.ll.PushFront(e)
	for m.ll.Len() > m.max {
		if !m.evictOldest() {
			break // everything left is pinned
		}
	}
}

// noteLost records that a frozen decision under key is gone, so a later Get miss is
// distinguishable from "never frozen". Bounded by the entry cap.
func (m *Memory) noteLost(key string) {
	if len(m.lostFrozen) >= m.max {
		for k := range m.lostFrozen {
			delete(m.lostFrozen, k)
			break
		}
	}
	m.lostFrozen[key] = struct{}{}
	m.lostN++
}

// FrozenLost reports whether a frozen entry under key existed and was dropped (TTL
// expiry or the pin cap) — the "was frozen, now lost" signal. See FrozenLoser.
func (m *Memory) FrozenLost(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.lostFrozen[key]
	return ok
}

// FrozenLossStats returns how many frozen decisions this store has DROPPED since start
// (TTL expiry / pin cap) and how many of those were later re-Put — repaired to the same
// bytes, so no representation flip reached the provider. dropped−repaired is the count of
// flips that actually cost a suffix cache-write. Both count each key once, however many
// turns observe it.
func (m *Memory) FrozenLossStats() (dropped, repaired int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lostN, m.repairedN
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
	// Sliding TTL: an entry still being read is still live. The TTL exists to reclaim
	// state for FINISHED sessions, not to kill a decision an ongoing session replays
	// every turn — expiring a frozen compaction mid-task flips an already-cached
	// message's representation and forces the provider to re-write the whole suffix
	// (one cache-write costs 11.5 cache-reads). Recency and lifetime refresh together.
	e.expires = m.now().Add(m.ttl)
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

// evictOldest drops the least-recently-used UNPINNED entry, walking back over pinned
// (frozen) ones. Reports false when nothing is evictable.
func (m *Memory) evictOldest() bool {
	for el := m.ll.Back(); el != nil; el = el.Prev() {
		if !el.Value.(*entry).pinned {
			m.remove(el)
			return true
		}
	}
	return false
}

func (m *Memory) remove(el *list.Element) {
	e := el.Value.(*entry)
	if e.pinned {
		m.pinnedN--
		m.noteLost(e.key) // a frozen decision is disappearing — make it detectable
	}
	m.ll.Remove(el)
	delete(m.items, e.key)
}
