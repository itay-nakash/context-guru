package dsl

import (
	"strings"
	"testing"
)

// TestApplyStages covers strip_ansi + replace + truncate_lines_at in one pass.
func TestApplyStages(t *testing.T) {
	doc := `
schema_version: 1
filters:
  f:
    match: "."
    strip_ansi: true
    replace:
      - pattern: "secret=\\w+"
        replacement: "secret=REDACTED"
    truncate_lines_at: 20
`
	var r Registry
	if err := r.Load([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	c := r.Match("anything")
	if c == nil {
		t.Fatal("filter did not match")
	}
	out, _ := Apply(c, "\x1b[31msecret=abc123\x1b[0m and a good deal more text past twenty runes")
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("strip_ansi failed: %q", out)
	}
	if !strings.Contains(out, "secret=REDACTED") {
		t.Fatalf("replace failed: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if len([]rune(line)) > 20 {
			t.Fatalf("truncate_lines_at not applied: %q", line)
		}
	}
}

func TestHeadTailCombined(t *testing.T) {
	doc := "schema_version: 1\nfilters:\n  f:\n    match: .\n    head_lines: 1\n    tail_lines: 1\n"
	var r Registry
	if err := r.Load([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	out, loss := Apply(r.Match("x"), "h\nm1\nm2\nt")
	if !strings.Contains(out, "omitted") || loss != LossWhole {
		t.Fatalf("head+tail should drop the middle (LossWhole): %q loss=%d", out, loss)
	}
	out2, loss2 := Apply(r.Match("x"), "a\nb") // <= head+tail: untouched
	if loss2 != LossNone || strings.Contains(out2, "omitted") {
		t.Fatalf("small input must be untouched: %q loss=%d", out2, loss2)
	}
}

func TestTailOnly(t *testing.T) {
	doc := "schema_version: 1\nfilters:\n  f:\n    match: .\n    tail_lines: 2\n"
	var r Registry
	if err := r.Load([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	out, loss := Apply(r.Match("x"), "1\n2\n3\n4\n5")
	if !strings.HasPrefix(out, "... (3 lines omitted)") || !strings.HasSuffix(out, "4\n5") || loss != LossWhole {
		t.Fatalf("tail-only wrong: %q loss=%d", out, loss)
	}
}

func TestKeepLinesMatching(t *testing.T) {
	doc := "schema_version: 1\nfilters:\n  f:\n    match: .\n    keep_lines_matching: ['ERROR']\n"
	var r Registry
	if err := r.Load([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	out, _ := Apply(r.Match("x"), "info a\nERROR boom\ninfo b")
	if strings.TrimSpace(out) != "ERROR boom" {
		t.Fatalf("keep_lines_matching should retain only matching lines: %q", out)
	}
}

func TestLoadRejectsBadSchemaVersion(t *testing.T) {
	var r Registry
	if err := r.Load([]byte("schema_version: 2\nfilters: {}\n")); err == nil {
		t.Fatal("schema_version != 1 must be rejected")
	}
}
