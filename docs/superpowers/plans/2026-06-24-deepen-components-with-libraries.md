# Deepen Naive Components With Established Libraries — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the from-scratch / stubbed pieces of `lab-context-engineering` with established libraries — real tokenizer, tree-sitter symbol+skeleton extraction, TOON encoding, and a real LLM-writes-code-in-a-sandbox extractor — and fix the verified non-dependency bugs.

**Architecture:** Keep the engine/stage/surface boundaries intact. Swap implementations behind the existing internal package APIs so callers don't change: `tokens.Count`, `reduce` signals/skeleton/format, `extract` strategies, `cheapmodel`. New leaf package `internal/treesitter` wraps the CGO binding. The Starlark extractor has the cheap model write a Starlark filter run over the FULL parsed output, verified by the existing `extract.IsContained` (fail-open).

**Tech Stack:** Go 1.25 (now **CGO-enabled**), `github.com/tiktoken-go/tokenizer`, `github.com/tree-sitter/go-tree-sitter` + `github.com/alexaandru/go-sitter-forest`, `github.com/toon-format/toon-go` (pinned), `go.starlark.net/starlark`.

## Global Constraints

- Go module `github.com/rossoctl/lab-context-engineering`, Go 1.25. `make fmt lint test build` must stay green; lint = `go vet` + `gofmt -l` clean.
- **CGO is now required** (tree-sitter): CI sets `CGO_ENABLED=1` with a C toolchain; the Docker final stage moves from `distroless/static` to `distroless/base-debian12:nonroot` (glibc present).
- **Fail-open is non-negotiable:** any error in any stage/strategy forwards the original content untouched. Every reduction stays reversible (markers + rewind store).
- DCO sign-off on every commit (`git commit -s`); author Osher Elhadad; `Assisted-By:` trailer, never `Co-Authored-By`. Conventional-commit titles.
- New dependencies are pinned in `go.mod`; `toon-go` has no releases — pin an exact commit (`go get github.com/toon-format/toon-go@<commit>`).
- Public API (`engine`, `surfaces`, `config`, `canon`, `observability`) signatures must not break — these tasks change internals only.

---

### Task 1: Accurate tokenizer (replace chars/4)

**Files:**
- Modify: `internal/tokens/tokens.go`
- Test: `internal/tokens/tokens_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: nothing.
- Produces: `tokens.Count(text string) int` (unchanged signature) — now BPE-accurate.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/tiktoken-go/tokenizer@latest`
Expected: `go.mod` gains the require line.

- [ ] **Step 2: Write the failing test**

```go
package tokens

import "testing"

func TestCountIsBPENotCharQuarter(t *testing.T) {
	// "hello world" is 2 BPE tokens in cl100k/o200k, not len/4 == 2..3 by luck;
	// use a case where chars/4 is clearly wrong: repeated punctuation.
	got := Count("!!!!!!!!!!!!!!!!")
	if got == len("!!!!!!!!!!!!!!!!")/4 {
		t.Fatalf("Count still looks like chars/4: %d", got)
	}
	if got <= 0 {
		t.Fatalf("Count returned %d", got)
	}
}

func TestCountEmpty(t *testing.T) {
	if Count("") != 0 {
		t.Fatal("empty must be 0")
	}
}

func TestCountStableAcrossCalls(t *testing.T) {
	a, b := Count("the quick brown fox"), Count("the quick brown fox")
	if a != b {
		t.Fatalf("non-deterministic: %d vs %d", a, b)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/tokens/ -run TestCountIsBPE -v`
Expected: FAIL (current impl is chars/4).

- [ ] **Step 4: Implement with tiktoken-go, lazy-initialized**

```go
// Package tokens estimates token counts using a real BPE tokenizer (o200k_base,
// the modern GPT family encoding) — an accurate offline proxy. The provider's
// usage remains authoritative; this drives reduction gating and never-inflate guards.
package tokens

import (
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

var (
	encOnce sync.Once
	enc     tokenizer.Codec
)

func codec() tokenizer.Codec {
	encOnce.Do(func() {
		// o200k_base is embedded in the binary (pure-Go, offline, no CGO).
		enc, _ = tokenizer.Get(tokenizer.O200kBase)
	})
	return enc
}

// Count returns the BPE token count of text (0 for empty). Falls back to a
// chars/4 estimate only if the tokenizer failed to initialize.
func Count(text string) int {
	if text == "" {
		return 0
	}
	c := codec()
	if c == nil {
		return (len(text) + 3) / 4
	}
	ids, _, err := c.Encode(text)
	if err != nil {
		return (len(text) + 3) / 4
	}
	return len(ids)
}
```

- [ ] **Step 5: Run tests + vet**

Run: `go test ./internal/tokens/ -v && go vet ./internal/tokens/`
Expected: PASS. Then `go test ./...` to confirm thresholds elsewhere still pass (adjust any test that hard-coded a chars/4 number — none currently assert absolute token counts).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/tokens/
git commit -s -m "feat(tokens): use tiktoken-go o200k_base instead of chars/4 estimate"
```

---

### Task 2: tree-sitter foundation package

**Files:**
- Create: `internal/treesitter/treesitter.go`
- Test: `internal/treesitter/treesitter_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces:
  - `treesitter.LangForExt(path string) string` — returns a grammar name ("go","python",...) or "".
  - `treesitter.Parse(lang string, src []byte) (*sitter.Tree, *sitter.Language, bool)` — parsed tree + language, ok=false on unknown lang / parse failure (fail-open).
  - Callers must `defer tree.Close()`.

- [ ] **Step 1: Add dependencies**

Run:
```
go get github.com/tree-sitter/go-tree-sitter@latest
go get github.com/alexaandru/go-sitter-forest@latest
```
Expected: both in `go.mod`. (forest provides `forest.GetLanguage(name)` across ~490 grammars; import the root package — binary-size note below.)

- [ ] **Step 2: Write the failing test**

```go
package treesitter

import "testing"

func TestLangForExt(t *testing.T) {
	cases := map[string]string{"a.go": "go", "b.py": "python", "c.ts": "typescript", "d.txt": ""}
	for path, want := range cases {
		if got := LangForExt(path); got != want {
			t.Fatalf("LangForExt(%q)=%q want %q", path, got, want)
		}
	}
}

func TestParseGo(t *testing.T) {
	tree, lang, ok := Parse("go", []byte("package p\nfunc Foo() int { return 1 }\n"))
	if !ok {
		t.Fatal("expected parse ok for valid Go")
	}
	defer tree.Close()
	if lang == nil || tree.RootNode().HasError() {
		t.Fatal("unexpected nil language or parse error")
	}
}

func TestParseUnknownLangFailsOpen(t *testing.T) {
	if _, _, ok := Parse("klingon", []byte("x")); ok {
		t.Fatal("unknown language must return ok=false")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `CGO_ENABLED=1 go test ./internal/treesitter/ -v`
Expected: FAIL (package doesn't exist).

- [ ] **Step 4: Implement the wrapper**

```go
// Package treesitter wraps the tree-sitter binding for the reduce signals and
// skeletonizer. CGO-backed; fail-open (unknown language / parse error => ok=false).
package treesitter

import (
	"path"
	"strings"

	forest "github.com/alexaandru/go-sitter-forest"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

var extToLang = map[string]string{
	".go": "go", ".py": "python", ".js": "javascript", ".jsx": "javascript",
	".ts": "typescript", ".tsx": "tsx", ".rs": "rust", ".java": "java",
	".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".hpp": "cpp",
	".rb": "ruby", ".php": "php", ".cs": "c_sharp", ".kt": "kotlin",
	".swift": "swift", ".scala": "scala",
}

// LangForExt maps a file path to a tree-sitter grammar name, or "" if unsupported.
func LangForExt(p string) string {
	return extToLang[strings.ToLower(path.Ext(p))]
}

// Parse parses src under the named grammar. ok=false on unknown grammar or a nil
// language; the caller treats that as "skip" (fail-open). Caller must Close the tree.
func Parse(lang string, src []byte) (*sitter.Tree, *sitter.Language, bool) {
	raw := forest.GetLanguage(lang)
	if raw == nil {
		return nil, nil, false
	}
	language := sitter.NewLanguage(raw)
	if language == nil {
		return nil, nil, false
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(language); err != nil {
		return nil, nil, false
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil, nil, false
	}
	return tree, language, true
}
```

> Note: `forest.GetLanguage(name)` links all grammars (large binary). If binary size matters, replace with a `switch` over per-language subpackages (`.../go-sitter-forest/golang`, `/python`, …) — same `*sitter.Language`. Keep the root import for v1 simplicity; track size in the commit message.

- [ ] **Step 5: Run tests + vet**

Run: `CGO_ENABLED=1 go test ./internal/treesitter/ -v && go vet ./internal/treesitter/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/treesitter/
git commit -s -m "feat(treesitter): CGO tree-sitter wrapper (LangForExt + Parse, fail-open)"
```

---

### Task 3: Real symbol extraction (replace regex `definedSymbols`)

**Files:**
- Modify: `internal/reduce/signals.go` (replace `definedSymbols`; keep `salientLiterals`/`literalsUsed`/`pathReferenced`/`symbolsUsed`)
- Test: `internal/reduce/signals_test.go`

**Interfaces:**
- Consumes: `treesitter.LangForExt`, `treesitter.Parse`.
- Produces: `definedSymbols(text, filePath string) map[string]struct{}` (unchanged signature) — now tree-sitter-based; `isCodePath` now delegates to `treesitter.LangForExt(fp) != ""`.

- [ ] **Step 1: Write the failing test (Go method case the regex missed)**

```go
package reduce

import "testing"

func TestDefinedSymbolsCatchesGoMethod(t *testing.T) {
	src := "package p\ntype T struct{}\nfunc (r *T) DoThing() {}\nfunc Helper() {}\n"
	syms := definedSymbols(src, "x.go")
	for _, want := range []string{"DoThing", "Helper"} {
		if _, ok := syms[want]; !ok {
			t.Fatalf("missing symbol %q in %v", want, syms)
		}
	}
}

func TestDefinedSymbolsIgnoresComments(t *testing.T) {
	src := "package p\n// func Ghost() {}\nfunc Real() {}\n"
	syms := definedSymbols(src, "x.go")
	if _, ok := syms["Ghost"]; ok {
		t.Fatal("commented-out func must not be a defined symbol")
	}
}

func TestDefinedSymbolsNonCodeEmpty(t *testing.T) {
	if len(definedSymbols("hello", "notes.txt")) != 0 {
		t.Fatal("non-code path must yield no symbols")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=1 go test ./internal/reduce/ -run TestDefinedSymbols -v`
Expected: FAIL (`DoThing` missed by the regex, or `Ghost` wrongly included).

- [ ] **Step 3: Implement via tree-sitter query**

Replace `defRe`, `isCodePath`, and `definedSymbols` in `signals.go` with:

```go
// (top of file) import "github.com/rossoctl/lab-context-engineering/internal/treesitter"
// and sitter "github.com/tree-sitter/go-tree-sitter"

func isCodePath(fp string) bool { return treesitter.LangForExt(fp) != "" }

// nameFieldKinds: definition node kinds whose "name" field (or identifier child)
// names a symbol, across grammars.
var defNodeKinds = map[string]bool{
	"function_declaration": true, "function_definition": true, "function_item": true,
	"method_declaration": true, "method_definition": true, "method": true,
	"class_declaration": true, "class_definition": true, "class": true,
	"struct_item": true, "enum_item": true, "trait_item": true,
	"type_spec": true, "interface_declaration": true, "module": true,
}

// definedSymbols returns names of functions/classes/methods/types defined in code
// text. Empty for non-code paths or on any parse failure (fail-open). Drops names
// shorter than 3 chars.
func definedSymbols(text, filePath string) map[string]struct{} {
	lang := treesitter.LangForExt(filePath)
	if lang == "" {
		return nil
	}
	src := []byte(text)
	tree, _, ok := treesitter.Parse(lang, src)
	if !ok {
		return nil
	}
	defer tree.Close()
	out := map[string]struct{}{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if defNodeKinds[n.Kind()] {
			if name := n.ChildByFieldName("name"); name != nil {
				if s := name.Utf8Text(src); len(s) >= 3 {
					out[s] = struct{}{}
				}
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())
	return out
}
```

Delete the now-unused `defRe` regex.

- [ ] **Step 4: Run tests + the full reduce suite**

Run: `CGO_ENABLED=1 go test ./internal/reduce/ -v`
Expected: PASS (new + existing relevance/pipeline tests).

- [ ] **Step 5: Commit**

```bash
git add internal/reduce/signals.go internal/reduce/signals_test.go
git commit -s -m "feat(reduce): tree-sitter symbol extraction (catches methods, ignores comments)"
```

---

### Task 4: Real code skeletonization (un-stub `skeletonize`)

**Files:**
- Modify: `internal/reduce/actions.go` (replace `skeletonize` stub; `skeletonReduce` already calls it and passes the lang)
- Test: `internal/reduce/skeleton_test.go`

**Interfaces:**
- Consumes: `treesitter`.
- Produces: `skeletonize(source, filePath string) (string, bool)` — **signature changes** from `(source, lang string)` to take the file path so it can resolve the grammar. Update `skeletonReduce` to call `skeletonize(item.Text, item.FilePath)`.

- [ ] **Step 1: Write the failing test**

```go
package reduce

import (
	"strings"
	"testing"
)

func TestSkeletonizeDropsGoBodies(t *testing.T) {
	src := "package p\nfunc Big() int {\n\t" + strings.Repeat("x := 1\n\t", 50) + "return x\n}\n"
	sk, ok := skeletonize(src, "big.go")
	if !ok {
		t.Fatal("expected skeletonization to apply")
	}
	if !strings.Contains(sk, "func Big()") {
		t.Fatal("signature must be kept")
	}
	if strings.Contains(sk, "x := 1\n\tx := 1") {
		t.Fatal("body should have been elided")
	}
}

func TestSkeletonizeNonCodeFails(t *testing.T) {
	if _, ok := skeletonize("hello", "n.txt"); ok {
		t.Fatal("non-code must return ok=false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=1 go test ./internal/reduce/ -run TestSkeletonize -v`
Expected: FAIL (stub returns false).

- [ ] **Step 3: Implement body elision by byte range**

```go
// (imports) "sort"; treesitter; sitter "github.com/tree-sitter/go-tree-sitter"; tokens already imported

var bodyDefKinds = map[string]bool{
	"function_declaration": true, "function_definition": true, "function_item": true,
	"method_declaration": true, "method_definition": true, "method": true,
	"constructor_declaration": true,
}

// skeletonize keeps signatures and drops function/method BODIES (the "body" field),
// language-agnostic via tree-sitter. Returns ok=false for non-code, parse failure,
// no bodies, or no token savings.
func skeletonize(source, filePath string) (string, bool) {
	lang := treesitter.LangForExt(filePath)
	if lang == "" {
		return "", false
	}
	src := []byte(source)
	tree, _, ok := treesitter.Parse(lang, src)
	if !ok {
		return "", false
	}
	defer tree.Close()
	type span struct{ start, end uint }
	var bodies []span
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if bodyDefKinds[n.Kind()] {
			if b := n.ChildByFieldName("body"); b != nil {
				bodies = append(bodies, span{b.StartByte(), b.EndByte()})
				return // don't recurse into a body we're dropping (nested fns)
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())
	if len(bodies) == 0 {
		return "", false
	}
	sort.Slice(bodies, func(i, j int) bool { return bodies[i].start > bodies[j].start })
	out := append([]byte(nil), src...)
	for _, b := range bodies {
		out = append(out[:b.start], append([]byte("{ ... }"), out[b.end:]...)...)
	}
	result := string(out)
	if tokens.Count(result) >= tokens.Count(source) {
		return "", false
	}
	return result, true
}
```

Update `skeletonReduce` to call `skeletonize(item.Text, item.FilePath)` (drop the second `lang` arg and its `skeletonize(item.Text, "")` call site).

- [ ] **Step 4: Run tests**

Run: `CGO_ENABLED=1 go test ./internal/reduce/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/reduce/actions.go internal/reduce/skeleton_test.go
git commit -s -m "feat(reduce): tree-sitter code skeletonization (keep signatures, drop bodies)"
```

---

### Task 5: Add TOON to the format re-encoder

**Files:**
- Modify: `internal/reduce/actions.go` (`bestEncoding` encoder table)
- Test: `internal/reduce/format_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces: a new encoder candidate `("toon", rank, encTOON)` in `bestEncoding`; selection still by `tokens.Count`, only returned when strictly smaller.

- [ ] **Step 1: Pin the dependency**

Run: `go get github.com/toon-format/toon-go@<latest-commit-sha>`
(No releases — pin an explicit commit; record the sha in the commit message.)

- [ ] **Step 2: Write the failing test**

```go
package reduce

import (
	"strings"
	"testing"
)

func TestBestEncodingConsidersTOON(t *testing.T) {
	// Uniform array with a nested field CSV can't represent; TOON should be a
	// candidate and the result must round-trip-contain the data.
	var recs []string
	for i := 0; i < 40; i++ {
		recs = append(recs, `{"id":`+itoaTest(i)+`,"meta":{"k":"v"},"name":"item"}`)
	}
	body := "[" + strings.Join(recs, ",") + "]"
	enc, name := bestEncoding(body)
	if enc == "" {
		t.Fatal("expected a smaller encoding")
	}
	if len(enc) >= len(body) {
		t.Fatal("encoding not smaller")
	}
	_ = name // may be toon or json_compact depending on token counts; just assert it ran
}
```

(Add `func itoaTest(n int) string` helper if not present in the test package.)

- [ ] **Step 3: Run test to verify it fails or is incomplete**

Run: `CGO_ENABLED=1 go test ./internal/reduce/ -run TestBestEncodingConsidersTOON -v`
Expected: passes only if TOON helps; first add the encoder, then confirm. (If it already passes via json_compact, still add TOON so it competes — verify with a uniform-flat case where TOON beats JSON.)

- [ ] **Step 4: Implement `encTOON` and register it**

```go
// import toon "github.com/toon-format/toon-go"

func encTOON(data any) (string, bool) {
	out, err := toon.MarshalString(data, toon.WithLengthMarkers(true))
	if err != nil || out == "" {
		return "", false
	}
	return out, true
}
```

In `bestEncoding`'s `encoders` slice, insert `{"toon", 1, encTOON}` and renumber ranks so the model-friendly order is `json_compact(0) < toon(1) < jsonl(2) < markdown_kv(3) < tsv(4) < csv(5)`. TOON output must still pass the existing "strictly smaller" gate; the rewind store holds the original so re-encoding is reversible.

- [ ] **Step 5: Run tests**

Run: `CGO_ENABLED=1 go test ./internal/reduce/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/reduce/actions.go internal/reduce/format_test.go
git commit -s -m "feat(reduce): add TOON to the format re-encoder candidates (pinned toon-go @<sha>)"
```

---

### Task 6: Starlark code-writing extractor strategy

**Files:**
- Create: `internal/extract/starlark.go`
- Modify: `internal/extract/extract.go` (add a "code" strategy that uses Starlark; update prompt contract; `strategyOrder` auto picks "code" for mid-size, "rlm" for very large)
- Create: `internal/extract/starlark_test.go`
- Modify: `internal/extract/prompt.go` (a code-writing prompt variant)
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: `extract.Model` (existing), `extract.IsContained` (existing).
- Produces:
  - `runStarlark(ctx, body, goal string, keepIDs []string, model Model) string` — asks the model for a Starlark program (contract: read `INPUT` string, assign `OUTPUT` string), runs it sandboxed over the FULL body, returns the candidate text ("" on any failure).
  - `runCode` becomes the new auto primary for mid-size bodies; `runSingle` (JSON-return) stays as a fallback strategy named "single".

- [ ] **Step 1: Add the dependency**

Run: `go get go.starlark.net/starlark@latest go.starlark.net/lib/json@latest`

- [ ] **Step 2: Write the failing test (sandbox runs a model-written filter over the FULL body)**

```go
package extract

import (
	"context"
	"strings"
	"testing"
)

// starlarkModel returns a fixed Starlark program that keeps records whose name
// contains "keep" — exercises real code execution over the full input.
type starlarkModel struct{}

func (starlarkModel) Complete(_ context.Context, _ string) (string, error) {
	return `
data = json.decode(INPUT)
kept = [r for r in data if "keep" in r["name"]]
OUTPUT = json.encode(kept)
`, nil
}

func TestRunStarlarkFiltersFullBody(t *testing.T) {
	var recs []string
	for i := 0; i < 100; i++ {
		name := "drop"
		if i%10 == 0 {
			name = "keep"
		}
		recs = append(recs, `{"id":`+itoa(i)+`,"name":"`+name+`"}`)
	}
	body := "[" + strings.Join(recs, ",") + "]"
	out := runStarlark(context.Background(), body, "find keep", nil, starlarkModel{})
	if out == "" {
		t.Fatal("expected a Starlark result")
	}
	if !IsContained(parseBody(out), parseBody(body)) {
		t.Fatalf("Starlark output must be a contained subset: %s", out)
	}
	if strings.Contains(out, "drop") {
		t.Fatal("filter should have dropped non-keep records")
	}
	if !strings.Contains(out, "keep") {
		t.Fatal("filter should have kept the keep records (recall, not truncation)")
	}
}

// malicious program must fail-open (no panic, returns "").
type evilModel struct{}

func (evilModel) Complete(_ context.Context, _ string) (string, error) {
	return `load("os", "x")`, nil // imports disabled
}

func TestRunStarlarkFailsOpenOnDisallowed(t *testing.T) {
	if out := runStarlark(context.Background(), `[{"a":1}]`, "", nil, evilModel{}); out != "" {
		t.Fatalf("disallowed program must fail open to \"\", got %q", out)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `CGO_ENABLED=1 go test ./internal/extract/ -run TestRunStarlark -v`
Expected: FAIL (`runStarlark` undefined).

- [ ] **Step 4: Implement the Starlark sandbox**

```go
// Package extract — starlark.go
package extract

import (
	"context"
	"time"

	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

const (
	starlarkMaxSteps = 50_000_000
	starlarkTimeout  = 2 * time.Second
)

// runStarlark asks the model for a Starlark program whose contract is: read the
// global string INPUT (the full tool output), assign a string global OUTPUT (the
// filtered value). It runs sandboxed over the FULL body — no imports, no I/O, step +
// time limits — and returns OUTPUT, or "" on any failure (fail-open). Containment is
// verified by the caller (RunExtraction).
func runStarlark(ctx context.Context, body, goal string, keepIDs []string, model Model) (out string) {
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	if model == nil {
		return ""
	}
	src, err := model.Complete(ctx, buildCodePrompt(body, goal, keepIDs))
	if err != nil {
		return ""
	}
	src = stripFences(src)

	ctx, cancel := context.WithTimeout(ctx, starlarkTimeout)
	defer cancel()
	thread := &starlark.Thread{Name: "extract"} // Load==nil => load() disabled
	thread.SetMaxExecutionSteps(starlarkMaxSteps)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			thread.Cancel(ctx.Err().Error())
		case <-done:
		}
	}()
	defer close(done)

	predeclared := starlark.StringDict{
		"json":  starjson.Module,
		"INPUT": starlark.String(body),
	}
	globals, err := starlark.ExecFile(thread, "extract.star", src, predeclared)
	if err != nil {
		return ""
	}
	res, ok := globals["OUTPUT"].(starlark.String)
	if !ok {
		return ""
	}
	return string(res)
}
```

Add `buildCodePrompt` to `prompt.go` (a sibling of `buildPrompt`): instruct the model to "write a Starlark program: `data = json.decode(INPUT)`; select a smaller value of the same shape; `OUTPUT = json.encode(result)`; select/never summarize; recall-first; no imports/IO." Show the schema + a small sample (it does NOT need the full body — the program runs over the real INPUT), so this prompt is token-cheap.

In `extract.go`: add strategy `"code"` calling `runStarlark`; in `strategyOrder` auto, primary becomes `"code"` for `tokenEst < max(floor*4,8000)` and `"rlm"` above it; keep `"single"` (JSON-return) and `"deterministic"` as ordered fallbacks. Add the case to `RunExtraction`'s switch.

- [ ] **Step 5: Run tests**

Run: `CGO_ENABLED=1 go test ./internal/extract/ -v`
Expected: PASS (new Starlark tests + existing containment/deterministic/rlm).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/extract/
git commit -s -m "feat(extract): Starlark code-writing extractor over full output (sandboxed, containment-verified)"
```

---

### Task 7: Fix extractor recall + cache correctness

**Files:**
- Modify: `internal/extract/extract.go` (`runSingle` no longer truncates to 4000 when used as fallback; document that "code"/"rlm" see the full body)
- Modify: `engine/extractfunc.go` (goal-aware cache key)
- Test: `engine/extract_engine_test.go` (add a cross-goal cache test)

**Interfaces:**
- Produces: cache key = `extract.ContentKey(text) + ":" + hash(goal+keep)` so the same body under a different goal is a cache miss.

- [ ] **Step 1: Write the failing test**

```go
// in engine package test
func TestExtractionCacheIsGoalAware(t *testing.T) {
	// Two requests, same large body, different goals -> the second must NOT reuse
	// the first's goal-specific filtered result.
	// (Construct two canon.Requests sharing the tool_result body but with different
	// trailing user text; run Transform on each with a model that echoes the goal;
	// assert the spliced results differ.)
}
```

(Write the concrete bodies as in the existing `TestExtractionEndToEnd`, differing only in the final user text; use a model whose output depends on the goal token so a shared cache would be observable.)

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=1 go test ./engine/ -run TestExtractionCacheIsGoalAware -v`
Expected: FAIL (current cache keys on body only → second goal reuses first result).

- [ ] **Step 3: Implement goal-aware key**

In `engine/extractfunc.go`, change the cache key from `extract.ContentKey(c.Text)` to a composite:

```go
import "crypto/sha256"; import "encoding/hex"

func goalKey(contentKey, goal string, keep []string) string {
	h := sha256.New()
	h.Write([]byte(contentKey)); h.Write([]byte{0}); h.Write([]byte(goal))
	for _, k := range keep { h.Write([]byte{0}); h.Write([]byte(k)) }
	return hex.EncodeToString(h.Sum(nil))[:24]
}
// key := goalKey(extract.ContentKey(c.Text), goal, keep)
```

- [ ] **Step 4: Run tests**

Run: `CGO_ENABLED=1 go test ./engine/ ./internal/extract/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add engine/extractfunc.go engine/extract_engine_test.go internal/extract/extract.go
git commit -s -m "fix(extract): goal-aware extraction cache key; full-body strategies avoid truncation"
```

---

### Task 8: OpenAI cheap-model client + robust content parsing

**Files:**
- Create: `internal/cheapmodel/openai.go`
- Modify: `internal/cheapmodel/anthropic.go` (scan content blocks for first text, not `Content[0]`)
- Test: `internal/cheapmodel/cheapmodel_test.go` (httptest servers for both)
- Modify: `cmd/proxy/main.go` (`--extract-provider anthropic|openai`)

**Interfaces:**
- Produces: `cheapmodel.OpenAI{BaseURL, APIKey, Model, MaxTokens, Client}` implementing `engine.Model` via `Complete`; both clients select the first text content block.

- [ ] **Step 1: Write the failing tests (httptest upstreams)**

```go
package cheapmodel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicSkipsNonTextLeadingBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"thinking","text":""},{"type":"text","text":"RESULT"}]}`)
	}))
	defer srv.Close()
	got, err := Anthropic{BaseURL: srv.URL, Model: "m"}.Complete(context.Background(), "p")
	if err != nil || got != "RESULT" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestOpenAIComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"OUT"}}]}`)
	}))
	defer srv.Close()
	got, err := OpenAI{BaseURL: srv.URL, Model: "m"}.Complete(context.Background(), "p")
	if err != nil || got != "OUT" {
		t.Fatalf("got %q err %v", got, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cheapmodel/ -v`
Expected: FAIL (OpenAI undefined; Anthropic returns "" for leading non-text block).

- [ ] **Step 3: Implement**

In `anthropic.go`, replace `return out.Content[0].Text` with a loop returning the first block whose `Text != ""`. Create `openai.go` with an `OpenAI` struct POSTing to `{base}/v1/chat/completions` (`{model, max_tokens, messages:[{role:"user",content:prompt}]}`), parsing `choices[0].message.content`. Add `--extract-provider` to `cmd/proxy/main.go` selecting which client to construct.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/cheapmodel/ -v && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cheapmodel/ cmd/proxy/main.go
git commit -s -m "feat(cheapmodel): OpenAI client + first-text-block selection; --extract-provider flag"
```

---

### Task 9: Non-dependency correctness/perf fixes

**Files:**
- Modify: `internal/reduce/signals.go` (`pathReferenced` precompile)
- Modify: `internal/extract/deterministic.go` (rune-safe slicing)
- Modify: `internal/proxyhttp/proxy.go` (upstream client timeout + request body cap)
- Test: extend `internal/extract/extract_test.go`, `internal/proxyhttp/proxy_test.go`

**Interfaces:** no signature changes.

- [ ] **Step 1: Write failing tests**

```go
// extract_test.go — rune-safe truncation
func TestTruncateValueDoesNotSplitRunes(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	out := truncateValue(s, 5).(string)
	if !utf8.ValidString(out) {
		t.Fatalf("truncation split a rune: %q", out)
	}
}
```

```go
// proxy_test.go — body size cap returns 413
func TestRequestBodyCap(t *testing.T) {
	h := New(Config{Engine: engine.New(config.Default(), nil, nil), Upstream: "http://unused", MaxBodyBytes: 10})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(strings.Repeat("x", 100)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want 413", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/extract/ ./internal/proxyhttp/ -run 'TestTruncateValue|TestRequestBodyCap' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

- `truncateValue`/`extractTextWindow`: convert to `[]rune` before slicing (`r := []rune(v); if len(r) > n { return string(r[:n]) }`).
- `pathReferenced`: precompute the two boundary regexes once per call from `file_path`+basename outside the per-item loop, or hoist to a small `sync.Map` cache keyed by file path. (Minimum: build the regexps once before the `for _, tok` loop instead of inside it; the existing call is already per-(item,candidate) — move compilation to the top of `pathReferenced`.)
- `proxyhttp.Config`: add `MaxBodyBytes int64` (default e.g. 32<<20) and `UpstreamTimeout time.Duration`; in `model`/`passthrough`, wrap `r.Body` with `http.MaxBytesReader` and return 413 on overflow; default `cfg.Client` to `&http.Client{Timeout: cfg.UpstreamTimeout}` when set.

- [ ] **Step 4: Run tests**

Run: `CGO_ENABLED=1 go test ./... -v 2>&1 | tail -20`
Expected: PASS across all packages.

- [ ] **Step 5: Commit**

```bash
git add internal/reduce/signals.go internal/extract/deterministic.go internal/proxyhttp/
git commit -s -m "fix: rune-safe truncation, precompiled pathReferenced regex, proxy body cap + timeout"
```

---

### Task 10: CI + Docker for CGO

**Files:**
- Modify: `.github/workflows/ci.yaml`
- Modify: `Dockerfile`
- Modify: `Makefile`

**Interfaces:** none.

- [ ] **Step 1: Update the Makefile**

Set `export CGO_ENABLED=1` near the top so `make test`/`make build` compile the tree-sitter binding. (Document that a C toolchain is required.)

- [ ] **Step 2: Update CI**

In `.github/workflows/ci.yaml`, ensure the `build-test` job runs with `CGO_ENABLED=1` (ubuntu runners include gcc). Add an `env: { CGO_ENABLED: "1" }` to the job.

- [ ] **Step 3: Update the Dockerfile**

Build stage already uses `golang:1.25` (has gcc). Change `CGO_ENABLED=0` → `CGO_ENABLED=1` in the build command, and the final stage from `gcr.io/distroless/static-debian12:nonroot` to `gcr.io/distroless/base-debian12:nonroot` (provides glibc for the CGO binary).

- [ ] **Step 4: Verify locally**

Run: `make lint && CGO_ENABLED=1 go test ./... 2>&1 | tail -20 && make build && ./bin/lab-cx version`
Expected: lint clean, all tests pass, binary runs.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yaml Dockerfile Makefile
git commit -s -m "build: enable CGO for tree-sitter (CI + distroless/base + Makefile)"
```

---

## Out of scope (deferred, not naive-critical)

- MinHash/LSH for dedup (`github.com/ekzhu/minhash-lsh`) — current O(n²) Jaccard is correct; revisit if a profiler flags it on large transcripts.
- cmdfilter as data-driven config / machine-readable (`go test -json`) parsing — current regex rules are correct for the enumerated tools; expand coverage later.
- Real RLM REPL loop (iterative `print`/`llm_query` fan-out) — Task 6's Starlark "code" strategy + chunked "rlm" cover the high-value cases; the full recursive REPL is a follow-up.
- Tier-2 real-Python subprocess sandbox — Starlark in-process covers JSON projection; add only if a hard memory ceiling or full Python semantics is needed.
- Gemini surface; tiktoken→true Claude token counts (needs Anthropic count_tokens API).
