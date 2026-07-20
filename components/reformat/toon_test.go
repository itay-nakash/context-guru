package reformat

import (
	"strings"
	"testing"
)

func TestEncodeTOON_UniformArrayShrinks(t *testing.T) {
	in := `[{"id":1,"name":"Alice","role":"admin"},{"id":2,"name":"Bob","role":"user"}]`
	out, ok := encodeTOON(in)
	if !ok {
		t.Fatal("expected uniform scalar array to encode")
	}
	// Header lists count + sorted keys once; each row is comma-separated.
	if !strings.HasPrefix(out, "[2]{id,name,role}:\n") {
		t.Fatalf("unexpected header, got:\n%s", out)
	}
	for _, want := range []string{"1,Alice,admin", "2,Bob,user"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing row %q in:\n%s", want, out)
		}
	}
	if len(out) >= len(in) {
		t.Fatalf("TOON (%d) not smaller than JSON (%d)", len(out), len(in))
	}
}

func TestEncodeTOON_QuotesDelimiters(t *testing.T) {
	out, ok := encodeTOON(`[{"a":"x,y"},{"a":"he said \"hi\""}]`)
	if !ok {
		t.Fatal("expected encode")
	}
	if !strings.Contains(out, `"x,y"`) || !strings.Contains(out, `"he said ""hi"""`) {
		t.Fatalf("bad CSV quoting:\n%s", out)
	}
}

func TestEncodeTOON_SkipsNonTable(t *testing.T) {
	cases := map[string]string{
		"object":       `{"id":1}`,
		"empty array":  `[]`,
		"nested value": `[{"a":{"b":1}}]`,
		"ragged keys":  `[{"a":1},{"a":1,"b":2}]`,
		"not json":     `just some log output`,
	}
	for name, in := range cases {
		if _, ok := encodeTOON(in); ok {
			t.Errorf("%s: expected ok=false (leave untouched)", name)
		}
	}
}
