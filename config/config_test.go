package config

import (
	"strings"
	"testing"
)

func TestLoadStrictRejectsUnknownFields(t *testing.T) {
	_, err := LoadBytes([]byte("pipeline: [format]\nbogus_key: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "bogus_key") {
		t.Fatalf("expected strict rejection of unknown field, got %v", err)
	}
}

func TestPresetExpandsPipeline(t *testing.T) {
	c, err := LoadBytes([]byte("preset: balanced\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Pipeline) == 0 || c.Pipeline[0] != "format" {
		t.Fatalf("preset did not expand pipeline: %v", c.Pipeline)
	}
}

func TestExplicitPipelineOverridesPreset(t *testing.T) {
	c, err := LoadBytes([]byte("preset: balanced\npipeline: [cacheinject]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Pipeline) != 1 || c.Pipeline[0] != "cacheinject" {
		t.Fatalf("explicit pipeline should win: %v", c.Pipeline)
	}
}

func TestUnknownPresetErrors(t *testing.T) {
	if _, err := LoadBytes([]byte("preset: nope\n")); err == nil {
		t.Fatal("expected error for unknown preset")
	}
}

func TestStoreOptionsParse(t *testing.T) {
	c, err := LoadBytes([]byte("store: {ttl_seconds: 60, max_entries: 5}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Store.TTLSeconds != 60 || c.Store.MaxEntries != 5 {
		t.Fatalf("store options not parsed: %+v", c.Store)
	}
}
