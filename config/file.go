package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// fileConfig is the on-disk YAML shape. It is intentionally separate from Settings:
// the file groups knobs under stage sections (reduce/extract/cache) and carries the
// named component-selection lists, which we then fold onto a Settings derived from the
// chosen preset. Pointers distinguish "key absent" (keep preset/default) from "key set
// to the zero value" (an explicit override).
//
// Extending the file is a one-line change: a new reducer/encoder/extract-strategy is
// referenced purely by NAME in the relevant list (reduce.reducers, reduce.encoders,
// extract.strategies) — see configs/lab-cx.yaml. No new struct field is needed to wire
// a newly-registered component on or off.
type fileConfig struct {
	Preset string   `yaml:"preset"`
	Stages []string `yaml:"stages"`

	Reduce *struct {
		Enabled               *bool    `yaml:"enabled"`
		ProtectRecent         *int     `yaml:"protect_recent"`
		ProtectRecentToolUses *int     `yaml:"protect_recent_tool_uses"`
		ProvableOnly          *bool    `yaml:"provable_only"`
		CollapseOutputs       *bool    `yaml:"collapse_outputs"`
		ContextLimit          *int     `yaml:"context_limit"`
		ReduceCachedPrefix    *bool    `yaml:"reduce_cached_prefix"`
		CmdFilter             *bool    `yaml:"cmd_filter"`
		RehydrateOnCompaction *bool    `yaml:"rehydrate_on_compaction"`
		Reducers              []string `yaml:"reducers"`
		Encoders              []string `yaml:"encoders"`
	} `yaml:"reduce"`

	Extract *struct {
		Enabled        *bool    `yaml:"enabled"`
		Mode           string   `yaml:"mode"`
		Strategies     []string `yaml:"strategies"`
		Floor          *int     `yaml:"floor"`
		StructuredOnly *bool    `yaml:"structured_only"`
		// Provider/model/auth/base are transport concerns the proxy reads off the loaded
		// config; the engine core ignores them. They live here so a single file fully
		// describes a run.
		Provider string `yaml:"provider"`
		Model    string `yaml:"model"`
		Auth     string `yaml:"auth"`
		Base     string `yaml:"base"`
	} `yaml:"extract"`

	Cache *struct {
		Enabled         *bool `yaml:"enabled"`
		Breakpoints     *int  `yaml:"breakpoints"`
		StableGap       *int  `yaml:"stable_gap"`
		ToolsBreakpoint *bool `yaml:"tools_breakpoint"`
	} `yaml:"cache"`
}

// Transport holds the extraction model's transport knobs parsed from a config file.
// The engine ignores these; the proxy uses them to construct the cheap-model client.
// Empty fields mean "fall back to flag/default".
type Transport struct {
	Provider string
	Model    string
	Auth     string
	Base     string
}

// Load reads a YAML config file and folds it onto the preset-derived Settings. Unknown
// top-level keys are an error (strict). Missing keys fall back to the preset/defaults.
// It returns the resolved Settings; use LoadWithTransport when the extraction model's
// provider/model/auth/base are also needed.
func Load(path string) (Settings, error) {
	s, _, err := LoadWithTransport(path)
	return s, err
}

// LoadWithTransport is Load plus the extraction transport block (provider/model/auth/
// base) the proxy needs to construct the cheap-model client.
func LoadWithTransport(path string) (Settings, Transport, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, Transport{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // strict: unknown keys are an error
	if err := dec.Decode(&fc); err != nil {
		return Settings{}, Transport{}, fmt.Errorf("config: parse %s: %w", path, err)
	}

	s := Preset(fc.Preset)
	if fc.Stages != nil {
		s.Stages = fc.Stages
	}

	if r := fc.Reduce; r != nil {
		setBool(&s.ReduceEnabled, r.Enabled)
		setInt(&s.ProtectRecent, r.ProtectRecent)
		setInt(&s.ProtectRecentToolUses, r.ProtectRecentToolUses)
		setBool(&s.ProvableOnly, r.ProvableOnly)
		setBool(&s.CollapseOutputs, r.CollapseOutputs)
		setInt(&s.ContextLimit, r.ContextLimit)
		setBool(&s.ReduceCachedPrefix, r.ReduceCachedPrefix)
		setBool(&s.CmdFilter, r.CmdFilter)
		setBool(&s.RehydrateOnCompaction, r.RehydrateOnCompaction)
		if r.Reducers != nil {
			s.Reducers = r.Reducers
		}
		if r.Encoders != nil {
			s.Encoders = r.Encoders
		}
	}

	if e := fc.Extract; e != nil {
		setBool(&s.ExtractEnabled, e.Enabled)
		if e.Mode != "" {
			s.ExtractMode = e.Mode
		}
		setInt(&s.LLMCompactFloor, e.Floor)
		setBool(&s.LLMCompactStructuredOnly, e.StructuredOnly)
		if e.Strategies != nil {
			s.ExtractStrategies = e.Strategies
		}
	}

	if c := fc.Cache; c != nil {
		setBool(&s.CacheEnabled, c.Enabled)
		setInt(&s.CacheBreakpoints, c.Breakpoints)
		setInt(&s.CacheStableGap, c.StableGap)
		setBool(&s.CacheToolsBreakpoint, c.ToolsBreakpoint)
	}

	var tr Transport
	if e := fc.Extract; e != nil {
		tr = Transport{Provider: e.Provider, Model: e.Model, Auth: e.Auth, Base: e.Base}
	}
	return s, tr, nil
}

func setBool(dst *bool, v *bool) {
	if v != nil {
		*dst = *v
	}
}

func setInt(dst *int, v *int) {
	if v != nil {
		*dst = *v
	}
}
