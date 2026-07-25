// Package tokens estimates token counts using a real BPE tokenizer (o200k_base,
// the modern GPT family encoding) — an accurate offline proxy. The provider's
// usage remains authoritative; this drives reduction gating and never-inflate guards.
package tokens

import (
	"hash/maphash"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

var (
	encOnce sync.Once
	enc     tokenizer.Codec
)

func codec() tokenizer.Codec {
	encOnce.Do(func() {
		// o200k_base is embedded in the binary (pure-Go, offline, no CGO).
		enc, _ = tokenizer.Get(tokenizer.O200kBase)
	})
	return enc
}

// countCache memoizes BPE counts by a fast hash of the text. The pipeline
// re-tokenizes the whole conversation ~2N+2 times per request (once per component
// before/after, plus the run before/after) — the vast majority being messages no
// component touched. Caching by content hash lets those passes reuse a single
// encode per distinct string. Bounded: cleared wholesale past cap (agent transcripts
// churn, so a warm working set re-fills quickly and memory stays flat).
var (
	cacheMu   sync.Mutex
	cacheSeed = maphash.MakeSeed()
	countMap  = make(map[uint64]int, 4096)
)

const countCacheCap = 200_000

// minCacheLen skips memoizing tiny strings, where hashing costs about as much as
// encoding and the churn would just thrash the map.
const minCacheLen = 24

func cacheHash(text string) uint64 {
	var h maphash.Hash
	h.SetSeed(cacheSeed)
	h.WriteString(text)
	return h.Sum64()
}

// Count returns the BPE token count of text (0 for empty). Falls back to a
// chars/4 estimate only if the tokenizer failed to initialize. Results for
// non-trivial strings are memoized by content hash (see countCache).
func Count(text string) int {
	if text == "" {
		return 0
	}
	cacheable := len(text) >= minCacheLen
	var key uint64
	if cacheable {
		key = cacheHash(text)
		cacheMu.Lock()
		if n, ok := countMap[key]; ok {
			cacheMu.Unlock()
			return n
		}
		cacheMu.Unlock()
	}
	c := codec()
	var n int
	if c == nil {
		n = (len(text) + 3) / 4
	} else if ids, _, err := c.Encode(text); err != nil {
		n = (len(text) + 3) / 4
	} else {
		n = len(ids)
	}
	if cacheable {
		cacheMu.Lock()
		if len(countMap) >= countCacheCap {
			countMap = make(map[uint64]int, 4096)
		}
		countMap[key] = n
		cacheMu.Unlock()
	}
	return n
}
