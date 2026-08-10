package offload

import (
	"testing"

	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/store"
)

// REGRESSION (H1): the recurrence flag must be test-and-set, and must be read BEFORE this
// sighting writes it. It used to be marked right after the gate ALLOWED a call, so first
// sight reclassified itself as recurring and collected a 50% valuation bump (6 expected
// reuses vs 4) it had not earned — the gate over-firing, in the opposite direction from the
// two pessimistic priors fixed elsewhere in this change. The only recurrence test then in
// existence passed `seenBefore` directly and never touched the flag-setting path at all.
func TestMarkSeenContentIsTestAndSet(t *testing.T) {
	c := &components.Ctx{Store: store.NewMemory(store.Options{}), Session: "s1"}

	if markSeenContent(c, "ck-A") {
		t.Fatal("first sighting must report NOT seen before")
	}
	if !markSeenContent(c, "ck-A") {
		t.Fatal("second sighting must report seen before")
	}
	// Distinct content must not be contaminated by another key's flag.
	if markSeenContent(c, "ck-B") {
		t.Fatal("a different content key must report NOT seen before")
	}

	// Session-independent, like the result cache: recurrence is a property of the content.
	// This is what makes the cross-session reuse signal meaningful.
	other := &components.Ctx{Store: c.Store, Session: "completely-different-session"}
	if !markSeenContent(other, "ck-A") {
		t.Fatal("recurrence must be visible across sessions (shared store)")
	}
}

// The valuation bump recurrence earns must be real — otherwise H1 was harmless and the fix
// pointless. Pin the gap so a change to expectedReuses that flattens it is caught.
func TestRecurrenceChangesValuation(t *testing.T) {
	once := expectedReuses(false, 5)
	recurring := expectedReuses(true, 5)
	if recurring <= once {
		t.Fatalf("recurring content must expect more reuses: %v vs %v", recurring, once)
	}
	// The flag is therefore worth spending on only when genuinely earned — which is why it
	// must be set on observation, not on our own decision to call.
	if recurring/once < 1.2 {
		t.Fatalf("the recurrence bump (%v/%v) is too small to justify the flag", recurring, once)
	}
}
