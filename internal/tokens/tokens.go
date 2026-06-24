// Package tokens estimates token counts. winnow uses tiktoken cl100k; this is a
// cheap, dependency-free proxy (~4 chars/token, the well-known approximation). It is
// only ever used for relative comparisons (never-inflate guards) and threshold gating,
// where an offline estimate is enough — the provider's usage is authoritative.
//
// ponytail: chars/4 heuristic; swap in a real BPE tokenizer (tiktoken-go) only if
// threshold fidelity is ever shown to matter.
package tokens

// Count returns an estimated token count for text.
func Count(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}
