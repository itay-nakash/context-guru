package extract

import (
	"context"
	"strings"
	"testing"
)

// A model program that reassigns the OUTPUT global (natural multi-step build) must
// still produce a result: Starlark forbids reassigning module globals, so execStarlark
// retries the program wrapped in a function (locals are reassignable). Regression for
// the "extract falls back to nothing on reducible outputs" bug.
func TestExecStarlarkOutputReassign(t *testing.T) {
	body := "keep me\nDROP a\nDROP b\nkeep me too"
	// two OUTPUT assignments — invalid at module scope, valid once wrapped
	src := "kept = [ln for ln in INPUT.split(\"\\n\") if \"DROP\" not in ln]\n" +
		"OUTPUT = \"\\n\".join(kept)\n" +
		"OUTPUT = re_sub(\"me too\", \"me\", OUTPUT)"
	out := execStarlark(context.Background(), body, src)
	if out == "" {
		t.Fatal("reassigned-OUTPUT program should be rescued by the function wrap, got empty")
	}
	if strings.Contains(out, "DROP") {
		t.Fatalf("filter should have dropped DROP lines: %q", out)
	}
}

// The wrapped-primary exec captures an optional SUMMARY global and defaults OUTPUT
// to INPUT / SUMMARY to "" when the program sets neither.
func TestExecStarlarkSummaryCaptured(t *testing.T) {
	body := "line a\nline b\nnoise\nline c"
	src := "kept = [ln for ln in INPUT.split(\"\\n\") if ln != \"noise\"]\n" +
		"OUTPUT = \"\\n\".join(kept)\n" +
		"SUMMARY = \"dropped 1 noise line\""
	out, sum := execStarlarkSummary(context.Background(), body, src)
	if strings.Contains(out, "noise") {
		t.Fatalf("noise line should be dropped: %q", out)
	}
	if sum != "dropped 1 noise line" {
		t.Fatalf("SUMMARY not captured: %q", sum)
	}
}

func TestExecStarlarkDefaultsNoOp(t *testing.T) {
	// A program that sets nothing leaves OUTPUT==INPUT (a clean miss) and SUMMARY empty.
	out, sum := execStarlarkSummary(context.Background(), "abc", "x = 1")
	if out != "abc" || sum != "" {
		t.Fatalf("defaults wrong: out=%q sum=%q", out, sum)
	}
}
