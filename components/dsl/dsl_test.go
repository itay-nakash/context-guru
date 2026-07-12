package dsl

import (
	"strings"
	"testing"
)

const makeFilter = `
schema_version: 1
filters:
  make:
    match: "^make"
    strip_lines_matching:
      - "^make\\[\\d+\\]:"
      - "^\\s*$"
    max_lines: 50
    on_empty: "make: ok"
tests:
  make:
    - name: strips entering/leaving
      input: |
        make[1]: Entering directory '/x'
        gcc -O2 foo.c
        make[1]: Leaving directory '/x'
      expected: |
        gcc -O2 foo.c
    - name: on_empty when all stripped
      input: |
        make[1]: Entering directory '/x'
        make[1]: Leaving directory '/x'
      expected: "make: ok"
`

func TestStripLinesAndOnEmpty(t *testing.T) {
	var r Registry
	if err := r.Load([]byte(makeFilter)); err != nil {
		t.Fatal(err)
	}
	c := r.Match("make test")
	if c == nil {
		t.Fatal("filter did not match selector")
	}
	out, _ := Apply(c, "make[1]: Entering directory '/x'\ngcc -O2 foo.c\nmake[1]: Leaving directory '/x'")
	if strings.TrimSpace(out) != "gcc -O2 foo.c" {
		t.Fatalf("strip_lines wrong: %q", out)
	}
	empty, _ := Apply(c, "make[1]: Entering directory '/x'\nmake[1]: Leaving directory '/x'")
	if empty != "make: ok" {
		t.Fatalf("on_empty wrong: %q", empty)
	}
}

func TestInlineTestsRun(t *testing.T) {
	fails, err := RunTests([]byte(makeFilter))
	if err != nil {
		t.Fatal(err)
	}
	if len(fails) != 0 {
		t.Fatalf("inline tests should pass, failures: %v", fails)
	}
}

func TestMatchOutputWithUnless(t *testing.T) {
	doc := `
schema_version: 1
filters:
  build:
    match: "build"
    match_output:
      - pattern: "BUILD SUCCESSFUL"
        message: "build: ok"
        unless: "WARNING"
`
	var r Registry
	if err := r.Load([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	c := r.Match("build")
	if out, loss := Apply(c, "compiling...\nBUILD SUCCESSFUL in 3s"); out != "build: ok" || loss != LossWhole {
		t.Fatalf("match_output should collapse to message: %q loss=%d", out, loss)
	}
	// unless guard: a warning present -> do NOT collapse
	if out, _ := Apply(c, "WARNING: deprecated\nBUILD SUCCESSFUL"); out == "build: ok" {
		t.Fatal("unless guard should have prevented collapse")
	}
}

func TestMaxLinesTailLossiness(t *testing.T) {
	doc := "schema_version: 1\nfilters:\n  log:\n    match: log\n    max_lines: 2\n"
	var r Registry
	if err := r.Load([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	out, loss := Apply(r.Match("log"), "a\nb\nc\nd")
	if !strings.Contains(out, "truncated") || loss != LossTail {
		t.Fatalf("max_lines should truncate with Tail loss: %q loss=%d", out, loss)
	}
}

func TestStripKeepMutuallyExclusive(t *testing.T) {
	doc := "schema_version: 1\nfilters:\n  x:\n    match: x\n    strip_lines_matching: ['a']\n    keep_lines_matching: ['b']\n"
	var r Registry
	if err := r.Load([]byte(doc)); err == nil {
		t.Fatal("expected compile error for strip+keep together")
	}
}
