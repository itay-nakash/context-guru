package extract

import "testing"

// TestContainmentCharSubsequence: deletion-only now allows trimming words /
// sentences / parts WITHIN a line (not just whole lines), while still rejecting
// any reorder, rewrite, or fabricated character.
func TestContainmentCharSubsequence(t *testing.T) {
	orig := "test_col_insert FAILED [ 61%]\nIndexError: out of range at common.py:86"
	accept := []string{
		"test_col_insert FAILED\nIndexError: out of range at common.py:86", // dropped " [ 61%]" within a line
		"test_col_insert FAILED [ 61%]",                                    // dropped a whole line
		"IndexError: common.py:86",                                         // dropped words within a line
		"",                                                                 // dropping everything is allowed
	}
	for _, a := range accept {
		if !IsContained(a, orig) {
			t.Errorf("should accept deletion: %q", a)
		}
	}
	reject := []string{
		"IndexError\ntest_col_insert FAILED", // reordered
		"test_col_insert PASSED",             // fabricated (PASSED not in orig in order)
		"IndexError: out of RANGE",           // reworded (uppercase RANGE not present)
		"col_insert failed and index is bad", // rewritten prose
	}
	for _, r := range reject {
		if IsContained(r, orig) {
			t.Errorf("should reject non-deletion: %q", r)
		}
	}
}

// TestContainmentLineSubsequence: a text reduction that drops whole lines (logs,
// code, tracebacks) is a valid lossless projection — it must pass containment
// even though it is NOT a contiguous substring of the original.
func TestContainmentLineSubsequence(t *testing.T) {
	orig := "line one\nERROR: boom in widget.c\nnoise\nmore noise\nline five"
	keep := "ERROR: boom in widget.c\nline five" // in-order subset of whole lines

	if !IsContained(keep, orig) {
		t.Fatal("in-order whole-line subset must be contained")
	}
	// Contiguous substring still works (single kept region).
	if !IsContained("noise\nmore noise", orig) {
		t.Fatal("contiguous substring must be contained")
	}
	// Reordered lines are NOT a subsequence → rejected.
	if IsContained("line five\nERROR: boom in widget.c", orig) {
		t.Fatal("reordered lines must be rejected")
	}
	// An edited line (not byte-identical) is rejected.
	if IsContained("ERROR: boom in widget.cpp", orig) {
		t.Fatal("an altered line must be rejected")
	}
	// Fabricated line absent from the original is rejected.
	if IsContained("totally invented line", orig) {
		t.Fatal("a fabricated line must be rejected")
	}
}
