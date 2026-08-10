package apply

import (
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Metadata writes: the narrow exception to the losslessness guard.
//
// bifrost cannot round-trip an Anthropic assistant turn that carries a `tool_use`
// block — it drops `id`, `name` and `input` on unmarshal — so the writeback loop
// discards any change to such a message rather than splice a lossy re-marshal.
// That is correct for CONTENT. But cacheinject's only possible targets on real
// agent traffic are exactly those messages, so every breakpoint it placed was
// thrown away before the request was sent (measured: 46 applied, 0 forwarded over
// 40 captured requests — issue #32).
//
// `cache_control` is metadata, not content: it changes nothing the model reads and
// needs no message model to express. So when a component's ONLY change to a
// message is adding `cache_control` to content blocks, we write those keys at their
// exact paths on the ORIGINAL raw bytes instead of splicing a re-marshal. Nothing
// else in the message is read or rewritten, so no provider field can be dropped
// and the guard's reason to exist does not arise.
//
// Anything beyond added `cache_control` keys — a text edit, a removed key, a
// changed block — is still discarded.

// metaWrite is one metadata key to set, as a path relative to the message and the
// raw JSON to write there.
type metaWrite struct {
	path string // "content.<b>.cache_control"
	raw  string
}

// metadataOnlyWrites compares a message's pre- and post-pipeline canonical
// marshals and returns the metadata writes that reproduce the change, or ok=false
// if the change is anything other than added content-block `cache_control` keys.
func metadataOnlyWrites(pre, post []byte) (writes []metaWrite, ok bool) {
	preBlocks := gjson.GetBytes(pre, "content")
	postBlocks := gjson.GetBytes(post, "content")
	if !preBlocks.IsArray() || !postBlocks.IsArray() {
		return nil, false // string content carries no block to mark
	}
	pb, qb := preBlocks.Array(), postBlocks.Array()
	if len(pb) != len(qb) {
		return nil, false // blocks added/removed — not a metadata change
	}
	stripped := post
	for b := range qb {
		p := "content." + strconv.Itoa(b) + ".cache_control"
		cc := qb[b].Get("cache_control")
		if !cc.Exists() || pb[b].Get("cache_control").Exists() {
			continue
		}
		writes = append(writes, metaWrite{path: p, raw: cc.Raw})
		var err error
		if stripped, err = sjson.DeleteBytes(stripped, p); err != nil {
			return nil, false
		}
	}
	if len(writes) == 0 {
		return nil, false
	}
	// The decisive check: with the added keys removed, the post-pipeline message must
	// be identical to the pre-pipeline one. Otherwise something else changed too.
	if !jsonEqual(stripped, pre) {
		return nil, false
	}
	return writes, true
}

// applyMetaWrites sets each metadata key on the raw body at msgPath. It refuses
// (ok=false, body untouched) if the raw message's block layout does not match what
// the writes assume, so a shape the normalizer and the raw body disagree about can
// never be written to the wrong block.
func applyMetaWrites(body []byte, msgPath string, nBlocks int, writes []metaWrite) ([]byte, bool) {
	blocks := gjson.GetBytes(body, msgPath+".content")
	if !blocks.IsArray() || len(blocks.Array()) != nBlocks {
		return body, false
	}
	out := body
	for _, w := range writes {
		full := msgPath + "." + w.path
		if gjson.GetBytes(out, full).Exists() {
			return body, false // caller already set it — never overwrite
		}
		next, err := sjson.SetRawBytes(out, full, []byte(w.raw))
		if err != nil {
			return body, false
		}
		out = next
	}
	return out, true
}

// maxWireBreakpoints is the provider's hard cap on cache_control directives per
// request. Over it, the request 400s.
const maxWireBreakpoints = 4

// breakpointPaths are every location a real prompt-cache breakpoint can live. The
// cap applies across all of them together. Structural (gjson path queries) for the
// same reason hasCacheBreakpoint is: a tool output whose text merely contains the
// string "cache_control" must not count.
//
// `cachePoint` is the Bedrock Converse spelling, and Bedrock places it as its OWN
// entry in the `system` and `tools` arrays — the two locations defect 2 is about. Those
// paths must be counted or the cap stays breachable on Bedrock exactly as it was on
// Anthropic (constructed: 6 on the wire, counter blind to 3 of them).
var breakpointPaths = []string{
	"system.#.cache_control",
	"tools.#.cache_control",
	"messages.#.cache_control",
	"messages.#.content.#.cache_control",
	"system.#.cachePoint",
	"tools.#.cachePoint",
	"messages.#.content.#.cachePoint",
}

// wireBreakpoints counts every breakpoint the provider will see in this request.
//
// A component cannot count these for itself. `system` and `tools` never reach it at
// all, and the `tool_result` blocks this package normalizes into synthetic role=tool
// messages lose their mark on the way in (see toolMessage) — so on real Claude Code
// traffic all three of the agent's own breakpoints were invisible to it (2 in
// `system`, 1 on a `tool_result` block), and it computed 3 free slots when 1 was free:
// 6 on the wire, and a 400 (issue #32).
func wireBreakpoints(body []byte) int {
	n := 0
	for _, p := range breakpointPaths {
		gjson.GetBytes(body, p).ForEach(func(_, v gjson.Result) bool {
			// nested arrays (content-of-messages) surface as arrays here; recurse one level
			if v.IsArray() {
				v.ForEach(func(_, vv gjson.Result) bool {
					if vv.IsObject() {
						n++
					}
					return true
				})
			} else if v.IsObject() {
				n++
			}
			return true
		})
	}
	return n
}
