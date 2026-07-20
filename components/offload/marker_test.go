package offload

import (
	"testing"

	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/store"
)

// A full (reversible) marker must degrade to an irreversible off-style drop when
// the store cannot persist the stash — otherwise it leaves an unresolvable
// <<cg:HASH>> marker in the request and silently loses the dropped content.
func TestMarkDegradesWhenStoreCannotPersist(t *testing.T) {
	rep := &components.Report{}
	c := &components.Ctx{Store: store.Nop{}}
	tok, key := mark(c, rep, markerFull, "original content", " [hint]")
	if tok != "" || key != "" {
		t.Fatalf("non-persisting store: want no marker/key, got tok=%q key=%q", tok, key)
	}
	if !rep.Irreversible {
		t.Fatal("degraded drop must set Irreversible so the pipeline keeps it (not reverted)")
	}

	// With a persisting store, full mode still stashes and emits a marker+key.
	rep2 := &components.Report{}
	c2 := &components.Ctx{Store: store.NewMemory(store.Options{})}
	tok2, key2 := mark(c2, rep2, markerFull, "original content", "")
	if tok2 == "" || key2 == "" {
		t.Fatalf("persisting store: want marker+key, got tok=%q key=%q", tok2, key2)
	}
	if got, ok := c2.Store.Get(key2); !ok || string(got) != "original content" {
		t.Fatalf("persisting store must retain the original, got %q ok=%v", got, ok)
	}
}
