package session

import "testing"

func TestResolveExplicitWins(t *testing.T) {
	if got := Resolve("  explicit-id  ", "sys", "user"); got != "explicit-id" {
		t.Fatalf("a non-empty explicit id should win (trimmed), got %q", got)
	}
}

func TestResolveHashStableAndScoped(t *testing.T) {
	a := Resolve("", "sys", "u1")
	b := Resolve("", "sys", "u1")
	c := Resolve("", "sys", "u2")
	if a != b {
		t.Fatal("identical (system, firstUser) must hash to the same session")
	}
	if a == c {
		t.Fatal("a different first user must produce a different session")
	}
	if len(a) != 16 {
		t.Fatalf("session hash should be 16 hex chars, got %q", a)
	}
}
