package dsl

import (
	"strings"
	"testing"
)

func load(t *testing.T, doc string) *Registry {
	t.Helper()
	var r Registry
	if err := r.Load([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	return &r
}

// truncate_lines_at used to run BEFORE `loss` was initialized, so a real intra-line
// cut reported LossNone and cmdfilter emitted no recovery hint for it.
func TestTruncateLinesAtReportsLoss(t *testing.T) {
	r := load(t, "schema_version: 1\nfilters:\n  f:\n    match: .\n    truncate_lines_at: 10\n")
	c := r.Match("x")

	out, loss := Apply(c, "short\n"+strings.Repeat("y", 40))
	if loss != LossWhole {
		t.Fatalf("intra-line cut must report LossWhole, got %d (out=%q)", loss, out)
	}
	// and no cut at all must stay lossless
	if _, loss := Apply(c, "short\nalso ok"); loss != LossNone {
		t.Fatalf("no cut must be LossNone, got %d", loss)
	}
}

// A silent mid-line cut reads as corrupted output to a model; the cut is marked,
// and the marker fits INSIDE the budget so the line never grows.
func TestTruncateLinesAtEllipsis(t *testing.T) {
	r := load(t, "schema_version: 1\nfilters:\n  f:\n    match: .\n    truncate_lines_at: 10\n")
	out, _ := Apply(r.Match("x"), strings.Repeat("y", 40))
	if want := strings.Repeat("y", 7) + "..."; out != want {
		t.Fatalf("want %q, got %q", want, out)
	}
	if len([]rune(out)) > 10 {
		t.Fatalf("truncation must respect the cap, got %d runes", len([]rune(out)))
	}
}

// An intra-line cut plus a line cap is not a clean tail drop — it must not be
// reported as the cheap-recovery case.
func TestTruncatePlusMaxLinesIsWhole(t *testing.T) {
	r := load(t, "schema_version: 1\nfilters:\n  f:\n    match: .\n    truncate_lines_at: 5\n    max_lines: 2\n")
	_, loss := Apply(r.Match("x"), strings.Repeat("abcdefgh\n", 8))
	if loss != LossWhole {
		t.Fatalf("truncate + max_lines must be LossWhole, got %d", loss)
	}
}

func TestCapClassResolvesToMaxLines(t *testing.T) {
	r := load(t, "schema_version: 1\nfilters:\n  f:\n    match: .\n    cap: warnings\n")
	out, loss := Apply(r.Match("x"), strings.Repeat("line\n", 30))
	if got := len(strings.Split(out, "\n")); got != Caps["warnings"]+1 { // +1 omission marker
		t.Fatalf("cap: warnings should cap at %d lines (+marker), got %d", Caps["warnings"], got)
	}
	if loss != LossTail {
		t.Fatalf("a clean cap is a tail drop, got %d", loss)
	}
}

func TestCapReduceAndValidation(t *testing.T) {
	r := load(t, "schema_version: 1\nfilters:\n  f:\n    match: .\n    cap: list\n    cap_reduce: 15\n")
	out, _ := Apply(r.Match("x"), strings.Repeat("line\n", 30))
	if got := len(strings.Split(out, "\n")); got != 6 { // 20-15=5, +1 marker
		t.Fatalf("cap_reduce wrong: got %d lines", got)
	}
	if ReducedCap(10, 20) != 10 || ReducedCap(10, 0) != 10 || ReducedCap(10, 4) != 6 {
		t.Fatal("ReducedCap must be underflow-safe and a no-op at by<=0")
	}
	var bad Registry
	if err := bad.Load([]byte("schema_version: 1\nfilters:\n  f:\n    match: .\n    cap: nope\n")); err == nil {
		t.Fatal("unknown cap class must be rejected at load")
	}
	var bad2 Registry
	if err := bad2.Load([]byte("schema_version: 1\nfilters:\n  f:\n    match: .\n    cap_reduce: 3\n")); err == nil {
		t.Fatal("cap_reduce without cap must be rejected at load")
	}
}

func TestDuplicateFilterNameRejected(t *testing.T) {
	doc := "schema_version: 1\nfilters:\n  f:\n    match: .\n"
	var r Registry
	if err := r.Load([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	if err := r.Load([]byte(doc)); err == nil {
		t.Fatal("a second filter with the same name must be rejected, not silently shadowed")
	}
}

func TestFailingInlineTestRejectedAtLoad(t *testing.T) {
	doc := `
schema_version: 1
filters:
  f:
    match: .
    strip_lines_matching: ['noise']
tests:
  f:
    - name: wrong-expectation
      input: "keep\nnoise\n"
      expected: "noise"
`
	var r Registry
	if err := r.Load([]byte(doc)); err == nil {
		t.Fatal("a filter whose own inline test fails must not load")
	}
}

func TestPriorityBeatsNameOrder(t *testing.T) {
	doc := `
schema_version: 1
filters:
  aaa-generic:
    match: '.'
    on_empty: generic
  zzz-specific:
    match: 'SPECIFIC'
    priority: 5
    on_empty: specific
`
	r := load(t, doc)
	if got := r.Match("SPECIFIC output"); got == nil || got.Name != "zzz-specific" {
		t.Fatalf("priority must beat name order, matched %v", got)
	}
	if got := r.Match("anything else"); got == nil || got.Name != "aaa-generic" {
		t.Fatalf("generic filter should still catch the rest, matched %v", got)
	}
}
