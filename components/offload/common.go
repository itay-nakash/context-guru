package offload

import (
	"strings"

	"github.com/kagenti/context-guru/schema"
	bschemas "github.com/maximhq/bifrost/core/schemas"
)

// errWords mark a tool output (or an item) as carrying a failure — such items
// are preserved by smartcrush and prioritized elsewhere, since dropping the one
// error in a haystack is exactly the accuracy loss to avoid.
var errWords = []string{"error", "fail", "exception", "panic", "fatal", "traceback"}

func hasError(s string) bool {
	ls := strings.ToLower(s)
	for _, w := range errWords {
		if strings.Contains(ls, w) {
			return true
		}
	}
	return false
}

// lastUserText returns the text of the most recent user message — the query
// relevance is scored against (extract, phi_evict).
func lastUserText(req *bschemas.BifrostChatRequest) string {
	for i := len(req.Input) - 1; i >= 0; i-- {
		if req.Input[i].Role == bschemas.ChatMessageRoleUser {
			return schema.MessageText(req.Input[i])
		}
	}
	return ""
}

// keywords extracts lowercased content words (>3 chars) as a set — a cheap
// relevance signal without embeddings.
func keywords(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if len(f) > 3 {
			out[f] = struct{}{}
		}
	}
	return out
}

// overlap is the fraction of query terms present in text (0..1).
func overlap(query map[string]struct{}, text string) float64 {
	if len(query) == 0 {
		return 0
	}
	tk := keywords(text)
	hit := 0
	for w := range query {
		if _, ok := tk[w]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(query))
}

// toolIndices returns the indices of tool-role messages, in order.
func toolIndices(req *bschemas.BifrostChatRequest) []int {
	var out []int
	for i := range req.Input {
		if req.Input[i].Role == bschemas.ChatMessageRoleTool {
			out = append(out, i)
		}
	}
	return out
}
