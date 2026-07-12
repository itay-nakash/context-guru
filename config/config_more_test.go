package config

import (
	"testing"

	_ "github.com/kagenti/context-guru/components/all"
)

func TestBuildUnknownComponentErrors(t *testing.T) {
	c, err := LoadBytes([]byte("pipeline: [nonesuch]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Build(nil); err == nil {
		t.Fatal("Build must error on an unregistered component")
	}
}

func TestBuildEmptyPipeline(t *testing.T) {
	c, _ := LoadBytes([]byte("preset: off\n"))
	p, err := c.Build(nil)
	if err != nil || p == nil {
		t.Fatalf("the off preset should build a no-op pipeline: %v", err)
	}
}

// TestBuildMarshalsComponentBlock exercises the components:<name> config-block
// marshal path in Build (the raw block is handed to the constructor).
func TestBuildMarshalsComponentBlock(t *testing.T) {
	c, err := LoadBytes([]byte("pipeline: [collapse]\ncomponents:\n  collapse: {max_tokens: 123}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Build(nil); err != nil {
		t.Fatalf("build with a component config block failed: %v", err)
	}
}

func TestNewStoreNonNil(t *testing.T) {
	c, _ := LoadBytes([]byte("store: {ttl_seconds: 5}\n"))
	if c.NewStore() == nil {
		t.Fatal("NewStore returned nil")
	}
}
