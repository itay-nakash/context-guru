package offload

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync/atomic"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/internal/extract"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
)

// State reuse over the generic Store (key→bytes), session-scoped by key prefix
// so a prior compaction is reused across turns instead of re-calling the LLM.
// Reusing the previous output byte-for-byte also keeps the request prefix stable
// (KV-cache friendly) — re-deriving a different summary/extraction each turn is
// both costly and cache-hostile.

// resultKey namespaces a per-content reduced output (extract) by session.
func resultKey(session, id string) string { return store.ResultPrefix + session + ":" + id }

// getResult returns a previously cached reduced output for content id, if any. This is
// extract_llm's replay lookup, so it feeds the same hit/miss counters as reapplyFrozen —
// otherwise the shipped coding config (no mask, failed_run self-skipping) would report
// zero freeze activity while doing all of its replay through here.
func getResult(c *components.Ctx, id string) ([]byte, bool) {
	v, ok := c.Store.Get(resultKey(c.Session, id))
	if ok {
		frozenHits.Add(1)
	} else {
		frozenMisses.Add(1)
	}
	return v, ok
}

// putResult caches a reduced output so a later turn re-sending the same content
// reuses it (no LLM call, byte-identical result).
func putResult(c *components.Ctx, id string, v []byte) {
	c.Store.Put(resultKey(c.Session, id), v)
}

// --- Freeze + reapply (cache stability) -------------------------------------
//
// The cache-safety invariant: once an offloader compacts an output, it must send the
// SAME bytes for that output on every later turn — otherwise the agent (which re-sends
// the ORIGINAL each turn) makes the output flip compacted→full→compacted, churning the
// provider KV cache. A tail gate alone is not enough: it compacts in the tail then
// skips once the content is in the prefix, which is exactly that churn. So a tail-gated
// offloader FREEZES its decision here (keyed by component + original-content hash) and
// REAPPLIES it on every turn, regardless of the tail boundary. New decisions are still
// gated to the tail; frozen ones are replayed everywhere.

func frozenKey(session, comp, ck string) string {
	return store.FrozenPrefix + session + ":" + comp + ":" + ck
}

// freeze records the replacement text a component produced for an original content, so
// later turns replay it byte-for-byte.
func freeze(c *components.Ctx, comp, original, replacement string) {
	c.Store.Put(frozenKey(c.Session, comp, contentKey(original)), []byte(replacement))
}

// frozenLost reports that this component DID freeze a decision for this content and the
// store has since dropped it (TTL expiry / pin cap). It is the counterpart to a plain
// reapplyFrozen miss, which is indistinguishable from "never frozen" — and the two call
// for OPPOSITE behavior:
//
//   - never frozen: obey the tail gate. Compacting content the provider already cached
//     flips it and forces a suffix cache-write, so NEW decisions stay in the tail.
//   - frozen, then lost: the provider ALREADY holds the compacted bytes for this message,
//     so leaving it verbatim is itself the cache-destructive move. Re-derive the decision
//     even at depth. This is not a fail-open exception — an offloader's replacement text
//     is a pure function of (content, component config), and the marker key is
//     sha256(original), so re-deriving reproduces the SAME bytes the provider cached and
//     re-establishes the freeze. For an ESTABLISHED compaction the safe direction is the
//     opposite of the usual "forward the original" (see docs/design.md).
//
// Only the FACT of the freeze has to survive, not its payload — one key in a bounded set,
// which is why the signal lives in the store instead of a second content index.
func frozenLost(c *components.Ctx, key string) bool {
	fl, ok := c.Store.(store.FrozenLoser)
	return ok && fl.FrozenLost(key)
}

// reapplyFrozen replays a component's frozen decision for the message at m, if one
// exists and still shrinks it. It also refreshes the expand originals for any markers
// in the replacement (the agent re-sent the full original as m's content), so
// restoration keeps working across turns. Returns the marker keys + whether it acted.
func reapplyFrozen(c *components.Ctx, comp string, m *bschemas.ChatMessage) ([]string, int, bool) {
	content := schema.MessageText(*m)
	if isKeptVerbatim(c, contentKey(content)) {
		return nil, 0, false // agent expanded this; replaying the collapse would loop
	}
	repl, ok := c.Store.Get(frozenKey(c.Session, comp, contentKey(content)))
	if !ok {
		frozenMisses.Add(1)
		return nil, 0, false
	}
	frozenHits.Add(1)
	rs := string(repl)
	saved := schema.TextTokens(content) - schema.TextTokens(rs)
	if saved <= 0 {
		return nil, 0, false
	}
	keys := expand.ParseMarkers(rs)
	for _, k := range keys {
		c.Store.Put(k, []byte(content)) // refresh the stashed original for expand
	}
	schema.SetMessageText(m, rs)
	return keys, saved, true
}

// repairLostFreeze reports whether an offloader may compact this message even though the
// cache-tail gate would forbid it, because a freeze for it was established and then lost.
// Re-deriving reproduces the bytes the provider already cached; NOT re-deriving is what
// flips the representation and re-writes the suffix. The caller's own never-worse and
// skipReduce guards still apply, so this only ever LIFTS the depth restriction.
func repairLostFreeze(c *components.Ctx, comp, content string) bool {
	return frozenLost(c, frozenKey(c.Session, comp, contentKey(content)))
}

// repairLostResult is repairLostFreeze for the OTHER replay namespace: extract_llm's
// per-content result cache (cg:res:), which is the same replay contract under a different
// name — and the one that actually carries the load in the shipped coding config, where
// mask is absent and failed_run self-skips on a cached agent. A lost result cache entry
// un-compacts an already-cached message exactly the same way, so it gets the same repair.
func repairLostResult(c *components.Ctx, id string) bool {
	return frozenLost(c, resultKey(c.Session, id))
}

// Freeze-replay counters: how often a replay landed vs found nothing. Cache-write is the
// largest cost line on long-horizon traffic and a lost freeze is the mechanism that
// produces it, so the store counts the drops and the repairs (a re-Put of a dropped
// frozen key) itself — exactly once each, regardless of how many turns observe them —
// while these two count the replay path. /stats reports all of them together.
var (
	frozenHits   atomic.Int64
	frozenMisses atomic.Int64
)

// FrozenStats returns the cumulative freeze-replay hits and misses since process start.
// Exported for the host's /stats, which pairs them with the store's drop/repair counts.
func FrozenStats() (hits, misses int64) { return frozenHits.Load(), frozenMisses.Load() }

// contentKey is a marker/whitespace-insensitive content hash (shared with extract's
// result cache), so the same output re-sent across turns maps to one frozen decision.
func contentKey(s string) string { return extract.ContentKey(s) }

// --- Kept-verbatim (expanded content) -------------------------------------
//
// When the agent expands an offloaded output, re-compacting it on the next turn would
// just make the agent expand it again — an expand loop (wasted round-trips + cache
// churn). The expand handler marks the restored content's key kept-verbatim so the
// offloaders leave it alone thereafter.

func keptKey(ck string) string { return "cg:keep:" + ck }

// MarkKeptVerbatim records that this original content was expanded and must not be
// re-compacted (keyed by content hash, session-independent). Exported for the proxy's
// expand loop, which has the restored original but not the offload Ctx.
func MarkKeptVerbatim(st store.Store, original string) {
	st.Put(keptKey(contentKey(original)), []byte{1})
}

func isKeptVerbatim(c *components.Ctx, ck string) bool {
	_, ok := c.Store.Get(keptKey(ck))
	return ok
}

// skipReduce reports whether an offloader must leave this content untouched: it
// already carries an offload marker (reducing again would double-compact and can
// orphan the earlier stash), or the agent expanded it and re-compacting would just
// trigger another expand — a per-turn bounce loop. Every offloader consults this on
// each candidate so the kept-verbatim / never-double-reduce guarantees hold uniformly.
func skipReduce(c *components.Ctx, content string) bool {
	return expand.HasPlaceholder(content) || isKeptVerbatim(c, contentKey(content))
}

// --- Stash ownership (scoping GET /expand by session) ----------------------
//
// A rewind stash is keyed by a content HASH, which is global (the same content in two
// sessions hashes the same). The model-driven expand loop is inherently same-session (a
// request only ever contains markers minted from its own content), but the management
// GET /expand endpoint takes an arbitrary id — so without a scope check any client that
// reaches the proxy could fetch another session's stashed original. We record, per
// (session, key), that this session stashed that key, and GET /expand only resolves a
// key the caller's session actually owns.

func ownerKey(session, key string) string { return "cg:own:" + session + ":" + key }

// recordOwner marks that this session stashed key (no-op for empty key / summary-off).
func recordOwner(c *components.Ctx, key string) {
	if key != "" {
		c.Store.Put(ownerKey(c.Session, key), []byte{1})
	}
}

// OwnsKey reports whether session stashed key. Exported for the proxy's GET /expand
// handler to scope retrieval to the caller's session (prevents cross-session/tenant
// disclosure of offloaded originals). Returns false when either is empty.
func OwnsKey(st store.Store, session, key string) bool {
	if session == "" || key == "" {
		return false
	}
	_, ok := st.Get(ownerKey(session, key))
	return ok
}

// summaryKey namespaces the one-line SUMMARY the LLM extract emitted for a content
// id, so a later turn reusing the cached reduction also re-emits the same marker
// digest (byte-stable) without re-calling the model.
func summaryKey(session, id string) string { return store.SummaryPrefix + session + ":" + id }

func getSummary(c *components.Ctx, id string) (string, bool) {
	b, ok := c.Store.Get(summaryKey(c.Session, id))
	return string(b), ok
}

func putSummary(c *components.Ctx, id, s string) {
	c.Store.Put(summaryKey(c.Session, id), []byte(s))
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
