package offload

import (
	"testing"

	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/store"
)

// A stash is keyed by a global content hash, so GET /expand must be scoped to the
// session that produced it — otherwise any caller could fetch another session's
// offloaded original by supplying its id (cross-session/tenant disclosure).
func TestOwnsKeyScopesBySession(t *testing.T) {
	st := store.NewMemory(store.Options{})
	cA := &components.Ctx{Session: "A", Store: st}
	recordOwner(cA, "hash123")

	if !OwnsKey(st, "A", "hash123") {
		t.Fatal("session A must own the key it stashed")
	}
	if OwnsKey(st, "B", "hash123") {
		t.Fatal("session B must NOT own session A's stash (IDOR)")
	}
	if OwnsKey(st, "", "hash123") {
		t.Fatal("an empty session must own nothing")
	}
	if OwnsKey(st, "A", "") {
		t.Fatal("an empty key must never be owned")
	}
}
