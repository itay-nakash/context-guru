// Package config loads context-guru's configuration and builds a pipeline from
// it. One strict YAML struct serves both hosts (design D9): the proxy loads a
// file; the AuthBridge plugin hands its config: subtree to LoadBytes; a k8s
// ConfigMap/CRD just renders the same YAML.
//
// The pipeline: name-list controls order + enablement. Each component's own
// typed config lives under components:<name>; it's handed to the component's
// constructor verbatim, so adding a component makes it configurable with no
// change here. A preset expands to a default pipeline + component configs,
// which the explicit fields then override.
package config

import (
	"bytes"
	"fmt"
	"os"

	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/store"
	"gopkg.in/yaml.v3"
)

// Config is the whole configuration document.
type Config struct {
	Preset     string               `yaml:"preset"`
	Pipeline   []string             `yaml:"pipeline"`
	Components map[string]yaml.Node `yaml:"components"`
	Store      store.Options        `yaml:"store"`
}

// Load reads and parses a YAML config file (strict: unknown keys are rejected).
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return LoadBytes(b)
}

// LoadBytes parses a YAML config document (strict). Used by the AuthBridge
// plugin's Configure, which receives its subtree as bytes.
func LoadBytes(b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // reject typos loudly
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := c.applyPreset(); err != nil {
		return nil, err
	}
	return &c, nil
}

// applyPreset fills an empty Pipeline from the named preset. Explicit fields in
// the document always win (they were already decoded).
func (c *Config) applyPreset() error {
	if c.Preset == "" {
		return nil
	}
	p, ok := presets[c.Preset]
	if !ok {
		return fmt.Errorf("config: unknown preset %q", c.Preset)
	}
	if len(c.Pipeline) == 0 {
		c.Pipeline = append([]string(nil), p...)
	}
	return nil
}

// presets map a name to a default pipeline (component names in run order). The
// referenced components are registered by P1+; an unknown name surfaces at
// Build time as a clear error.
var presets = map[string][]string{
	"off":        {}, // passthrough: no components (baseline / A-B control)
	"safe":       {"format", "cacheinject"},
	"balanced":   {"format", "dedup", "failed_run", "cmdfilter", "cacheinject"},
	"aggressive": {"format", "dedup", "failed_run", "cmdfilter", "smartcrush", "extract", "cacheinject"},
	"coding":     {"format", "skeleton", "cmdfilter", "cacheinject"},
	"mcp":        {"format", "smartcrush", "cacheinject"},
	// agent: tuned for long agentic sessions (e.g. Claude Code on SWE-bench),
	// where the dominant cost is the transcript of tool outputs (file reads)
	// re-sent every turn. mask (drop old tool outputs) is the biggest lever
	// there — ~27% content-token savings with no task-reward loss in the
	// eval-containers SWE-bench sweep (see docs/RESULTS.md); extract + failed_run
	// + dedup add relevance/supersession/dup wins; cacheinject keeps the prefix
	// cacheable. Order: lossless first, then offload old-then-large, cache last.
	"agent": {"format", "dedup", "failed_run", "mask", "extract", "cacheinject"},
	// summarize restructures the whole transcript (changes the message count) — run
	// it alone so no other component's in-place edits race apply's rebuild.
	"summarize": {"summarize"},
}

// Build constructs the ordered pipeline from the config, wiring each named
// component with its raw config block.
func (c *Config) Build(e components.Emitter) (*components.Pipeline, error) {
	comps := make([]components.Component, 0, len(c.Pipeline))
	for _, name := range c.Pipeline {
		var raw []byte
		if node, ok := c.Components[name]; ok {
			b, err := yaml.Marshal(&node)
			if err != nil {
				return nil, fmt.Errorf("config: marshal %q block: %w", name, err)
			}
			raw = b
		}
		comp, err := components.New(name, raw)
		if err != nil {
			return nil, err
		}
		comps = append(comps, comp)
	}
	return components.NewPipeline(comps, e), nil
}

// NewStore builds the configured state store (in-memory for v1).
func (c *Config) NewStore() store.Store { return store.NewMemory(c.Store) }
