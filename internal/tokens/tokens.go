// Package tokens estimates token counts using a real BPE tokenizer (o200k_base,
// the modern GPT family encoding) — an accurate offline proxy. The provider's
// usage remains authoritative; this drives reduction gating and never-inflate guards.
package tokens

import (
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

// Count returns the BPE token count of text (0 for empty). Falls back to a
// chars/4 estimate only if the tokenizer failed to initialize.
func Count(text string) int {
	if text == "" {
		return 0
	}
	c := codec()
	if c == nil {
		return (len(text) + 3) / 4
	}
	ids, _, err := c.Encode(text)
	if err != nil {
		return (len(text) + 3) / 4
	}
	return len(ids)
}
