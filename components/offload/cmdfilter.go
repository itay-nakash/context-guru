// Package offload holds the lossy-but-reversible components (they drop bytes and
// stash the original for the expand tool loop). Each registers itself via
// init(); a binary blank-imports components/all to pull them in.
package offload

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/components/dsl"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/schema"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("cmdfilter", newCmdfilter) }

// Cmdfilter shrinks tool-output messages with declarative DSL filters. It is an
// Offload: it stashes the original before filtering so the expand tool can
// recover it. Filters match on the tool output's first non-empty line (the
// proxy-world stand-in for rtk's shell command).
type Cmdfilter struct {
	reg     *dsl.Registry
	mode    markerMode
	minSize int
}

type cmdfilterConfig struct {
	Filters         []string `yaml:"filters"`          // inline filter YAML documents
	DisableBuiltins bool     `yaml:"disable_builtins"` // skip the bundled starter filters
	MarkerMode      string   `yaml:"marker_mode"`      // full (default) | summary | off
	MinSize         *int     `yaml:"min_size"`         // byte floor below which filtering isn't worth a marker
}

// defaultMinSize is rtk's MIN_TEE_SIZE: below it the recovery marker routinely
// costs more tokens than the filter saves, so we don't bother. The
// marker-inclusive never-worse check would catch those anyway; this just skips the
// work (and the stash) instead of doing it and throwing it away.
const defaultMinSize = 500

func newCmdfilter(raw []byte) (components.Component, error) {
	var cfg cmdfilterConfig
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	reg := &dsl.Registry{}
	if !cfg.DisableBuiltins {
		if err := reg.Load([]byte(builtinFilters)); err != nil {
			return nil, err
		}
	}
	for _, doc := range cfg.Filters {
		if err := reg.Load([]byte(doc)); err != nil {
			return nil, err
		}
	}
	minSize := defaultMinSize
	if cfg.MinSize != nil {
		minSize = *cfg.MinSize
	}
	return &Cmdfilter{reg: reg, mode: parseMarkerMode(cfg.MarkerMode), minSize: minSize}, nil
}

func (Cmdfilter) Name() string { return "cmdfilter" }

func (f *Cmdfilter) Enabled(*components.Ctx) bool { return f.reg.Len() > 0 }

func (f *Cmdfilter) Offload(req *schemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	var keys []string
	changed := 0
	for i := range req.Input {
		m := &req.Input[i]
		if m.Role != schemas.ChatMessageRoleTool {
			continue
		}
		if !schema.Rewritable(*m) {
			continue // non-text blocks would be dropped by a text rewrite
		}
		content := schema.MessageText(*m)
		if content == "" {
			continue
		}
		if skipReduce(c, content) {
			continue // marker-bearing (a filter rule could drop the marker line and orphan
			// the stash) or expanded by the agent — leave it verbatim
		}
		if len(content) < f.minSize {
			continue // below the size floor the marker often costs more than the saving
		}
		key := selectorKey(content)
		filt := f.reg.Match(key)
		if filt == nil {
			if fs := c.Stats(); fs != nil {
				// The miss ledger: it turns "which filter to write next" into data
				// instead of guesswork (after rtk's parse_failures table). Log only the
				// FIRST line — the selector is multi-line, and keying the bounded ledger
				// on whole multi-line blobs would make almost every entry unique and
				// exhaust the cap on noise instead of ranking real shapes.
				fs.FilterMiss(firstLine(key))
			}
			continue
		}
		out, loss := dsl.Apply(filt, content)
		if out == content {
			continue
		}
		// Build the token that goes where the restoration marker would (per
		// marker_mode) WITHOUT stashing yet, so the never-worse check below can
		// still bail. Compare the FULL rewritten text (token included) against the
		// original — the marker costs tokens too, so filtering that barely wins can
		// still make the message larger (rtk never_worse, at the message level).
		stashKey := hashKey(content)
		// degrade full→off when the store can't persist (no unresolvable marker).
		mode := effectiveMode(c, f.mode)
		var token string
		switch mode {
		case markerFull:
			token = expand.Marker(stashKey) + recoveryHint(loss, len(strings.Split(out, "\n")))
		case markerSummary:
			token = expand.SummaryMarker
		} // off: no token
		newText := out
		if token != "" {
			newText += "\n" + token
		}
		before, after := schema.TextTokens(content), schema.TextTokens(newText)
		if after >= before {
			continue
		}
		if mode == markerFull {
			c.Store.Put(stashKey, []byte(content))
			recordOwner(c, stashKey) // scope GET /expand retrieval to this session
			keys = append(keys, stashKey)
		} else {
			rep.Irreversible = true
		}
		schema.SetMessageText(m, newText)
		if fs := c.Stats(); fs != nil {
			fs.FilterAct(filt.Family(), filt.Name, stashKey, before-after)
		}
		changed++
	}
	if changed == 0 {
		rep.Skipped = true
	}
	return keys, nil
}

// selectorHeadLines is how many leading non-empty lines a filter's match regex is
// tested against. It is NOT 1, and that matters: measuring real agent traffic showed
// 112 pytest outputs (311 KB) matching nothing because the harness prepends its own
// preamble ("Exit code 1", "Internet access disabled") or the run opens with a bare
// "ERROR path::test" line, so pytest's "=== test session starts ===" banner is never
// the FIRST line. A one-line selector makes a filter's reach depend on the agent's
// output framing rather than on the tool that produced it.
//
// Kept small on purpose: a whole-blob scan would let a generic pattern match on some
// incidental line deep inside unrelated output, which is the opposite failure.
const selectorHeadLines = 6

// firstLine returns the leading line of a (possibly multi-line) selector key.
func firstLine(key string) string {
	if i := strings.IndexByte(key, '\n'); i >= 0 {
		return key[:i]
	}
	return key
}

// selectorKey is the string a filter's match regex is tested against: the first few
// non-empty, trimmed lines of the tool output, newline-joined.
func selectorKey(content string) string {
	var head []string
	for _, line := range strings.Split(content, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			head = append(head, s)
			if len(head) == selectorHeadLines {
				break
			}
		}
	}
	return strings.Join(head, "\n")
}

// recoveryHint types the hint by WHAT was lost. A clean contiguous tail cut is
// cheaply recoverable — the agent can re-read from the cut point instead of pulling
// the whole blob back — so it says so (rtk emits a partial-recovery hint for the
// same case). Collapsing both kinds into one hint made every loss look like a
// whole-blob loss and pushed the agent toward the expensive recovery.
func recoveryHint(loss dsl.Lossiness, kept int) string {
	switch loss {
	case dsl.LossTail:
		return " [truncated after line " + strconv.Itoa(kept) + "; rest via " + expand.ToolName + "]"
	case dsl.LossWhole:
		return " [full output: call " + expand.ToolName + "]"
	default:
		return ""
	}
}

func hashKey(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}
