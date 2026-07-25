package offload

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The deterministic reducer must drop ONLY obvious noise (repeated blocks, blank
// runs, progress bars) and keep every unique informative line — so it can never
// hide content the agent needs and force a redo.
func TestCollapseObviousNoise(t *testing.T) {
	// a traceback dumped 4× by a retry loop + blank runs + a progress bar
	block := "Traceback (most recent call last):\n  File \"x\", line 4\nModuleNotFoundError: No module named 'numpy'"
	in := block + "\n" + block + "\n" + block + "\n" + block +
		"\n\n\n\nkeep me unique line A\n 45%|████████  | 45/100\nkeep me unique line B"
	out, changed := collapseObviousNoise(in)
	if !changed {
		t.Fatal("expected noise to be collapsed")
	}
	// repeated block kept exactly once
	if n := strings.Count(out, "ModuleNotFoundError"); n != 1 {
		t.Fatalf("repeated block should collapse to 1 copy, got %d\n%s", n, out)
	}
	// unique lines preserved
	for _, want := range []string{"keep me unique line A", "keep me unique line B"} {
		if !strings.Contains(out, want) {
			t.Fatalf("unique line dropped: %q\n%s", want, out)
		}
	}
	// progress bar line removed
	if strings.Contains(out, "45%") {
		t.Fatalf("progress bar should be dropped\n%s", out)
	}
	// blank run collapsed (no triple newline remains)
	if strings.Contains(out, "\n\n\n") {
		t.Fatalf("blank run not collapsed\n%s", out)
	}
	if len(out) >= len(in) {
		t.Fatalf("output should be smaller: %d -> %d", len(in), len(out))
	}
}

// CRLF (\r\n) content must be preserved: the terminating CR is the line separator,
// not an in-line progress redraw. A regression once treated it as a redraw and blanked
// every CRLF line (keeping only the empty text after the trailing CR).
func TestStripTerminalNoisePreservesCRLF(t *testing.T) {
	in := "line one\r\nline two\r\nline three\r\n"
	out, _ := stripTerminalNoise(in)
	for _, want := range []string{"line one", "line two", "line three"} {
		if !strings.Contains(out, want) {
			t.Fatalf("CRLF content blanked — %q missing from %q", want, out)
		}
	}
}

// An INTERIOR carriage return IS a progress redraw: keep only the final rendered
// segment (and preserve the line's own trailing CR).
func TestStripTerminalNoiseCollapsesRedraw(t *testing.T) {
	in := "downloading: 10%\rdownloading: 55%\rdownloading: 100%\ndone\n"
	out, changed := stripTerminalNoise(in)
	if !changed {
		t.Fatal("interior-CR redraw should be collapsed")
	}
	if strings.Contains(out, "10%") || strings.Contains(out, "55%") {
		t.Fatalf("redraw should keep only the final segment: %q", out)
	}
	if !strings.Contains(out, "100%") || !strings.Contains(out, "done") {
		t.Fatalf("final segment + following lines must survive: %q", out)
	}
}

// headPeek must never split a multibyte rune and emit invalid UTF-8 into the marker.
func TestHeadPeekUTF8Safe(t *testing.T) {
	// many multibyte runes so the byte cut lands mid-rune at several offsets
	content := strings.Repeat("日本語テキスト絵文字😀 ", 50)
	for _, n := range []int{1, 3, 7, 16, 40, 96} {
		peek := headPeek(content, n)
		if !utf8.ValidString(peek) {
			t.Fatalf("headPeek(%d) produced invalid UTF-8: %q", n, peek)
		}
	}
}

// No obvious noise → returns the content unchanged (conservative: never touch
// unique content).
func TestCollapseObviousNoiseNoop(t *testing.T) {
	in := "line one\nline two\nline three\nresult: 42\nerror: none"
	out, changed := collapseObviousNoise(in)
	if changed || out != in {
		t.Fatalf("clean content must be left verbatim, changed=%v\n%s", changed, out)
	}
}
