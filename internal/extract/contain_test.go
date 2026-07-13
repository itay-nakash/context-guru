package extract

import "testing"

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
