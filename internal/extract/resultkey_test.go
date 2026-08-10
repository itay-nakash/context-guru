package extract

import (
	"strings"
	"testing"
)

// The result key must be GLOBAL: identical content under identical extractor semantics
// must produce the same key regardless of session, because an extraction is a
// context-free derived result. Session-scoping was throwing away ~80% of the available
// reuse (82 of 103 unique contents recurred across sessions).
func TestResultKeyIsSessionIndependent(t *testing.T) {
	cfg := DefaultCfg()
	ck := ContentKey("some tool output")
	// There is no session parameter at all — the type system enforces the property.
	if ResultKey(ck, "m", cfg) != ResultKey(ck, "m", cfg) {
		t.Fatal("the same content+model+cfg must map to one stable key")
	}
	if ResultKey(ck, "m", cfg) == ResultKey(ContentKey("different output"), "m", cfg) {
		t.Fatal("different content must map to different keys")
	}
}

// A prompt/extractor version bump must MISS rather than serve a stale extraction derived
// under different rules. Serving stale is worse than missing, because nothing surfaces it.
func TestResultKeyVersionBumpMisses(t *testing.T) {
	cfg := DefaultCfg()
	ck := ContentKey("output")
	if PromptVersion == "" {
		t.Fatal("PromptVersion must be non-empty or keys collide across prompt revisions")
	}
	// ResultKey delegates to resultKeyWithVersion, so a bumped version is exactly what a
	// future prompt revision will produce. It MUST miss the current key.
	current := ResultKey(ck, "m", cfg)
	bumped := resultKeyWithVersion(ck, "m", cfg, PromptVersion+"-next")
	if current == bumped {
		t.Fatal("a prompt/extractor version bump must MISS, not serve a stale extraction")
	}
	// And the live constant must be the one ResultKey actually uses.
	if current != resultKeyWithVersion(ck, "m", cfg, PromptVersion) {
		t.Fatal("ResultKey must hash the live PromptVersion")
	}
	// A different extractor model writes a different program, so it must miss too.
	if ResultKey(ck, "m2", cfg) == current {
		t.Fatal("model must be part of the key")
	}
}

// Config changes that steer the result must also miss — a result derived under
// rewrite:true is not the same artifact as one derived under rewrite:false.
func TestResultKeyConfigFingerprintMisses(t *testing.T) {
	ck := ContentKey("output")
	base := DefaultCfg()
	rewrite := DefaultCfg()
	rewrite.Rewrite = !base.Rewrite
	if ResultKey(ck, "m", base) == ResultKey(ck, "m", rewrite) {
		t.Fatal("rewrite mode must change the key: deletion-only and rewrite are different artifacts")
	}
	floor := DefaultCfg()
	floor.Floor = base.Floor + 1000
	if ResultKey(ck, "m", base) == ResultKey(ck, "m", floor) {
		t.Fatal("floor must change the key")
	}
	mode := DefaultCfg()
	mode.Mode = "single"
	if ResultKey(ck, "m", base) == ResultKey(ck, "m", mode) {
		t.Fatal("strategy mode must change the key")
	}
}

// AllowedStrategies is a SET, so its order must not change the key — otherwise the same
// config spelled two ways misses its own cache.
func TestResultKeyStrategyOrderInsensitive(t *testing.T) {
	ck := ContentKey("output")
	a := DefaultCfg()
	a.AllowedStrategies = []string{"code", "single"}
	b := DefaultCfg()
	b.AllowedStrategies = []string{"single", "code"}
	if ResultKey(ck, "m", a) != ResultKey(ck, "m", b) {
		t.Fatal("the same strategy SET spelled in a different order must map to one key")
	}
}

// The key components must be separated unambiguously, so no concatenation of two fields
// can be mistaken for another pair (a classic hash-composition bug).
func TestResultKeyComponentsCannotStraddle(t *testing.T) {
	cfg := DefaultCfg()
	// "ab"+"c" vs "a"+"bc" must not collide.
	if ResultKey("ab", "c", cfg) == ResultKey("a", "bc", cfg) {
		t.Fatal("key components must be length-unambiguous (separator required)")
	}
}

// The preamble split must not change the CONTENT the model sees — only its placement.
// Losing an instruction while "optimizing caching" would be a silent quality regression.
func TestPromptSplitPreservesContent(t *testing.T) {
	body := `[{"id":1,"name":"keep"}]`
	goal := "find the keep records"
	keep := []string{"keep"}
	for _, rewrite := range []bool{true, false} {
		sys, user := buildCodePromptSplit(body, goal, keep, rewrite)
		single := buildCodePrompt(body, goal, keep, rewrite)
		for _, want := range []string{"Starlark", "OUTPUT", "SUMMARY", "INPUT"} {
			if !strings.Contains(sys+user, want) {
				t.Fatalf("rewrite=%v: split prompt lost %q", rewrite, want)
			}
		}
		// Every substantive chunk of the single-message prompt must survive somewhere.
		if len(sys)+len(user) < len(single)-200 {
			t.Fatalf("rewrite=%v: split prompt is %d chars vs single %d — content lost",
				rewrite, len(sys)+len(user), len(single))
		}
		// The invariant half must NOT contain the per-call variable data, or it cannot cache.
		if strings.Contains(sys, goal) || strings.Contains(sys, body) {
			t.Fatalf("rewrite=%v: system block must be invariant (found goal/body in it)", rewrite)
		}
		// And the variable half must carry them.
		if !strings.Contains(user, goal) || !strings.Contains(user, "keep") {
			t.Fatalf("rewrite=%v: user part must carry the goal and keep-list", rewrite)
		}
	}
	// The two rewrite modes must produce different (but each stable) preambles.
	sysA, _ := buildCodePromptSplit(body, goal, keep, true)
	sysB, _ := buildCodePromptSplit(body, goal, keep, false)
	if sysA == sysB {
		t.Fatal("rewrite and deletion-only contracts must differ")
	}
	// Stability: same inputs, same bytes — the property caching depends on.
	sysA2, _ := buildCodePromptSplit("totally different body", "different goal", nil, true)
	if sysA != sysA2 {
		t.Fatal("the system preamble must be byte-identical across calls (else it never caches)")
	}
}
