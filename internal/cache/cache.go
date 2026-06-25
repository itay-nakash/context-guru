// Package cache injects Anthropic ephemeral cache_control breakpoints on the stable
// prefix of a request. Lossless: nothing is dropped or rewritten, only annotated
// where the provider may cache. Ported from the reference prototype's cache.py.
//
// All functions operate on the canonical Root map (Anthropic shape) in place.
package cache

const maxBreakpoints = 4

func ephemeral() map[string]any { return map[string]any{"type": "ephemeral"} }

// asBlocks returns msg["content"] as a []any of block maps, or nil.
func msgContentList(msg map[string]any) ([]any, bool) {
	c, ok := msg["content"].([]any)
	return c, ok
}

func hasCacheControl(block any) bool {
	b, ok := block.(map[string]any)
	if !ok {
		return false
	}
	_, ok = b["cache_control"]
	return ok
}

// FloorIndex returns the message index of the last message carrying a cache_control
// breakpoint (-1 if none). Reducing any message at or before this index would bust
// the client's prompt cache, so reduction must stay below it. -1 means unrestricted.
func FloorIndex(root map[string]any) int {
	floor := -1
	msgs, _ := root["messages"].([]any)
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		c, ok := msgContentList(mm)
		if !ok {
			continue
		}
		for _, b := range c {
			if hasCacheControl(b) {
				floor = i
				break
			}
		}
	}
	return floor
}

// ProtectedAnchorIndex returns the message index just behind the reducer's protected
// working set — the deepest place a stable breakpoint can sit and still be byte-
// identical next turn. Returns -1 if there's nothing to anchor.
func ProtectedAnchorIndex(msgs []any, protectRecent, protectRecentToolUses int) int {
	n := len(msgs)
	if n == 0 {
		return -1
	}
	protectFrom := n - protectRecent
	if protectRecentToolUses > 0 {
		var tu []int
		for i, m := range msgs {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			c, ok := msgContentList(mm)
			if !ok {
				continue
			}
			for _, b := range c {
				if bb, ok := b.(map[string]any); ok && bb["type"] == "tool_use" {
					tu = append(tu, i)
					break
				}
			}
		}
		if len(tu) >= protectRecentToolUses {
			if cand := tu[len(tu)-protectRecentToolUses]; cand < protectFrom {
				protectFrom = cand
			}
		}
	}
	if protectFrom-1 < 0 {
		return 0
	}
	return protectFrom - 1
}

func countBreakpoints(root map[string]any) int {
	n := 0
	if sys, ok := root["system"].([]any); ok {
		for _, b := range sys {
			if hasCacheControl(b) {
				n++
			}
		}
	}
	msgs, _ := root["messages"].([]any)
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if c, ok := msgContentList(mm); ok {
			for _, b := range c {
				if hasCacheControl(b) {
					n++
				}
			}
		}
	}
	return n
}

// markLastBlock adds cache_control to the last block of a message content,
// normalising a bare string into a single text block. Returns the new content.
func markLastBlock(content any) any {
	switch c := content.(type) {
	case string:
		if c != "" {
			return []any{map[string]any{"type": "text", "text": c, "cache_control": ephemeral()}}
		}
	case []any:
		if len(c) > 0 {
			if last, ok := c[len(c)-1].(map[string]any); ok {
				if _, has := last["cache_control"]; !has {
					last["cache_control"] = ephemeral()
				}
			}
		}
	}
	return content
}

// Inject annotates root in place with ephemeral cache breakpoints on the stable
// prefix. Safe/no-op once the provider cap (4) is reached. stableAnchorIdx (or -1
// to fall back to a tail offset) pins the deep-stable breakpoint to recurring bytes.
func Inject(root map[string]any, breakpoints, stableGap, stableAnchorIdx int, toolsBreakpoint bool) {
	budget := maxBreakpoints - countBreakpoints(root)
	if budget <= 0 {
		return
	}

	// 0) tools: biggest static segment.
	if toolsBreakpoint && budget > 0 {
		if tools, ok := root["tools"].([]any); ok && len(tools) > 0 {
			if last, ok := tools[len(tools)-1].(map[string]any); ok {
				if _, has := last["cache_control"]; !has {
					last["cache_control"] = ephemeral()
					budget--
				}
			}
		}
	}

	// 1) end of system: caches tools + system.
	if budget > 0 {
		switch sys := root["system"].(type) {
		case string:
			if sys != "" {
				root["system"] = []any{map[string]any{"type": "text", "text": sys, "cache_control": ephemeral()}}
				budget--
			}
		case []any:
			if len(sys) > 0 {
				if last, ok := sys[len(sys)-1].(map[string]any); ok {
					if _, has := last["cache_control"]; !has {
						last["cache_control"] = ephemeral()
						budget--
					}
				}
			}
		}
	}

	msgs, _ := root["messages"].([]any)

	// 2) deep-stable breakpoint, anchored to the protected-set boundary when given.
	stableIdx := stableAnchorIdx
	if !(stableAnchorIdx >= 0 && stableAnchorIdx < len(msgs)-1) {
		gap := stableGap
		if gap < 1 {
			gap = 1
		}
		stableIdx = len(msgs) - 1 - gap
	}
	if breakpoints >= 3 && budget > 0 && stableIdx >= 0 && stableIdx < len(msgs)-1 {
		if m, ok := msgs[stableIdx].(map[string]any); ok {
			m["content"] = markLastBlock(m["content"])
			budget--
		}
	}

	// 3) rolling breakpoint on the last message.
	if budget > 0 && len(msgs) > 0 {
		if m, ok := msgs[len(msgs)-1].(map[string]any); ok {
			m["content"] = markLastBlock(m["content"])
		}
	}
}
