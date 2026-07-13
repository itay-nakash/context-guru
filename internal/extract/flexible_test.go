package extract

import (
	"context"
	"strings"
	"testing"
)

// TestExecStarlarkRegex: a model-written filter can use the injected re_ helpers
// to trim within lines (surgical deletion), and the result is deletion-only so it
// passes containment.
func TestExecStarlarkRegex(t *testing.T) {
	body := "test_a PASSED [ 10%]\ntest_b FAILED [ 20%]\ntest_c PASSED [ 30%]"
	// Drop PASSED lines, then strip the trailing "[ NN%]" progress column.
	src := `lines = [ln for ln in INPUT.split("\n") if "PASSED" not in ln]
OUTPUT = re_sub(" \\[ *[0-9]+%\\]", "", "\n".join(lines))`
	out := execStarlark(context.Background(), body, src)
	if out != "test_b FAILED" {
		t.Fatalf("regex trim failed: %q", out)
	}
	if !IsContained(out, body) {
		t.Fatal("a pure deletion (drop lines + strip columns) must be contained")
	}
}

// TestExecStarlarkBadRegexFailsOpen: an invalid pattern errors the program, which
// yields "" (the caller falls back to deterministic).
func TestExecStarlarkBadRegexFailsOpen(t *testing.T) {
	if out := execStarlark(context.Background(), "abc", `OUTPUT = re_sub("(", "", INPUT)`); out != "" {
		t.Fatalf("bad regex must fail open (empty), got %q", out)
	}
}

// TestRewriteModeSkipsContainment: with Rewrite, a reworded (non-contained) result
// is accepted (sanity + smaller only); by default it is rejected.
func TestRewriteModeSkipsContainment(t *testing.T) {
	body := "the build failed because widget.c has an index error at line 86"
	reword := "widget.c: index error line 86" // NOT a subsequence (reordered/reworded)
	if IsContained(reword, body) {
		t.Skip("test premise: reword must not be a subsequence")
	}
	def := Cfg{}
	if validateExtraction(reword, body, nil, def) {
		t.Fatal("default (deletion-only) must reject a reworded result")
	}
	rw := Cfg{Rewrite: true}
	if !validateExtraction(reword, body, nil, rw) {
		t.Fatal("rewrite mode must accept a reworded (sane, smaller) result")
	}
	// Even in rewrite mode, a KEEP id that vanished still fails the sanity gate.
	if validateExtraction("nothing relevant", body, []string{"widget.c"}, rw) {
		t.Fatal("rewrite mode must still preserve KEEP identifiers (sanity gate)")
	}
}

// guard: the JSON example filter still works with the new predeclared set.
func TestExecStarlarkJSONStillWorks(t *testing.T) {
	body := `[{"path":"a.py","m":"keep"},{"path":"b.py","m":"drop"}]`
	src := `data = json.decode(INPUT)
OUTPUT = json.encode([r for r in data if "keep" in r["m"]])`
	out := execStarlark(context.Background(), body, src)
	if strings.Contains(out, "drop") || !strings.Contains(out, "keep") {
		t.Fatalf("json filter regressed: %q", out)
	}
}
