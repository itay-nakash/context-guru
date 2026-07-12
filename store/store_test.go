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
