package extract

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func parse(s string) any { return parseBody(s) }

func TestContainment(t *testing.T) {
	original := parse(`{"id":1,"name":"alpha","tags":["x","y","z"],"note":"hello world"}`)
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"subset keys", `{"id":1,"name":"alpha"}`, true},
		{"substring value", `{"note":"hello"}`, true},
		{"subsequence list", `{"tags":["x","z"]}`, true},
		{"drop everything (nil-ish)", `{}`, true},
		{"extra key", `{"id":1,"missing":true}`, false},
		{"paraphrased string", `{"name":"ALPHA"}`, false},
		{"changed number", `{"id":2}`, false},
		{"invented value", `{"name":"omega"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsContained(parse(c.out), original); got != c.want {
				t.Fatalf("IsContained(%s) = %v, want %v", c.out, got, c.want)
			}
		})
	}
}

func TestContainmentListSubsequenceOrder(t *testing.T) {
	orig := parse(`[{"id":1},{"id":2},{"id":3}]`)
	if !IsContained(parse(`[{"id":1},{"id":3}]`), orig) {
		t.Fatal("order-preserving subsequence should be contained")
	}
	if IsContained(parse(`[{"id":3},{"id":1}]`), orig) {
		t.Fatal("out-of-order subsequence must NOT be contained")
	}
}

func TestDeterministicProjectIsAlwaysContained(t *testing.T) {
	body := `[{"id":1,"name":"alpha","blob":"` + strings.Repeat("x", 200) + `"},
	          {"id":2,"name":"beta","blob":"` + strings.Repeat("y", 200) + `"}]`
	proj := DeterministicProject(parseBody(body), []string{"alpha"}, 4000)
	if !IsContained(proj, parseBody(body)) {
		t.Fatalf("deterministic projection must be contained: %v", proj)
	}
}

// fakeModel parses the INPUT embedded in the prompt and returns its first element
// (for arrays) — a content-aware, always-contained selection, like a well-behaved
// cheap model. Used to exercise the single + RLM paths.
type firstElemModel struct{}

func (firstElemModel) Complete(_ context.Context, prompt string) (string, error) {
	i := strings.Index(prompt, sampleMarker)
	if i < 0 {
		return "", nil
	}
	rest := prompt[i+len(sampleMarker):]
	end := strings.Index(rest, "\n\n")
	if end >= 0 {
		rest = rest[:end]
	}
	var arr []any
	if json.Unmarshal([]byte(rest), &arr) == nil && len(arr) > 0 {
		b, _ := json.Marshal(arr[:1])
		return string(b), nil
	}
	return rest, nil
}

// paraphraseModel always returns an invented value (fails containment).
type paraphraseModel struct{}

func (paraphraseModel) Complete(_ context.Context, _ string) (string, error) {
	return `[{"id":999,"name":"INVENTED"}]`, nil
}

func TestRunExtractionSingleSelects(t *testing.T) {
	body := `[{"id":1,"name":"alpha"},{"id":2,"name":"beta"},{"id":3,"name":"gamma"}]`
	cfg := DefaultCfg()
	cfg.Mode = "single"
	cfg.AllowDeterministic = false
	out, strat := RunExtraction(context.Background(), body, "find alpha", []string{"alpha"}, 100, cfg, firstElemModel{})
	if strat != "single" {
		t.Fatalf("strategy = %q, want single", strat)
	}
	if !IsContained(parseBody(out), parseBody(body)) {
		t.Fatalf("accepted result not contained: %s", out)
	}
	if len(out) >= len(body) {
		t.Fatalf("result not smaller: %d vs %d", len(out), len(body))
	}
}

func TestRunExtractionRejectsParaphraseFallsToDeterministic(t *testing.T) {
	// Records carry a bulky non-important "detail" field that deterministic drops.
	blob := strings.Repeat("noise ", 30)
	body := `[{"id":1,"detail":"` + blob + `"},{"id":2,"detail":"` + blob + `"}]`
	cfg := DefaultCfg()
	cfg.Mode = "single" // single fails containment, then deterministic runs
	out, strat := RunExtraction(context.Background(), body, "find alpha", []string{"alpha"}, 100, cfg, paraphraseModel{})
	if strat != "deterministic" {
		t.Fatalf("strategy = %q, want deterministic (paraphrase must be rejected)", strat)
	}
	if !IsContained(parseBody(out), parseBody(body)) {
		t.Fatalf("deterministic fallback not contained: %s", out)
	}
}

func TestRunExtractionRLMBatchedMergesChunks(t *testing.T) {
	// 50 uniform records -> chunked (size 20) -> 3 chunks -> first of each merged.
	var recs []string
	for i := 0; i < 50; i++ {
		recs = append(recs, `{"id":`+itoa(i)+`,"v":"rec`+itoa(i)+`"}`)
	}
	body := "[" + strings.Join(recs, ",") + "]"
	cfg := DefaultCfg()
	cfg.Mode = "rlm"
	cfg.AllowDeterministic = false
	out, strat := RunExtraction(context.Background(), body, "anything", nil, 100, cfg, firstElemModel{})
	if strat != "rlm" {
		t.Fatalf("strategy = %q, want rlm", strat)
	}
	if !IsContained(parseBody(out), parseBody(body)) {
		t.Fatalf("merged RLM result not contained: %s", out)
	}
	var merged []any
	if err := json.Unmarshal([]byte(out), &merged); err != nil {
		t.Fatalf("rlm output not a JSON array: %v", err)
	}
	if len(merged) != 3 { // one per chunk
		t.Fatalf("expected 3 merged records, got %d", len(merged))
	}
}

func TestTruncateValueDoesNotSplitRunes(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	out := truncateValue(s, 5).(string)
	if !utf8.ValidString(out) {
		t.Fatalf("truncation split a rune: %q", out)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
