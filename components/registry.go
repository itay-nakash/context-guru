package components

import (
	"fmt"
	"sort"
	"sync"
)

// Constructor builds a component instance. It receives the raw config block for
// its name (may be nil), so a component can be configured purely from YAML with
// no core change. Returning an error aborts pipeline construction.
type Constructor func(raw []byte) (Component, error)

var (
	regMu    sync.RWMutex
	registry = map[string]Constructor{}
)

// Register makes a component available by name. Called from each component's
// init(); double-registration or an empty name panics at boot (the
// database/sql pattern AuthBridge also uses).
func Register(name string, c Constructor) {
	if name == "" {
		panic("components: Register with empty name")
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("components: duplicate registration for " + name)
	}
	registry[name] = c
}

// Names lists every registered component name, sorted.
func Names() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// New builds a single component by name with its raw config block.
func New(name string, raw []byte) (Component, error) {
	regMu.RLock()
	ctor, ok := registry[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("components: unknown component %q (registered: %v)", name, Names())
	}
	return ctor(raw)
}
