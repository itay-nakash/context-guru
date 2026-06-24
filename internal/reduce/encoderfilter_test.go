package reduce

import (
	"strconv"
	"strings"
	"testing"
)

// TestBestEncodingHonorsAllowedSet verifies named encoder selection: when only "toon"
// is allowed, bestEncoding must return either a toon result or "" — never csv/tsv/jsonl,
// even on a flat uniform array where a delimited encoder would otherwise win on tokens.
func TestBestEncodingHonorsAllowedSet(t *testing.T) {
	// A wide flat uniform array: csv/tsv normally beat toon here by dropping repeated
	// keys entirely, so this fixture is exactly where a delimited encoder "would win".
	var recs []string
	for i := 0; i < 50; i++ {
		recs = append(recs, `{"id":`+strconv.Itoa(i)+`,"name":"item","status":"active","kind":"row"}`)
	}
	body := "[" + strings.Join(recs, ",") + "]"

	// Sanity: with no filter, a delimited encoder is allowed to win.
	encAll, _ := bestEncoding(body, nil)
	if encAll == "" {
		t.Fatal("unfiltered bestEncoding returned nothing on a flat array")
	}

	enc, name := bestEncoding(body, []string{"toon"})
	if enc == "" {
		// Allowed: toon may not beat the original; "" is a valid result.
		return
	}
	if name != "toon" {
		t.Fatalf("Encoders=[toon] but bestEncoding returned %q", name)
	}
	if name == "csv" || name == "tsv" || name == "jsonl" {
		t.Fatalf("disallowed encoder %q leaked through filter", name)
	}
}

// TestBestEncodingEmptyFilterPreservesBehavior: an empty/nil allowed set behaves
// exactly like before (no filtering).
func TestBestEncodingEmptyFilterPreservesBehavior(t *testing.T) {
	var recs []string
	for i := 0; i < 40; i++ {
		recs = append(recs, `{"id":`+strconv.Itoa(i)+`,"name":"item","status":"active"}`)
	}
	body := "[" + strings.Join(recs, ",") + "]"
	enc, _ := bestEncoding(body, nil)
	if enc == "" || len(enc) >= len(body) {
		t.Fatalf("empty-filter bestEncoding should still reduce: enc=%q", enc)
	}
}
