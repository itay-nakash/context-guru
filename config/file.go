package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// fileConfig is the on-disk YAML shape. It is intentionally separate from Settings:
// the file groups knobs under compactor sections (reduce/extract/summarize/truncate/
// cache) and carries the named component-selection lists, which we then fold onto a
// Settings derived from the chosen preset. Pointers distinguish "key absent" (keep
// preset/default) from "key set to the zero value" (an explicit override).
//
// Extending the file is a one-line change: a new reducer/encoder/extract-strategy is
// referenced purely by NAME in the relevant list (reduce.reducers, reduce.encoders,
// extract.strategies, compactors) — see configs/lab-cx.yaml. No new struct field is
// needed to wire a newly-registered component on or off.
type fileConfig struct {
	Preset     string   `yaml:"preset"`
	Compactors []string `yaml:"compactors"`

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
		Enabled            *bool    `yaml:"enabled"`
		Mode               string   `yaml:"mode"`
		Strategies         []string `yaml:"strategies"`
		Floor              *int     `yaml:"floor"`
		StructuredOnly     *bool    `yaml:"structured_only"`
		modelTransportYAML `yaml:",inline"`
	} `yaml:"extract"`

	Summarize *struct {
		Enabled            *bool  `yaml:"enabled"`
		Level              string `yaml:"level"`
		KeepLast           *int   `yaml:"keep_last"`
		TriggerTokens      *int   `yaml:"trigger_tokens"`
		modelTransportYAML `yaml:",inline"`
	} `yaml:"summarize"`

	Truncate *struct {
		Enabled       *bool `yaml:"enabled"`
		KeepLast      *int  `yaml:"keep_last"`
		TriggerTokens *int  `yaml:"trigger_tokens"`
	} `yaml:"truncate"`

	Cache *struct {
		Enabled         *bool `yaml:"enabled"`
		Breakpoints     *int  `yaml:"breakpoints"`
		StableGap       *int  `yaml:"stable_gap"`
		ToolsBreakpoint *bool `yaml:"tools_breakpoint"`
	} `yaml:"cache"`
}

// modelTransportYAML is the LLM-model transport block shared by every LLM compactor
// (extract, summarize). The engine core ignores these; the proxy reads them to build
// the cheap-model client. Inlined into each compactor's YAML section.
type modelTransportYAML struct {
	Source   string `yaml:"source"` // "config" (default) | "incoming"
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Auth     string `yaml:"auth"`
	Base     string `yaml:"base"`
	APIKey   string `yaml:"api_key"` // inline secret (allowed); prefer key_env for hygiene
	KeyEnv   string `yaml:"key_env"` // env var name to read the key from
}

// ModelTransport holds one LLM compactor's transport + credentials parsed from a config
// file. The engine ignores these; the proxy uses them to construct the model client.
// Empty fields fall back to flag/default. Source "incoming" reuses the proxied request's
// own model + credentials instead of a configured static client.
type ModelTransport struct {
	Source   string
	Provider string
	Model    string
	Auth     string
	Base     string
	APIKey   string
	KeyEnv   string
}

func (m modelTransportYAML) transport() ModelTransport {
	return ModelTransport{
		Source: m.Source, Provider: m.Provider, Model: m.Model,
		Auth: m.Auth, Base: m.Base, APIKey: m.APIKey, KeyEnv: m.KeyEnv,
	}
}

// Transports carries the per-compactor model transports the proxy needs to construct
// model clients.
type Transports struct {
	Extract   ModelTransport
	Summarize ModelTransport
}

// Load reads a YAML config file and folds it onto the preset-derived Settings. Unknown
// top-level keys are an error (strict). Missing keys fall back to the preset/defaults.
func Load(path string) (Settings, error) {
	s, _, err := LoadFull(path)
	return s, err
}

// LoadFull is Load plus the per-compactor model transports (provider/model/auth/base/
// credentials/source) the proxy needs to construct the model clients.
func LoadFull(path string) (Settings, Transports, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, Transports{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // strict: unknown keys are an error
	if err := dec.Decode(&fc); err != nil {
		return Settings{}, Transports{}, fmt.Errorf("config: parse %s: %w", path, err)
	}

	s := Preset(fc.Preset)
	if fc.Compactors != nil {
		s.Compactors = fc.Compactors
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

	if su := fc.Summarize; su != nil {
		setBool(&s.SummarizeEnabled, su.Enabled)
		if su.Level != "" {
			s.SummarizeLevel = su.Level
		}
		setInt(&s.SummarizeKeepLast, su.KeepLast)
		setInt(&s.SummarizeTriggerTokens, su.TriggerTokens)
	}

	if tr := fc.Truncate; tr != nil {
		setBool(&s.TruncateEnabled, tr.Enabled)
		setInt(&s.TruncateKeepLast, tr.KeepLast)
		setInt(&s.TruncateTriggerTokens, tr.TriggerTokens)
	}

	if c := fc.Cache; c != nil {
		setBool(&s.CacheEnabled, c.Enabled)
		setInt(&s.CacheBreakpoints, c.Breakpoints)
		setInt(&s.CacheStableGap, c.StableGap)
		setBool(&s.CacheToolsBreakpoint, c.ToolsBreakpoint)
	}

	var t Transports
	if e := fc.Extract; e != nil {
		t.Extract = e.transport()
	}
	if su := fc.Summarize; su != nil {
		t.Summarize = su.transport()
	}
	return s, t, nil
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
