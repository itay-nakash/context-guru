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
