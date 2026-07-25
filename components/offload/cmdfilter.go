// Package offload holds the lossy-but-reversible components (they drop bytes and
// stash the original for the expand tool loop). Each registers itself via
// init(); a binary blank-imports components/all to pull them in.
package offload

import (
	"crypto/sha256"
	"encoding/hex"
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
	reg  *dsl.Registry
	mode markerMode
}

type cmdfilterConfig struct {
	Filters         []string `yaml:"filters"`          // inline filter YAML documents
	DisableBuiltins bool     `yaml:"disable_builtins"` // skip the bundled starter filters
	MarkerMode      string   `yaml:"marker_mode"`      // full (default) | summary | off
}

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
	return &Cmdfilter{reg: reg, mode: parseMarkerMode(cfg.MarkerMode)}, nil
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
		filt := f.reg.Match(selectorKey(content))
		if filt == nil {
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
		key := hashKey(content)
		// degrade full→off when the store can't persist (no unresolvable marker).
		mode := effectiveMode(c, f.mode)
		var token string
		switch mode {
		case markerFull:
			token = expand.Marker(key) + recoveryHint(loss)
		case markerSummary:
			token = expand.SummaryMarker
		} // off: no token
		newText := out
		if token != "" {
			newText += "\n" + token
		}
		if schema.TextTokens(newText) >= schema.TextTokens(content) {
			continue
		}
		if mode == markerFull {
			c.Store.Put(key, []byte(content))
			recordOwner(c, key) // scope GET /expand retrieval to this session
			keys = append(keys, key)
		} else {
			rep.Irreversible = true
		}
		schema.SetMessageText(m, newText)
		changed++
	}
	if changed == 0 {
		rep.Skipped = true
	}
	return keys, nil
}

// selectorKey is the string a filter's match regex is tested against: the first
// non-empty, trimmed line of the tool output.
func selectorKey(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

func recoveryHint(loss dsl.Lossiness) string {
	if loss == dsl.LossNone {
		return ""
	}
	return " [full output: call " + expand.ToolName + "]"
}

func hashKey(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}

// builtinFilters is a small starter set adapted from rtk built-ins. Users add
// more via the cmdfilter `filters:` config with no recompile.
const builtinFilters = `
schema_version: 1
filters:
  pytest:
    description: keep failures + summary, drop passing noise
    match: "(pytest|=+ test session starts)"
    strip_lines_matching:
      - "^\\s*$"
      - " PASSED"
      - "^\\.+$"
    max_lines: 80
    on_empty: "pytest: all passed"
  npm-install:
    description: collapse npm/yarn install chatter
    match: "^(npm|yarn|added|removed) "
    strip_lines_matching:
      - "^npm warn"
      - "^\\s*$"
    max_lines: 40
    on_empty: "install: ok"
  make:
    description: drop make directory chatter
    match: "^(make|gcc|cc|clang) "
    strip_lines_matching:
      - "^make\\[\\d+\\]:"
      - "^\\s*$"
    max_lines: 60
    on_empty: "make: ok"
`
