package tokens

import "testing"

func TestCountIsBPENotCharQuarter(t *testing.T) {
	// "hello world" is 2 BPE tokens in cl100k/o200k, not len/4 == 2..3 by luck;
	// use a case where chars/4 is clearly wrong: repeated punctuation.
	got := Count("!!!!!!!!!!!!!!!!")
	if got == len("!!!!!!!!!!!!!!!!")/4 {
		t.Fatalf("Count still looks like chars/4: %d", got)
	}
	if got <= 0 {
		t.Fatalf("Count returned %d", got)
	}
}

func TestCountEmpty(t *testing.T) {
	if Count("") != 0 {
		t.Fatal("empty must be 0")
	}
}

func TestCountStableAcrossCalls(t *testing.T) {
	a, b := Count("the quick brown fox"), Count("the quick brown fox")
	if a != b {
		t.Fatalf("non-deterministic: %d vs %d", a, b)
	}
}
