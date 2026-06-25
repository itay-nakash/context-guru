// Package markers handles reversible, namespaced content markers. lab-cx's marker
// is «labcx:HEXID»; foreign markers from other reducers (headroom, claw) are left
// alone so lab-cx stacks cleanly on top of them. Ported from the reference prototype's
// markers.py.
package markers

import (
	"fmt"
	"regexp"
)

var (
	labcxRe   = regexp.MustCompile(`«labcx:([0-9a-f]{4,64})»`)
	foreignRe = regexp.MustCompile(`<<ccr:[0-9a-f]{4,64}>>|\[rewind:[0-9a-zA-Z]{4,64}\]`)
)

// Make returns the marker for a rewind id.
func Make(rewindID string) string { return fmt.Sprintf("«labcx:%s»", rewindID) }

// RecoveryNote is a self-advertising, model-readable note appended to a reduced
// block so the omission is a known unknown the model can recover, not a silent drop.
func RecoveryNote(label, what, rewindID string) string {
	return fmt.Sprintf("[labcx: %s %s; call labcx_expand(%q) to restore] %s",
		label, what, rewindID, Make(rewindID))
}

// FindIDs returns all lab-cx rewind ids referenced in text.
func FindIDs(text string) []string {
	m := labcxRe.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(m))
	for _, g := range m {
		out = append(out, g[1])
	}
	return out
}

// Has reports whether text already carries a lab-cx marker (i.e. the block was
// already reduced/extracted on this or a prior turn). Compactors use it to avoid
// re-processing an already-reduced block.
func Has(text string) bool { return labcxRe.MatchString(text) }

// HasForeign reports whether text carries another reducer's marker.
func HasForeign(text string) bool { return foreignRe.MatchString(text) }

// Strip removes lab-cx and foreign markers from text (used for a marker-insensitive
// content key).
func Strip(text string) string {
	return foreignRe.ReplaceAllString(labcxRe.ReplaceAllString(text, ""), "")
}
