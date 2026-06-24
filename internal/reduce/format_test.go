package reduce

import (
	"strconv"
	"strings"
	"testing"
)

func itoaTest(n int) string { return strconv.Itoa(n) }

func TestBestEncodingConsidersTOON(t *testing.T) {
	// A flat uniform array is where TOON's tabular header shines; bestEncoding
	// must return a non-empty, strictly-smaller encoding. We don't hard-assert
	// the winning format name (TOON, tsv, or csv may win on token count).
	var recs []string
	for i := 0; i < 40; i++ {
		recs = append(recs, `{"id":`+itoaTest(i)+`,"name":"item","status":"active"}`)
	}
	body := "[" + strings.Join(recs, ",") + "]"
	enc, name := bestEncoding(body, nil)
	if enc == "" {
		t.Fatal("expected a smaller encoding")
	}
	if len(enc) >= len(body) {
		t.Fatal("encoding not smaller")
	}
	_ = name // may be toon, tsv, or csv depending on token counts; just assert it ran
}

func TestEncTOON(t *testing.T) {
	data := []any{
		map[string]any{"id": "1", "name": "a"},
		map[string]any{"id": "2", "name": "b"},
	}
	out, ok := encTOON(data)
	if !ok || out == "" {
		t.Fatalf("encTOON failed: ok=%v out=%q", ok, out)
	}
	if !strings.Contains(out, "name") {
		t.Fatalf("TOON output missing field: %q", out)
	}
}
