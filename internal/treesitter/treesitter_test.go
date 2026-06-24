package treesitter

import "testing"

func TestLangForExt(t *testing.T) {
	cases := map[string]string{"a.go": "go", "b.py": "python", "c.ts": "typescript", "d.txt": ""}
	for path, want := range cases {
		if got := LangForExt(path); got != want {
			t.Fatalf("LangForExt(%q)=%q want %q", path, got, want)
		}
	}
}

func TestParseGo(t *testing.T) {
	tree, lang, ok := Parse("go", []byte("package p\nfunc Foo() int { return 1 }\n"))
	if !ok {
		t.Fatal("expected parse ok for valid Go")
	}
	defer tree.Close()
	if lang == nil || tree.RootNode().HasError() {
		t.Fatal("unexpected nil language or parse error")
	}
}

func TestParseUnknownLangFailsOpen(t *testing.T) {
	if _, _, ok := Parse("klingon", []byte("x")); ok {
		t.Fatal("unknown language must return ok=false")
	}
}
