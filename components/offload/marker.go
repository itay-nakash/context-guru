package offload

import (
	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/expand"
)

// markerMode selects what an Offload component leaves behind in place of the
// content it drops. It is per-component config (marker_mode), defaulting to full.
//
//   - full    (default): stash the original in the Store and leave a resolvable
//     <<cg:HASH>> marker, so the expand tool can restore it. Fully reversible.
//   - summary: leave a non-resolvable ⟪cg⟫ sentinel next to the component's own
//     human note. Nothing is stashed; there is no restoration. The note is the
//     "short summary of what was compacted."
//   - off:     leave no marker at all — just the reduced content / note. No stash,
//     no restoration, no sentinel.
//
// summary/off are deliberate lossy drops, so mark records rep.Irreversible to
// exempt them from the pipeline's "dropped content without stashing → revert"
// guard (which still catches a component that forgot to stash in full mode).
type markerMode int

const (
	markerFull markerMode = iota
	markerSummary
	markerOff
)

// parseMarkerMode maps the yaml value to a mode; unknown/empty → full (so
// existing configs keep their reversible behavior).
func parseMarkerMode(s string) markerMode {
	switch s {
	case "summary":
		return markerSummary
	case "off":
		return markerOff
	default:
		return markerFull
	}
}

// mark centralizes the three marker_modes for the spot where an Offload component
// would write its restoration marker. It returns the token to splice there and
// the store key to append to the component's cacheKeys (empty in summary/off).
//
//	full    -> Store.Put(HASH, original); token = "<<cg:HASH>>"+hint ; key = HASH
//	summary -> no stash; token = expand.SummaryMarker ; key = "" ; rep.Irreversible
//	off     -> no stash; token = "" ; key = "" ; rep.Irreversible
//
// hint is the component-specific recovery hint (e.g. " [full output: call
// context_guru_expand]"); it is only emitted in full mode, where expand works.
func mark(c *components.Ctx, rep *components.Report, mode markerMode, original, hint string) (token, key string) {
	if mode == markerFull {
		key = hashKey(original)
		c.Store.Put(key, []byte(original))
		return expand.Marker(key) + hint, key
	}
	rep.Irreversible = true
	if mode == markerSummary {
		return expand.SummaryMarker, ""
	}
	return "", "" // off
}
