// Package dsl is a declarative, user-extensible text-filter engine, adapted
// from rtk's TOML filter DSL (design D11). It lets command/log/tool-output
// shrinking be authored in YAML — no recompile — with the same 8-stage pipeline
// and Lossiness typing rtk proved out.
//
// Adaptation for the proxy world: rtk matches a shell command string; we match a
// per-message "selector" (the tool name, or the first line of content) since a
// proxy sees tool OUTPUTS, not the command that produced them. The content
// stages are unchanged.
//
// Pipeline order (each stage optional, applied in this exact order):
//  1. strip_ansi          2. replace[]        3. match_output[]+unless
//  4. strip/keep lines     5. truncate_lines_at 6. head/tail
//  7. max_lines           8. on_empty
//
// Filters are lossy (they drop lines), so the cmdfilter component that wraps
// this engine is an Offload: it stashes the original before applying a filter.
package dsl

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Lossiness records what a filter did to its input, so the caller can emit an
// accurate recovery hint (after rtk's Lossiness enum).
type Lossiness int

const (
	LossNone  Lossiness = iota // nothing dropped, or only reversible reformatting
	LossTail                   // a clean contiguous tail was dropped (tail -n +N recovers)
	LossWhole                  // non-contiguous / whole-blob loss; only full retrieval recovers
)

// Def is a raw filter definition (from YAML). All fields except Match are optional.
type Def struct {
	Description        string        `yaml:"description"`
	Match              string        `yaml:"match"` // regex against the selector key
	StripANSI          bool          `yaml:"strip_ansi"`
	Replace            []ReplaceRule `yaml:"replace"`
	MatchOutput        []MatchRule   `yaml:"match_output"`
	StripLinesMatching []string      `yaml:"strip_lines_matching"`
	KeepLinesMatching  []string      `yaml:"keep_lines_matching"`
	TruncateLinesAt    *int          `yaml:"truncate_lines_at"`
	HeadLines          *int          `yaml:"head_lines"`
	TailLines          *int          `yaml:"tail_lines"`
	MaxLines           *int          `yaml:"max_lines"`
	OnEmpty            *string       `yaml:"on_empty"`
}

// ReplaceRule is a chained line-by-line regex substitution ($1 backrefs allowed).
type ReplaceRule struct {
	Pattern     string `yaml:"pattern"`
	Replacement string `yaml:"replacement"`
}

// MatchRule short-circuits: if Pattern matches the whole blob (and Unless does
// not), return Message immediately.
type MatchRule struct {
	Pattern string `yaml:"pattern"`
	Message string `yaml:"message"`
	Unless  string `yaml:"unless"`
}

// File is a filter document: a schema version + named filters + inline tests.
type File struct {
	SchemaVersion int                   `yaml:"schema_version"`
	Filters       map[string]Def        `yaml:"filters"`
	Tests         map[string][]TestCase `yaml:"tests"`
}

// TestCase is an inline filter test (input -> expected), run by RunTests.
type TestCase struct {
	Name     string `yaml:"name"`
	Input    string `yaml:"input"`
	Expected string `yaml:"expected"`
}

// Compiled is a filter with its regexes prebuilt.
type Compiled struct {
	Name       string
	match      *regexp.Regexp
	def        Def
	replace    []compiledReplace
	matchOut   []compiledMatch
	stripLines []*regexp.Regexp
	keepLines  []*regexp.Regexp
}

type compiledReplace struct {
	re   *regexp.Regexp
	repl string
}
type compiledMatch struct {
	re     *regexp.Regexp
	msg    string
	unless *regexp.Regexp
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// Compile validates and precompiles a filter definition.
func Compile(name string, d Def) (*Compiled, error) {
	if d.Match == "" {
		return nil, fmt.Errorf("dsl: filter %q missing match", name)
	}
	if len(d.StripLinesMatching) > 0 && len(d.KeepLinesMatching) > 0 {
		return nil, fmt.Errorf("dsl: filter %q sets both strip_lines_matching and keep_lines_matching", name)
	}
	m, err := regexp.Compile(d.Match)
	if err != nil {
		return nil, fmt.Errorf("dsl: filter %q match: %w", name, err)
	}
	c := &Compiled{Name: name, match: m, def: d}
	for i, r := range d.Replace {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("dsl: filter %q replace[%d]: %w", name, i, err)
		}
		c.replace = append(c.replace, compiledReplace{re: re, repl: r.Replacement})
	}
	for i, r := range d.MatchOutput {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("dsl: filter %q match_output[%d]: %w", name, i, err)
		}
		cm := compiledMatch{re: re, msg: r.Message}
		if r.Unless != "" {
			u, err := regexp.Compile(r.Unless)
			if err != nil {
				return nil, fmt.Errorf("dsl: filter %q match_output[%d].unless: %w", name, i, err)
			}
			cm.unless = u
		}
		c.matchOut = append(c.matchOut, cm)
	}
	for _, p := range d.StripLinesMatching {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("dsl: filter %q strip_lines_matching: %w", name, err)
		}
		c.stripLines = append(c.stripLines, re)
	}
	for _, p := range d.KeepLinesMatching {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("dsl: filter %q keep_lines_matching: %w", name, err)
		}
		c.keepLines = append(c.keepLines, re)
	}
	return c, nil
}

// Apply runs the 8-stage pipeline over input, returning the filtered output and
// what kind of loss occurred.
func Apply(c *Compiled, input string) (string, Lossiness) {
	lines := strings.Split(input, "\n")

	// 1. strip_ansi
	if c.def.StripANSI {
		for i := range lines {
			lines[i] = ansiRe.ReplaceAllString(lines[i], "")
		}
	}
	// 2. replace (chained, line-by-line)
	if len(c.replace) > 0 {
		for i := range lines {
			for _, r := range c.replace {
				lines[i] = r.re.ReplaceAllString(lines[i], r.repl)
			}
		}
	}
	// 3. match_output — first matching rule wins, unless the guard matches.
	if len(c.matchOut) > 0 {
		blob := strings.Join(lines, "\n")
		for _, m := range c.matchOut {
			if m.re.MatchString(blob) && (m.unless == nil || !m.unless.MatchString(blob)) {
				return m.msg, LossWhole
			}
		}
	}
	// 4. strip/keep lines (mutually exclusive)
	if len(c.stripLines) > 0 {
		lines = filterLines(lines, c.stripLines, false)
	} else if len(c.keepLines) > 0 {
		lines = filterLines(lines, c.keepLines, true)
	}
	// 5. truncate_lines_at (unicode-safe per-line cap)
	if c.def.TruncateLinesAt != nil {
		n := *c.def.TruncateLinesAt
		for i, l := range lines {
			if r := []rune(l); len(r) > n {
				lines[i] = string(r[:n])
			}
		}
	}

	loss := LossNone
	// 6. head/tail
	if c.def.HeadLines != nil || c.def.TailLines != nil {
		lines, loss = headTail(lines, c.def.HeadLines, c.def.TailLines)
	}
	// 7. max_lines (absolute cap, counts the omission marker)
	if c.def.MaxLines != nil && len(lines) > *c.def.MaxLines {
		n := *c.def.MaxLines
		omitted := len(lines) - n
		lines = append(lines[:n:n], fmt.Sprintf("... (%d lines truncated)", omitted))
		if loss == LossNone {
			loss = LossTail
		} else {
			loss = LossWhole
		}
	}
	out := strings.Join(lines, "\n")
	// 8. on_empty
	if strings.TrimSpace(out) == "" && c.def.OnEmpty != nil {
		return *c.def.OnEmpty, LossNone
	}
	return out, loss
}

func filterLines(lines []string, res []*regexp.Regexp, keep bool) []string {
	out := lines[:0:0]
	for _, l := range lines {
		matched := false
		for _, re := range res {
			if re.MatchString(l) {
				matched = true
				break
			}
		}
		if matched == keep {
			out = append(out, l)
		}
	}
	return out
}

// headTail keeps head+tail lines with an omission marker between; returns the
// loss kind (LossTail when a clean contiguous tail was dropped, else LossWhole).
func headTail(lines []string, head, tail *int) ([]string, Lossiness) {
	total := len(lines)
	switch {
	case head != nil && tail != nil:
		if total <= *head+*tail {
			return lines, LossNone
		}
		omitted := total - *head - *tail
		out := append([]string{}, lines[:*head]...)
		out = append(out, fmt.Sprintf("... (%d lines omitted)", omitted))
		out = append(out, lines[total-*tail:]...)
		return out, LossWhole // non-contiguous drop
	case head != nil:
		if total <= *head {
			return lines, LossNone
		}
		out := append([]string{}, lines[:*head]...)
		out = append(out, fmt.Sprintf("... (%d lines omitted)", total-*head))
		return out, LossTail // clean tail drop
	case tail != nil:
		if total <= *tail {
			return lines, LossNone
		}
		out := []string{fmt.Sprintf("... (%d lines omitted)", total-*tail)}
		out = append(out, lines[total-*tail:]...)
		return out, LossWhole
	}
	return lines, LossNone
}

// Registry holds compiled filters, matched first-by-sorted-name for determinism.
type Registry struct{ filters []*Compiled }

// Load parses a YAML filter document and appends its filters to the registry.
// schema_version must be 1. Filters are stored sorted by name.
func (r *Registry) Load(b []byte) error {
	var f File
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("dsl: %w", err)
	}
	if f.SchemaVersion != 1 {
		return fmt.Errorf("dsl: unsupported schema_version %d (want 1)", f.SchemaVersion)
	}
	names := make([]string, 0, len(f.Filters))
	for n := range f.Filters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c, err := Compile(n, f.Filters[n])
		if err != nil {
			return err
		}
		r.filters = append(r.filters, c)
	}
	return nil
}

// Match returns the first filter whose match regex matches key, or nil.
func (r *Registry) Match(key string) *Compiled {
	for _, c := range r.filters {
		if c.match.MatchString(key) {
			return c
		}
	}
	return nil
}

// Len reports how many filters are loaded.
func (r *Registry) Len() int { return len(r.filters) }

// RunTests compiles and runs the inline [tests] in a filter document, returning
// the names of failing cases (empty = all pass). Powers a `verify` command.
func RunTests(b []byte) (failures []string, err error) {
	var f File
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	for name, def := range f.Filters {
		c, err := Compile(name, def)
		if err != nil {
			return nil, err
		}
		for _, tc := range f.Tests[name] {
			got, _ := Apply(c, tc.Input)
			if strings.TrimRight(got, "\n") != strings.TrimRight(tc.Expected, "\n") {
				failures = append(failures, name+"/"+tc.Name)
			}
		}
	}
	sort.Strings(failures)
	return failures, nil
}
