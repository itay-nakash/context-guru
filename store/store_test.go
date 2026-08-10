package store

import (
	"testing"
	"time"
)

func TestMemoryPutGetMiss(t *testing.T) {
	m := NewMemory(Options{})
	m.Put("k", []byte("v"))
	if got, ok := m.Get("k"); !ok || string(got) != "v" {
		t.Fatalf("Get=%q ok=%v", got, ok)
	}
	if _, ok := m.Get("absent"); ok {
		t.Fatal("absent key must miss")
	}
}

func TestMemoryTTLExpiry(t *testing.T) {
	now := time.Unix(0, 0)
	m := NewMemory(Options{TTLSeconds: 10})
	m.now = func() time.Time { return now }
	m.Put("k", []byte("v"))
	now = now.Add(11 * time.Second)
	if _, ok := m.Get("k"); ok {
		t.Fatal("entry past its TTL must be gone")
	}
}

func TestMemoryPutUpdatesInPlace(t *testing.T) {
	m := NewMemory(Options{})
	m.Put("k", []byte("old"))
	m.Put("k", []byte("new"))
	if got, _ := m.Get("k"); string(got) != "new" {
		t.Fatalf("Put should overwrite, got %q", got)
	}
}

func TestMemoryLRUEviction(t *testing.T) {
	m := NewMemory(Options{MaxEntries: 2})
	m.Put("a", []byte("1"))
	m.Put("b", []byte("2"))
	m.Get("a")              // touch a -> b becomes the oldest
	m.Put("c", []byte("3")) // over capacity -> evicts b
	if _, ok := m.Get("b"); ok {
		t.Fatal("LRU should have evicted the least-recently-used entry (b)")
	}
	if _, ok := m.Get("a"); !ok {
		t.Fatal("recently touched entry (a) must survive")
	}
	if _, ok := m.Get("c"); !ok {
		t.Fatal("newest entry (c) must be present")
	}
}

func TestStickyBoundedAndCopied(t *testing.T) {
	m := NewMemory(Options{MaxSessions: 2})
	m.MarkSticky("s1", "a")
	m.MarkSticky("s2", "b")
	m.MarkSticky("s3", "c") // exceeds maxStick=2 -> an arbitrary session is dropped
	if len(m.sticky) > 2 {
		t.Fatalf("sticky sessions must stay bounded, got %d", len(m.sticky))
	}
	if _, ok := m.Sticky("s3")["c"]; !ok {
		t.Fatal("the newest sticky session must be present")
	}
	// Sticky returns a copy: mutating it must not affect the store.
	got := m.Sticky("s3")
	got["x"] = struct{}{}
	if _, leaked := m.Sticky("s3")["x"]; leaked {
		t.Fatal("Sticky must return a defensive copy")
	}
}

// A frozen decision that is still being replayed every turn must not die of old age:
// the TTL reclaims state for FINISHED sessions, and expiring a live decision flips an
// already-cached message's representation (a suffix cache-write at 11.5x the read price).
func TestMemorySlidingTTLOnGet(t *testing.T) {
	now := time.Unix(0, 0)
	m := NewMemory(Options{TTLSeconds: 10})
	m.now = func() time.Time { return now }
	m.Put("k", []byte("v"))
	// Read just before each expiry, 100 times = 900s ≫ the 10s TTL.
	for i := 0; i < 100; i++ {
		now = now.Add(9 * time.Second)
		if _, ok := m.Get("k"); !ok {
			t.Fatalf("a continuously-read entry expired at iteration %d", i)
		}
	}
	// ... but stopping the reads still expires it (the TTL is not disabled).
	now = now.Add(11 * time.Second)
	if _, ok := m.Get("k"); ok {
		t.Fatal("an unread entry must still expire")
	}
}

// The sliding refresh must not keep an entry nobody reads alive just because OTHER
// entries are being read.
func TestMemoryUnreadEntryStillExpires(t *testing.T) {
	now := time.Unix(0, 0)
	m := NewMemory(Options{TTLSeconds: 10})
	m.now = func() time.Time { return now }
	m.Put("hot", []byte("1"))
	m.Put("cold", []byte("2"))
	for i := 0; i < 5; i++ {
		now = now.Add(9 * time.Second)
		m.Get("hot")
	}
	if _, ok := m.Get("cold"); ok {
		t.Fatal("an entry that was never read must expire on its original deadline")
	}
	if _, ok := m.Get("hot"); !ok {
		t.Fatal("the continuously-read entry must survive")
	}
}

// The default TTL must outlast a long-horizon agent task (TB averaged 1975s, up to 4h)
// — the old 1800s default expired live frozen decisions mid-task.
func TestDefaultTTLCoversLongTask(t *testing.T) {
	if DefaultTTL < 2*time.Hour {
		t.Fatalf("default TTL %v is too short for a long-horizon task", DefaultTTL)
	}
	m := NewMemory(Options{})
	if m.ttl != DefaultTTL {
		t.Fatalf("zero TTLSeconds should yield DefaultTTL, got %v", m.ttl)
	}
	if m2 := NewMemory(Options{TTLSeconds: 42}); m2.ttl != 42*time.Second {
		t.Fatalf("ttl_seconds must stay configurable, got %v", m2.ttl)
	}
}

// Frozen decisions are pinned against LRU eviction: losing one is not a cache miss but
// a cache-DESTRUCTIVE event. Ordinary entries still evict normally.
func TestFrozenEntriesExemptFromLRU(t *testing.T) {
	m := NewMemory(Options{MaxEntries: 4}) // pin cap = max/2 = 2
	m.Put(FrozenPrefix+"s:mask:aaa", []byte("frozen"))
	for i := 0; i < 20; i++ {
		m.Put(string(rune('a'+i)), []byte("x"))
	}
	if _, ok := m.Get(FrozenPrefix + "s:mask:aaa"); !ok {
		t.Fatal("a frozen decision must survive LRU pressure")
	}
	if m.ll.Len() > 4 {
		t.Fatalf("cache still has to respect the entry cap, len=%d", m.ll.Len())
	}
}

// The eviction exemption must be capped so a pathological session cannot pin the whole
// cache and starve the rewind stashes the expand loop needs.
func TestFrozenPinCapped(t *testing.T) {
	m := NewMemory(Options{MaxEntries: 10}) // pin cap = 5
	for i := 0; i < 20; i++ {
		m.Put(FrozenPrefix+"s:mask:"+string(rune('a'+i)), []byte("f"))
	}
	if m.pinnedN > 5 {
		t.Fatalf("pinned entries %d exceed the max/2 cap", m.pinnedN)
	}
	if m.ll.Len() > 10 {
		t.Fatalf("entry cap breached, len=%d", m.ll.Len())
	}
	// Beyond the cap the decisions are evictable — but their loss stays VISIBLE, which
	// is the whole point of the signal.
	if dropped, _ := m.FrozenLossStats(); dropped == 0 {
		t.Fatal("frozen decisions dropped past the pin cap must be counted")
	}
}

// A dropped frozen decision must be distinguishable from one that never existed: the two
// call for opposite behavior (re-derive at depth vs obey the tail gate).
func TestFrozenLostIsDistinguishable(t *testing.T) {
	now := time.Unix(0, 0)
	m := NewMemory(Options{TTLSeconds: 10})
	m.now = func() time.Time { return now }
	k := FrozenPrefix + "s:mask:aaa"
	if m.FrozenLost(k) {
		t.Fatal("a key that was never frozen must not report as lost")
	}
	m.Put(k, []byte("masked"))
	now = now.Add(11 * time.Second)
	if _, ok := m.Get(k); ok {
		t.Fatal("expired entry must miss")
	}
	if !m.FrozenLost(k) {
		t.Fatal("an EXPIRED frozen decision must report as lost, not as never-frozen")
	}
	dropped, repaired := m.FrozenLossStats()
	if dropped != 1 || repaired != 0 {
		t.Fatalf("want dropped=1 repaired=0, got %d/%d", dropped, repaired)
	}
	// Re-freezing the same key repairs it: no flip reached the provider.
	m.Put(k, []byte("masked"))
	if m.FrozenLost(k) {
		t.Fatal("a re-frozen decision is no longer lost")
	}
	if dropped, repaired = m.FrozenLossStats(); dropped != 1 || repaired != 1 {
		t.Fatalf("want dropped=1 repaired=1, got %d/%d", dropped, repaired)
	}
}

// repaired must never exceed dropped. At the pin cap a decision is recorded lost and
// stays unprotected, so it must NOT also be scored as repaired — otherwise frozen_flips
// reads 0 while messages are in fact flipping, and the metric lies in the safe direction.
func TestPinCapDoesNotFakeRepair(t *testing.T) {
	m := NewMemory(Options{MaxEntries: 4}) // pin cap = 2
	for i := 0; i < 6; i++ {
		m.Put(FrozenPrefix+"s:mask:"+string(rune('a'+i)), []byte("f"))
	}
	dropped, repaired := m.FrozenLossStats()
	if dropped == 0 {
		t.Fatal("over-cap frozen decisions must be counted as dropped")
	}
	if repaired != 0 {
		t.Fatalf("nothing was re-frozen, so repaired must be 0, got %d", repaired)
	}
	// Re-freezing a key that WAS lost counts exactly one repair. (The first two keys are
	// pinned and were never lost, so pick one the cap actually pushed out.)
	var lost string
	for k := range m.lostFrozen {
		lost = k
		break
	}
	m.Put(lost, []byte("f2"))
	d2, r2 := m.FrozenLossStats()
	if r2 != 1 {
		t.Fatalf("re-freezing a lost decision must count one repair, got %d", r2)
	}
	if r2 > d2 {
		t.Fatalf("repaired (%d) must never exceed dropped (%d)", r2, d2)
	}
}

// A replay decision that is dropped while UNPINNED (it missed the pin cap) must still be
// reported lost. Gating loss detection on the pin flag would let exactly the most
// at-risk entries vanish silently — unreported, and therefore never repaired.
func TestUnpinnedFrozenLossIsStillReported(t *testing.T) {
	now := time.Unix(0, 0)
	m := NewMemory(Options{TTLSeconds: 10, MaxEntries: 2}) // pin cap = 1
	m.SetClock(func() time.Time { return now })
	pinned := FrozenPrefix + "s:mask:pinned"
	overCap := FrozenPrefix + "s:mask:overcap"
	m.Put(pinned, []byte("a"))
	m.Put(overCap, []byte("b")) // past the cap -> unpinned
	now = now.Add(11 * time.Second)
	m.Get(overCap) // expires it
	if !m.FrozenLost(overCap) {
		t.Fatal("an unpinned frozen decision that expired must still report as lost")
	}
}

// Pinned entries must not be immortal. The TTL is only enforced in Get, and a dead
// session's decisions are never read again, so without expiry-aware eviction pinnedN
// ratchets to max/2 and stays there: half the cache leaks AND pinning silently stops
// working for every later session.
func TestExpiredPinnedEntriesAreReclaimed(t *testing.T) {
	now := time.Unix(0, 0)
	m := NewMemory(Options{TTLSeconds: 10, MaxEntries: 20}) // pin cap = 10
	m.SetClock(func() time.Time { return now })
	for i := 0; i < 10; i++ { // fill every pin slot from a "dead" session
		m.Put(FrozenPrefix+"dead:mask:"+string(rune('a'+i)), []byte("f"))
	}
	if m.pinnedN != 10 {
		t.Fatalf("expected 10 pinned, got %d", m.pinnedN)
	}
	now = now.Add(11 * time.Second) // the dead session's decisions are all past their TTL
	for i := 0; i < 30; i++ {       // a new session's traffic drives eviction
		m.Put("rewind"+string(rune('a'+i)), []byte("payload"))
	}
	if m.pinnedN >= 10 {
		t.Fatalf("expired pinned entries must be reclaimed, pinnedN=%d", m.pinnedN)
	}
	if m.ll.Len() > 20 {
		t.Fatalf("entry cap breached, len=%d", m.ll.Len())
	}
	// A fresh session can pin again, because slots actually freed.
	m.Put(FrozenPrefix+"live:mask:x", []byte("f"))
	if el := m.items[FrozenPrefix+"live:mask:x"]; el == nil || !el.Value.(*entry).pinned {
		t.Fatal("a new session must be able to pin after old decisions expired")
	}
}

// cg:len: is apply's prev-turn message count — the MaxCachedIdx boundary. Losing it makes
// TailOnly return true for EVERY index (fail-open, mutating the cached prefix), so it must
// be pinned too. It is 2-4 bytes.
func TestLenTrackerIsPinned(t *testing.T) {
	m := NewMemory(Options{MaxEntries: 20})
	m.Put(LenPrefix+"sess", []byte("42"))
	for i := 0; i < 10; i++ {
		m.Put(FrozenPrefix+"s:mask:"+string(rune('a'+i)), []byte("f"))
	}
	for i := 0; i < 40; i++ {
		m.Put("rewind"+string(rune('a'+i)), []byte("payload"))
	}
	if got, ok := m.Get(LenPrefix + "sess"); !ok || string(got) != "42" {
		t.Fatal("the cache-boundary tracker must survive eviction pressure (else TailOnly fails open)")
	}
}

// An entry that is present and readable is not "dropped". Counting it at write time (when
// it merely missed the pin cap) inflated the drop count with live entries and made the
// next ordinary re-freeze look like a repair — flips reading 0 while nothing was wrong.
func TestOverCapEntryIsNotCountedAsDropped(t *testing.T) {
	m := NewMemory(Options{MaxEntries: 4}) // pin cap = 2
	for i := 0; i < 6; i++ {
		m.Put(FrozenPrefix+"s:mask:"+string(rune('a'+i)), []byte("f"))
	}
	dropped, repaired := m.FrozenLossStats()
	// Whatever was evicted is a real drop; nothing was re-frozen, so repaired must be 0.
	if repaired != 0 {
		t.Fatalf("no key was re-frozen, so repaired must be 0, got %d (dropped=%d)", repaired, dropped)
	}
	// Re-freezing the same over-cap key twice must not manufacture a drop/repair pair.
	before, _ := m.FrozenLossStats()
	k := FrozenPrefix + "s:mask:f"
	m.Put(k, []byte("f2"))
	m.Put(k, []byte("f3"))
	after, rep2 := m.FrozenLossStats()
	if after != before || rep2 != 0 {
		t.Fatalf("re-freezing a LIVE over-cap key must not count drops/repairs: %d->%d rep=%d",
			before, after, rep2)
	}
}

// The loss marks are one shared budget, so eviction must not let a busy session delete
// another session's fresh mark — that session's next turn would see a plain miss and flip
// its message unrepaired. Oldest-first keeps the newest marks.
func TestLossMarkEvictionKeepsNewest(t *testing.T) {
	now := time.Unix(0, 0)
	m := NewMemory(Options{TTLSeconds: 10, MaxEntries: 2})
	m.SetClock(func() time.Time { return now })
	old := FrozenPrefix + "A:mask:old"
	m.Put(old, []byte("f"))
	now = now.Add(11 * time.Second)
	m.Get(old) // expire -> mark A's loss
	if !m.FrozenLost(old) {
		t.Fatal("A's loss must be marked")
	}
	// Session B churns enough losses to overflow the mark budget (max=2).
	for i := 0; i < 5; i++ {
		k := FrozenPrefix + "B:mask:" + string(rune('a'+i))
		m.Put(k, []byte("f"))
		now = now.Add(11 * time.Second)
		m.Get(k)
	}
	newest := FrozenPrefix + "B:mask:e"
	if !m.FrozenLost(newest) {
		t.Fatal("the NEWEST loss mark must survive the budget (oldest is evicted first)")
	}
}
