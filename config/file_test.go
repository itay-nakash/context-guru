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
stages: [reduce, extract, cache]
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
	if !reflect.DeepEqual(s.Stages, []string{"reduce", "extract", "cache"}) {
		t.Errorf("Stages = %v", s.Stages)
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
