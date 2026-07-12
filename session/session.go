// Package session resolves the conversation key that state is scoped to.
//
// Per the design (D4): each host supplies an explicit id when it has one
// (bifrost proxy: x-context-guru-session header or Anthropic metadata.user_id;
// AuthBridge: pctx.Session A2A id; eval-containers: gateway-stamped). When none
// is available we fall back to a deterministic content hash of the system
// prompt + first user message, which needs no host cooperation.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Resolve returns the session key. If explicit is non-empty it wins; otherwise
// a stable hash of (system, firstUser) is used. The fallback matches winnow/lab
// behaviour so two turns of the same conversation land on the same key.
func Resolve(explicit, system, firstUser string) string {
	if s := strings.TrimSpace(explicit); s != "" {
		return s
	}
	h := sha256.Sum256([]byte(system + "\x00" + firstUser))
	return hex.EncodeToString(h[:])[:16]
}
