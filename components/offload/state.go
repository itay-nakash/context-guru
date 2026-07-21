package offload

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
)

// State reuse over the generic Store (key→bytes), session-scoped by key prefix
// so a prior compaction is reused across turns instead of re-calling the LLM.
// Reusing the previous output byte-for-byte also keeps the request prefix stable
// (KV-cache friendly) — re-deriving a different summary/extraction each turn is
// both costly and cache-hostile.

// resultKey namespaces a per-content reduced output (extract) by session.
func resultKey(session, id string) string { return "cg:res:" + session + ":" + id }

// getResult returns a previously cached reduced output for content id, if any.
func getResult(c *components.Ctx, id string) ([]byte, bool) {
	return c.Store.Get(resultKey(c.Session, id))
}

// putResult caches a reduced output so a later turn re-sending the same content
// reuses it (no LLM call, byte-identical result).
func putResult(c *components.Ctx, id string, v []byte) {
	c.Store.Put(resultKey(c.Session, id), v)
}

// sumCheckpoint is the per-session summarize state: the exact summary message
// text produced last time (re-emitted verbatim so the prefix stays byte-stable),
// how many leading span messages it subsumed, a hash of that span to prove the
// prefix is unchanged before reusing, and the stash key of the summarized span
// (its original is refreshed in the Store on reuse so expand keeps working).
type sumCheckpoint struct {
	SummaryMsg   string `json:"m"`
	CoveredCount int    `json:"c"`
	CoveredHash  string `json:"h"`
	Key          string `json:"k"`
}

func sumKey(session string) string { return "cg:sum:" + session }

func loadCheckpoint(c *components.Ctx) (sumCheckpoint, bool) {
	b, ok := c.Store.Get(sumKey(c.Session))
	if !ok {
		return sumCheckpoint{}, false
	}
	var cp sumCheckpoint
	if json.Unmarshal(b, &cp) != nil {
		return sumCheckpoint{}, false
	}
	return cp, true
}

func saveCheckpoint(c *components.Ctx, cp sumCheckpoint) {
	if b, err := json.Marshal(cp); err == nil {
		c.Store.Put(sumKey(c.Session), b)
	}
}

// spanHash is a stable content hash of a message span, used to confirm the
// covered prefix is unchanged on a later turn before reusing the summary.
func spanHash(span []bschemas.ChatMessage) string {
	h := sha256.New()
	for i := range span {
		b, _ := json.Marshal(span[i])
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}
