package extract

import (
	"context"
	"strings"
	"testing"
)

// fixedModel returns a canned Starlark program.
type fixedModel struct{ prog string }

func (m fixedModel) Complete(context.Context, string) (string, error) { return m.prog, nil }

const body = `[{"id":1,"name":"keep this"},{"id":2,"name":"drop this"},{"id":3,"name":"keep that"}]`

// A valid filter that selects a contained subset is accepted (strategy "code").
func TestRunExtractionCodeAccepted(t *testing.T) {
	cfg := DefaultCfg()
	cfg.Mode = "code"
	m := fixedModel{prog: `data = json.decode(INPUT)
OUTPUT = json.encode([r for r in data if "keep" in r["name"]])`}
	out, strat := RunExtraction(context.Background(), body, "find keep records", nil, 5000, cfg, m)
	if strat != "code" {
		t.Fatalf("expected code strategy to win, got %q (out=%s)", strat, out)
	}
	if strings.Contains(out, "drop this") || !strings.Contains(out, "keep this") {
		t.Fatalf("code filter should keep only the keep records: %s", out)
	}
	if !IsContained(parseBody(out), parseBody(body)) {
		t.Fatal("accepted output must be a contained subset")
	}
}

// A filter that INVENTS data fails the containment check and must be rejected —
// falling back to the deterministic projection (never returning fabricated data).
func TestRunExtractionRejectsNonContained(t *testing.T) {
	cfg := DefaultCfg()
	cfg.Mode = "code"
	m := fixedModel{prog: `OUTPUT = json.encode([{"id":999,"name":"invented record"}])`}
	out, strat := RunExtraction(context.Background(), body, "find keep records", nil, 5000, cfg, m)
	if strings.Contains(out, "invented") {
		t.Fatalf("fabricated (non-contained) output must be rejected, got: %s", out)
	}
	if strat == "code" {
		t.Fatalf("non-contained code result must not win; strategy=%q", strat)
	}
}

// ContentKey ignores <<cg:…>> markers + whitespace so a re-sent body still hits cache.
func TestContentKeyMarkerInsensitive(t *testing.T) {
	a := ContentKey("some tool output here")
	b := ContentKey("some   tool output\n<<cg:abc123def456>> here")
	if a != b {
		t.Fatalf("ContentKey must be marker/whitespace-insensitive: %q vs %q", a, b)
	}
}

func TestHarvestIdentifiers(t *testing.T) {
	ids := HarvestIdentifiers("fix auth/session.py and test_auth_expiry (see issue 12345)", 40)
	joined := strings.Join(ids, " ")
	if !strings.Contains(joined, "auth/session.py") || !strings.Contains(joined, "test_auth_expiry") {
		t.Fatalf("expected paths/symbols harvested, got %v", ids)
	}
}
