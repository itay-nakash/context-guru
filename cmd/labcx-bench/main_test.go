package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kagenti/lab-context-engineering/config"
	"github.com/kagenti/lab-context-engineering/engine"
)

// findFixture returns the committed fixture by name (fails the test if absent).
func findFixture(t *testing.T, name string) fixture {
	t.Helper()
	for _, fx := range fixtureSet {
		if fx.name == name {
			return fx
		}
	}
	t.Fatalf("fixture %q not in fixtureSet", name)
	return fixture{}
}

// componentByName returns the settings constructor for a named component (fails if absent).
func componentByName(t *testing.T, name string) func() config.Settings {
	t.Helper()
	for _, c := range components() {
		if c.name == name {
			return c.settings
		}
	}
	t.Fatalf("component %q not defined", name)
	return nil
}

// readFixture loads a committed fixture from disk via the same resolver the harness uses.
func readFixture(t *testing.T, fx fixture) string {
	t.Helper()
	base, err := fixturesDir()
	if err != nil {
		t.Fatalf("fixturesDir: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(fx.relPath)))
	if err != nil {
		t.Fatalf("read %s: %v", fx.relPath, err)
	}
	return string(body)
}

// TestHarnessReducesAndRecovers runs the harness end to end on one committed fixture and
// asserts the core promise: a deterministic component reduces tokens > 0 AND the original
// is recoverable byte-for-byte from the rewind store (reversible).
func TestHarnessReducesAndRecovers(t *testing.T) {
	fx := findFixture(t, "runner_py")
	text := readFixture(t, fx)

	r := measure(fx, text, component{name: "skeleton", settings: componentByName(t, "skeleton")})

	if r.TokensBefore <= 0 {
		t.Fatalf("expected non-empty fixture, got tokens_before=%d", r.TokensBefore)
	}
	if r.TokensAfter >= r.TokensBefore {
		t.Fatalf("expected token reduction: tokens_after=%d not < tokens_before=%d",
			r.TokensAfter, r.TokensBefore)
	}
	if r.BytesAfter >= r.BytesBefore {
		t.Fatalf("expected byte reduction: bytes_after=%d not < bytes_before=%d",
			r.BytesAfter, r.BytesBefore)
	}
	if r.PctSaved <= 0 {
		t.Fatalf("expected pct_saved > 0, got %.2f", r.PctSaved)
	}
	if !r.Reversible {
		t.Fatalf("skeleton reduction must be reversible (recoverable via rewind store)")
	}
}

// TestCmdFilterReducesRealCommandOutput proves the command-output filter reduces a real
// large command output (rtk's cargo build) and stays reversible.
func TestCmdFilterReducesRealCommandOutput(t *testing.T) {
	fx := findFixture(t, "cargo_build")
	text := readFixture(t, fx)
	r := measure(fx, text, component{name: "cmdfilter", settings: componentByName(t, "cmdfilter")})
	if r.TokensAfter >= r.TokensBefore {
		t.Fatalf("cmdfilter should reduce cargo build noise: before=%d after=%d",
			r.TokensBefore, r.TokensAfter)
	}
	if !r.Reversible {
		t.Fatalf("cmdfilter reduction must be reversible")
	}
}

// TestEveryRowReversible runs the whole matrix and asserts no component ever produces an
// irreversible (unrecoverable lossy) result — fail-open and reversibility are the repo's
// hard invariants.
func TestEveryRowReversible(t *testing.T) {
	for _, r := range measureAll() {
		if !r.Reversible {
			t.Errorf("%s / %s: not reversible (tokens %d->%d)",
				r.Fixture, r.Component, r.TokensBefore, r.TokensAfter)
		}
	}
}

// TestFindMarkersExpandRoundTrip is a direct check that the engine's public recovery API
// (FindMarkers + Expand) round-trips an original through a reducing pass.
func TestFindMarkersExpandRoundTrip(t *testing.T) {
	fx := findFixture(t, "glab_mr_list")
	text := readFixture(t, fx)

	eng := engine.New(componentByName(t, "collapse")(), nil, nil)
	req := buildRequest(fx, text)
	before := toolResultText(req.Clone(), fx)
	out, _ := eng.Transform(context.Background(), req)
	after := toolResultText(out, fx)

	ids := engine.FindMarkers(after)
	if len(ids) == 0 {
		t.Fatalf("expected a recovery marker after collapse")
	}
	orig, ok := eng.Expand(ids[0])
	if !ok {
		t.Fatalf("Expand(%q) returned not-found", ids[0])
	}
	if !strings.Contains(before, orig) {
		t.Fatalf("recovered original does not match the pre-reduction text")
	}
}
