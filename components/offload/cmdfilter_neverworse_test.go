package offload

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/components/dsl"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
	"gopkg.in/yaml.v3"
)

// never_worse at the message level: for EVERY shipped filter, running its own test
// inputs through the component must never make a message larger. The marker costs
// tokens too, so a filter that barely wins can still grow the message — which is why
// the guard compares the marker-INCLUSIVE rewrite. A wider selector routes more
// output to more filters, so this is worth asserting across the whole set.
func TestNeverWorseAcrossEveryBuiltin(t *testing.T) {
	var f dsl.File
	if err := yaml.Unmarshal([]byte(builtinFilters), &f); err != nil {
		t.Fatal(err)
	}
	comp := newFilterComp(t, "min_size: 1\n") // ignore the floor so every case is exercised
	for name, cases := range f.Tests {
		for _, tc := range cases {
			if strings.TrimSpace(tc.Input) == "" {
				continue
			}
			req := &schemas.BifrostChatRequest{Provider: schemas.Anthropic, Input: []schemas.ChatMessage{cmdToolMsg(tc.Input)}}
			c := &components.Ctx{Ctx: context.Background(), Session: "s", Store: store.NewMemory(store.Options{}), MaxCachedIdx: -1}
			if _, err := comp.Offload(req, &components.Report{}, c); err != nil {
				t.Fatal(err)
			}
			before := schema.TextTokens(tc.Input)
			after := schema.TextTokens(schema.MessageText(req.Input[0]))
			if after > before {
				t.Errorf("%s/%s GREW the message: %d -> %d tokens", name, tc.Name, before, after)
			}
		}
	}
}
