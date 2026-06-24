package reduce

import "testing"

func TestDefinedSymbolsCatchesGoMethod(t *testing.T) {
	src := "package p\ntype T struct{}\nfunc (r *T) DoThing() {}\nfunc Helper() {}\n"
	syms := definedSymbols(src, "x.go")
	for _, want := range []string{"DoThing", "Helper"} {
		if _, ok := syms[want]; !ok {
			t.Fatalf("missing symbol %q in %v", want, syms)
		}
	}
}

func TestDefinedSymbolsIgnoresComments(t *testing.T) {
	src := "package p\n// func Ghost() {}\nfunc Real() {}\n"
	syms := definedSymbols(src, "x.go")
	if _, ok := syms["Ghost"]; ok {
		t.Fatal("commented-out func must not be a defined symbol")
	}
}

func TestDefinedSymbolsNonCodeEmpty(t *testing.T) {
	if len(definedSymbols("hello", "notes.txt")) != 0 {
		t.Fatal("non-code path must yield no symbols")
	}
}
