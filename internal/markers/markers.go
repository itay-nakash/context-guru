// Package markers handles reversible, namespaced content markers. winnow's marker
// is «winnow:HEXID»; foreign markers from other reducers (headroom, claw) are left
// alone so winnow stacks cleanly on top of them. Ported from winnow's markers.py.
package markers

import (
	"fmt"
	"regexp"
)

var (
	winnowRe  = regexp.MustCompile(`«winnow:([0-9a-f]{4,64})»`)
	foreignRe = regexp.MustCompile(`<<ccr:[0-9a-f]{4,64}>>|\[rewind:[0-9a-zA-Z]{4,64}\]`)
)

// Make returns the marker for a rewind id.
func Make(rewindID string) string { return fmt.Sprintf("«winnow:%s»", rewindID) }

// RecoveryNote is a self-advertising, model-readable note appended to a reduced
// block so the omission is a known unknown the model can recover, not a silent drop.
func RecoveryNote(label, what, rewindID string) string {
	return fmt.Sprintf("[winnow: %s %s; call winnow_expand(%q) to restore] %s",
		label, what, rewindID, Make(rewindID))
}

// FindIDs returns all winnow rewind ids referenced in text.
func FindIDs(text string) []string {
	m := winnowRe.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(m))
	for _, g := range m {
		out = append(out, g[1])
	}
	return out
}

// HasForeign reports whether text carries another reducer's marker.
func HasForeign(text string) bool { return foreignRe.MatchString(text) }

// Strip removes winnow and foreign markers from text (used for a marker-insensitive
// content key).
func Strip(text string) string {
	return foreignRe.ReplaceAllString(winnowRe.ReplaceAllString(text, ""), "")
}
