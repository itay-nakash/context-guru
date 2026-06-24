// Command labcx-bench is an OFFLINE measurement harness: it proves, on REAL tool-output
// fixtures, how much each DETERMINISTIC reduction component reduces tokens/bytes — with
// no model in the loop (the Extract stage is OFF). For every fixture it builds a minimal
// canonical request that surfaces the fixture as a tool_result, runs the engine with one
// component enabled at a time (and the full deterministic pipeline), and reports
// tokens/bytes before+after, % saved, whether the reduction is reversible (the original
// recovers byte-for-byte via the rewind store), and the added latency.
//
// The token counts come from the same real BPE tokenizer the engine uses (o200k_base,
// internal/tokens). Run it and paste the table into docs/RESULTS-offline.md:
//
//	CGO_ENABLED=1 go run ./cmd/labcx-bench            # human table
//	CGO_ENABLED=1 go run ./cmd/labcx-bench --json     # machine-readable rows
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/config"
	"github.com/kagenti/lab-context-engineering/engine"
	"github.com/kagenti/lab-context-engineering/internal/tokens"
)

// fixture is one real tool output plus how to surface it as a canonical request.
type fixture struct {
	name     string // table label
	relPath  string // path under testdata/fixtures
	toolName string // tool that produced it (read → skeleton path; "" → generic)
	filePath string // input.file_path for read tools (drives FilePath/skeleton)
	command  string // input.command for Bash-style tools (drives cmdfilter)
}

// fixtureSet is the committed corpus. Each entry names the component it primarily
// exercises (see testdata/fixtures/README.md for provenance of every file).
var fixtureSet = []fixture{
	// cmd_outputs → command-output filter
	{"pytest_failures", "cmd_outputs/pytest_failures.txt", "Bash", "", "pytest -q tests/"},
	{"cargo_test_failure", "cmd_outputs/cargo_test_failure.txt", "Bash", "", "cargo test"},
	{"cargo_build", "cmd_outputs/cargo_build.txt", "Bash", "", "cargo build"},
	// structured_json → format re-encoder (toon / tsv / csv / json_compact)
	{"flights_search", "structured_json/flights_search.json", "search_flights", "", ""},
	{"users_directory", "structured_json/users_directory.json", "list_users", "", ""},
	{"products_inventory", "structured_json/products_inventory.json", "list_products", "", ""},
	{"oc_pods_slice", "structured_json/oc_pods_slice.json", "Bash", "", "oc get pods -o json"},
	// search_results → format re-encoder on nested record arrays
	{"glab_issue_list", "search_results/glab_issue_list.json", "Bash", "", "glab issue list -F json"},
	{"glab_mr_list", "search_results/glab_mr_list.json", "Bash", "", "glab mr list -F json"},
	// file_reads → code skeletonizer
	{"runner_py", "file_reads/runner.py", "Read", "scripts/benchmark-sessions/lib/runner.py", ""},
}

// component is one deterministic configuration to measure in isolation, plus the full
// deterministic pipeline. extract is always OFF (no model).
type component struct {
	name     string
	settings func() config.Settings
}

// isolate builds a settings value that turns the reduce stage on but enables only the
// named reducers / encoders, with caching off and extraction off — so a measured
// reduction is attributable to that one component.
func isolate(reducers, encoders []string, cmdFilter bool) config.Settings {
	return config.Settings{
		CacheEnabled:  false,
		ReduceEnabled: true,
		ProtectRecent: 2,
		// ProvableOnly off so an unused tool_result collapses to reason "unused" (a
		// collapse-eligible reason); reductions stay reversible regardless.
		ProvableOnly:    false,
		CollapseOutputs: true,
		CmdFilter:       cmdFilter,
		Reducers:        reducers,
		Encoders:        encoders,
		Stages:          []string{"reduce"},
		ExtractEnabled:  false,
	}
}

func components() []component {
	return []component{
		{"cmdfilter", func() config.Settings {
			// Only the command-output filter: no reducers in the routing loop.
			s := isolate([]string{"__none__"}, nil, true)
			return s
		}},
		{"format_toon", func() config.Settings {
			// Only the format re-encoder, restricted to TOON.
			return isolate([]string{"format"}, []string{"toon"}, false)
		}},
		{"format_best", func() config.Settings {
			// Only the format re-encoder, all encoders (it picks the smallest faithful).
			return isolate([]string{"format"}, nil, false)
		}},
		{"skeleton", func() config.Settings {
			// Only the code skeletonizer.
			return isolate([]string{"skeleton"}, nil, false)
		}},
		{"collapse", func() config.Settings {
			// Only the collapse reducer (reversible marker for unused outputs).
			return isolate([]string{"collapse"}, nil, false)
		}},
		{"pipeline_full", func() config.Settings {
			// Full deterministic pipeline: cmdfilter + all reducers/encoders + cache,
			// extraction OFF. This is the "balanced" preset minus the model.
			s := config.Default()
			s.ExtractEnabled = false
			s.ProvableOnly = false
			return s
		}},
	}
}

// row is one measured (fixture, component) result.
type row struct {
	Fixture      string  `json:"fixture"`
	Component    string  `json:"component"`
	TokensBefore int     `json:"tokens_before"`
	TokensAfter  int     `json:"tokens_after"`
	PctSaved     float64 `json:"pct_saved"`
	BytesBefore  int     `json:"bytes_before"`
	BytesAfter   int     `json:"bytes_after"`
	Reversible   bool    `json:"reversible"`
	AddedLatency float64 `json:"added_latency_ms"`
	changed      bool    // internal: did this component touch the fixture at all
}

func main() {
	jsonOut := flag.Bool("json", false, "emit machine-readable JSON rows instead of a table")
	flag.Parse()

	rows := measureAll()

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			fmt.Fprintln(os.Stderr, "labcx-bench:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Print(markdownTable(rows))
}

// fixturesDir locates testdata/fixtures by walking up from the working directory for the
// go.mod that marks the module root, so the harness runs from any subdirectory (and from
// `go test`, whose cwd is the package dir).
func fixturesDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata", "fixtures"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("module root (go.mod) not found above %s", dir)
		}
		dir = parent
	}
}

// measureAll runs every component against every fixture and returns the rows in a stable
// order (fixture order, then component order).
func measureAll() []row {
	base, err := fixturesDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "labcx-bench:", err)
		os.Exit(1)
	}
	comps := components()
	var rows []row
	for _, fx := range fixtureSet {
		body, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(fx.relPath)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "labcx-bench: read %s: %v\n", fx.relPath, err)
			os.Exit(1)
		}
		fixtureText := string(body)
		for _, c := range comps {
			rows = append(rows, measure(fx, fixtureText, c))
		}
	}
	return rows
}

// measure runs one component against one fixture in isolation and verifies reversibility.
func measure(fx fixture, fixtureText string, c component) row {
	settings := c.settings()
	eng := engine.New(settings, nil, nil)

	req := buildRequest(fx, fixtureText)
	before := req.Clone()

	start := time.Now()
	out, _ := eng.Transform(context.Background(), req)
	elapsed := time.Since(start)

	beforeText := toolResultText(before, fx)
	afterText := toolResultText(out, fx)

	r := row{
		Fixture:      fx.name,
		Component:    c.name,
		TokensBefore: tokens.Count(beforeText),
		TokensAfter:  tokens.Count(afterText),
		BytesBefore:  len(beforeText),
		BytesAfter:   len(afterText),
		AddedLatency: float64(elapsed.Microseconds()) / 1000.0,
		changed:      afterText != beforeText,
	}
	if r.TokensBefore > 0 {
		r.PctSaved = round2(float64(r.TokensBefore-r.TokensAfter) / float64(r.TokensBefore) * 100)
	}
	r.Reversible = verifyReversible(eng, beforeText, afterText)
	return r
}

// verifyReversible confirms the reduction is recoverable: if the component left a winnow
// marker, the rewind store must return the exact original; if it left the text untouched
// (a no-op for this fixture/component), that is trivially reversible. A reduction that
// changed the text but exposes no recoverable original would be a (disallowed) lossy drop.
func verifyReversible(eng *engine.Engine, before, after string) bool {
	if after == before {
		return true // no-op: nothing to recover
	}
	ids := engine.FindMarkers(after)
	if len(ids) == 0 {
		// The format re-encoder rewrites in place behind a recovery note that DOES carry
		// a marker; if no marker is present and the text changed, treat as not provably
		// reversible.
		return false
	}
	for _, id := range ids {
		orig, ok := eng.Expand(id)
		if !ok || !strings.Contains(before, orig) {
			return false
		}
	}
	return true
}

// buildRequest wraps a fixture as a canonical Anthropic-shaped request: an assistant
// tool_use (so command-aware and read-aware passes see the command / file path) followed
// by the tool_result carrying the fixture, then two neutral trailing turns so the
// fixture sits OUTSIDE the protect-recent window and is eligible for reduction.
func buildRequest(fx fixture, fixtureText string) canon.Request {
	input := map[string]any{}
	if fx.command != "" {
		input["command"] = fx.command
	}
	if fx.filePath != "" {
		input["file_path"] = fx.filePath
	}
	root := map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "Run the tool and report back."},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tu_1", "name": fx.toolName, "input": input},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": fixtureText},
			}},
			map[string]any{"role": "assistant", "content": "Acknowledged; continuing."},
			map[string]any{"role": "user", "content": "Please proceed with the next step."},
		},
	}
	return canon.Request{Root: root}
}

// toolResultText pulls the (possibly reduced) text of the fixture's tool_result block
// back out of a request, so before/after are compared on the SAME block.
func toolResultText(req canon.Request, fx fixture) string {
	for _, m := range req.Messages() {
		for _, b := range canon.Blocks(m) {
			if canon.BlockType(b) != "tool_result" {
				continue
			}
			switch c := b["content"].(type) {
			case string:
				return c
			case []any:
				var parts []string
				for _, x := range c {
					if bb, ok := x.(map[string]any); ok && bb["type"] == "text" {
						if t, ok := bb["text"].(string); ok {
							parts = append(parts, t)
						}
					}
				}
				return strings.Join(parts, "\n")
			}
		}
	}
	return ""
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5*sign(f))) / 100
}

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

// markdownTable renders the measured rows as the table pasted into RESULTS-offline.md.
func markdownTable(rows []row) string {
	var b strings.Builder
	b.WriteString("| fixture | component | tokens_before | tokens_after | %saved | bytes_before | bytes_after | reversible | added_latency_ms |\n")
	b.WriteString("| --- | --- | ---: | ---: | ---: | ---: | ---: | :---: | ---: |\n")
	// Stable order: fixtures as declared, components as declared.
	for _, r := range rows {
		rev := "yes"
		if !r.Reversible {
			rev = "no"
		}
		note := ""
		if !r.changed {
			note = " (no-op)"
		}
		fmt.Fprintf(&b, "| %s | %s%s | %d | %d | %.2f | %d | %d | %s | %.3f |\n",
			r.Fixture, r.Component, note, r.TokensBefore, r.TokensAfter, r.PctSaved,
			r.BytesBefore, r.BytesAfter, rev, r.AddedLatency)
	}
	return b.String()
}
