package offload

import (
	"strings"
	"testing"

	"github.com/rossoctl/context-guru/components/dsl"
)

// apt output that carries a real problem must survive the filter. This test earned
// its place: it caught a '^debconf: ' strip rule that swallowed
// "debconf: unable to initialize frontend" along with the harmless delaying notice.
// Any new strip rule on a high-volume filter should be run past a list like this.
func TestAptKeepsProblems(t *testing.T) {
	var r dsl.Registry
	if err := r.Load([]byte(builtinFilters)); err != nil {
		t.Fatal(err)
	}
	boiler := strings.Repeat("Setting up libfoo:amd64 (1.2.3-1) ...\n", 40)
	for _, keep := range []string{
		"E: Unable to locate package nope",
		"E: Package 'foo' has no installation candidate",
		"dpkg: dependency problems prevent configuration of bar:",
		"W: Possible missing firmware /lib/firmware/x.bin",
		"Errors were encountered while processing:",
		"debconf: unable to initialize frontend: Dialog",
		"N: Ignoring file 'x.list.bak' in directory",
		"Do you want to continue? [Y/n]",
	} {
		in := boiler + keep + "\n" + boiler
		c := r.Match(selectorKey(in))
		if c == nil || c.Name != "apt" {
			t.Fatalf("routed to %v", c)
		}
		out, _ := dsl.Apply(c, in)
		if !strings.Contains(out, keep) {
			t.Errorf("apt filter DROPPED %q -> %q", keep, out)
		}
	}
	// and the pure-boilerplate case collapses hard
	if out, _ := dsl.Apply(r.Match(selectorKey(boiler)), boiler); out != "apt: install ok" {
		t.Fatalf("pure boilerplate should collapse, got %q", out)
	}
}

// Selectors must key on TOOL IDENTITY, not on a generic verb. Found in production:
// swift-build's bare '^Compiling ' claimed CYTHON output and stripped its
// "Compiling x.pyx because it changed" lines. These are real outputs from other tools
// that a tool-specific filter must not claim.
func TestSelectorsDoNotClaimForeignOutput(t *testing.T) {
	var r dsl.Registry
	if err := r.Load([]byte(builtinFilters)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, out string }{
		{"cython", "Compiling pkg/mod.pyx because it changed.\n[1/4] Cythonizing pkg/mod.pyx\n[2/4] Cythonizing pkg/other.pyx\nrunning build_ext\n"},
		{"cargo", "Compiling serde v1.0.197\nCompiling libc v0.2.153\n   Finished dev [unoptimized] target(s) in 4.21s\n"},
	} {
		if c := r.Match(selectorKey(tc.out)); c != nil && c.Name == "swift-build" {
			t.Errorf("%s output must not be claimed by swift-build", tc.name)
		}
	}
}
