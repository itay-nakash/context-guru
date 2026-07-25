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

func TestCountCacheHitMatchesFresh(t *testing.T) {
	// A cacheable (>= minCacheLen) string: first call encodes, second is a cache
	// hit. Both must agree with a direct encode of the same content.
	s := "the quick brown fox jumps over the lazy dog, repeatedly and verbosely"
	if len(s) < minCacheLen {
		t.Fatal("test string too short to exercise the cache")
	}
	first := Count(s)
	second := Count(s) // served from countMap
	if first != second {
		t.Fatalf("cache hit disagreed: %d vs %d", first, second)
	}
	cacheMu.Lock()
	_, cached := countMap[cacheHash(s)]
	cacheMu.Unlock()
	if !cached {
		t.Fatal("cacheable string was not memoized")
	}
}
