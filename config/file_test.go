package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeTmp writes content to a temp YAML file and returns its path.
func writeTmp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

// TestLoadDisablesExtractAndSelectsComponents is the spec'd acceptance test: a YAML
// that disables extract and enables only the toon encoder + only the collapse reducer
// must produce Settings/Components that reflect exactly that.
func TestLoadDisablesExtractAndSelectsComponents(t *testing.T) {
	p := writeTmp(t, `
preset: balanced
reduce:
  reducers: [collapse]
  encoders: [toon]
extract:
  enabled: false
`)
	s, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.ExtractEnabled {
		t.Errorf("ExtractEnabled = true, want false")
	}
	if !reflect.DeepEqual(s.Encoders, []string{"toon"}) {
		t.Errorf("Encoders = %v, want [toon]", s.Encoders)
	}
	if !reflect.DeepEqual(s.Reducers, []string{"collapse"}) {
		t.Errorf("Reducers = %v, want [collapse]", s.Reducers)
	}
}

// TestLoadFullShape exercises every documented key and the preset base + overrides.
func TestLoadFullShape(t *testing.T) {
	p := writeTmp(t, `
preset: balanced
compactors: [reduce, extract, cache]
reduce:
  protect_recent: 5
  provable_only: false
  reducers: [collapse, skeleton, format, dedup, cmdfilter]
  encoders: [json_compact, toon, csv]
extract:
  enabled: true
  mode: single
  strategies: [code, single, deterministic]
  floor: 1234
cache:
  enabled: true
  breakpoints: 7
`)
	s, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.ProtectRecent != 5 {
		t.Errorf("ProtectRecent = %d, want 5", s.ProtectRecent)
	}
	if s.ProvableOnly {
		t.Errorf("ProvableOnly = true, want false (override)")
	}
	if !reflect.DeepEqual(s.Compactors, []string{"reduce", "extract", "cache"}) {
		t.Errorf("Compactors = %v", s.Compactors)
	}
	if !reflect.DeepEqual(s.ExtractStrategies, []string{"code", "single", "deterministic"}) {
		t.Errorf("ExtractStrategies = %v", s.ExtractStrategies)
	}
	if !s.ExtractEnabled {
		t.Errorf("ExtractEnabled = false, want true")
	}
	if s.ExtractMode != "single" {
		t.Errorf("ExtractMode = %q, want single", s.ExtractMode)
	}
	if s.LLMCompactFloor != 1234 {
		t.Errorf("LLMCompactFloor = %d, want 1234", s.LLMCompactFloor)
	}
	if s.CacheBreakpoints != 7 {
		t.Errorf("CacheBreakpoints = %d, want 7", s.CacheBreakpoints)
	}
}

// TestLoadCompactorsAndTransports exercises the summarize/truncate blocks and the
// per-compactor LLM transport + credential fields, returned via LoadFull.
func TestLoadCompactorsAndTransports(t *testing.T) {
	p := writeTmp(t, `
preset: balanced
compactors: [extract, summarize, truncate, cache]
extract:
  enabled: true
  source: config
  provider: anthropic
  model: claude-haiku-4-5
  api_key: sk-inline
summarize:
  enabled: true
  level: highly_detailed
  keep_last: 4
  trigger_tokens: 5000
  source: incoming
  provider: openai
  model: gpt-4o-mini
  key_env: MY_SUM_KEY
truncate:
  enabled: true
  keep_last: 8
  trigger_tokens: 12000
`)
	s, tr, err := LoadFull(p)
	if err != nil {
		t.Fatalf("LoadFull: %v", err)
	}
	if !reflect.DeepEqual(s.Compactors, []string{"extract", "summarize", "truncate", "cache"}) {
		t.Errorf("Compactors = %v", s.Compactors)
	}
	if !s.SummarizeEnabled || s.SummarizeLevel != "highly_detailed" || s.SummarizeKeepLast != 4 || s.SummarizeTriggerTokens != 5000 {
		t.Errorf("summarize settings not folded: %+v", s)
	}
	if !s.TruncateEnabled || s.TruncateKeepLast != 8 || s.TruncateTriggerTokens != 12000 {
		t.Errorf("truncate settings not folded: %+v", s)
	}
	if tr.Extract.Source != "config" || tr.Extract.Model != "claude-haiku-4-5" || tr.Extract.APIKey != "sk-inline" {
		t.Errorf("extract transport = %+v", tr.Extract)
	}
	if tr.Summarize.Source != "incoming" || tr.Summarize.Provider != "openai" || tr.Summarize.KeyEnv != "MY_SUM_KEY" {
		t.Errorf("summarize transport = %+v", tr.Summarize)
	}
}

// TestLoadDefaultsFromPreset: missing keys fall back to the preset/defaults.
func TestLoadDefaultsFromPreset(t *testing.T) {
	p := writeTmp(t, `preset: safe`)
	s, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Preset("safe")
	if s.ProvableOnly != want.ProvableOnly || s.CollapseOutputs != want.CollapseOutputs {
		t.Errorf("safe preset not applied: got ProvableOnly=%v CollapseOutputs=%v",
			s.ProvableOnly, s.CollapseOutputs)
	}
	if s.CacheBreakpoints != want.CacheBreakpoints {
		t.Errorf("CacheBreakpoints = %d, want preset %d", s.CacheBreakpoints, want.CacheBreakpoints)
	}
}

// TestLoadStrictUnknownKey: an unknown top-level key is an error.
func TestLoadStrictUnknownKey(t *testing.T) {
	p := writeTmp(t, `
preset: balanced
bogus_key: 1
`)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error on unknown top-level key, got nil")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("/no/such/file.yaml"); err == nil {
		t.Fatal("expected error for missing file")
	}
}
