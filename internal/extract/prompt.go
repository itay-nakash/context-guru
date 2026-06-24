package extract

import (
	"encoding/json"
	"strings"
)

// Prompt building. Because the model returns the filtered VALUE (which containment
// then verifies), it must SEE the values — so the prompt shows the actual JSON/text
// (truncated). For very large lists the RLM strategy chunks the body so each chunk is
// shown in full. The rule set is winnow's "select, never summarize, recall-first"
// contract, retargeted from "write a function" to "return the JSON".

// sampleMarker precedes the body in the prompt; tests and the (future) model both
// locate the payload after it.
const sampleMarker = "INPUT (return a smaller value of this same shape):\n"

const rules = `Return ONLY a JSON value (or, for raw text input, the kept text): a SMALLER value
of the SAME shape, selecting only what the agent needs next. Rules, in priority order:
1. RECALL FIRST. When unsure whether a record/field is relevant, KEEP IT.
2. SELECT, NEVER SUMMARIZE. Return whole records/objects/values byte-for-byte. Never
   paraphrase, truncate, round, reformat, or invent values.
3. PRESERVE EXACTLY: ids, numbers, names, paths, timestamps, error messages, stack
   traces — and anything matching the KEEP list.
4. Only drop records that are CLEARLY irrelevant boilerplate, duplicates, or noise.
5. If you cannot identify clearly-irrelevant content, RETURN THE INPUT UNCHANGED.
6. Keep the natural shape and types. Output ONLY the value — no prose, no markdown.`

const example = `EXAMPLE
Goal: "Fix failing test test_auth_expiry; find the relevant hit."
KEEP: ["test_auth_expiry","auth/session.py"]
INPUT: [{"path":"auth/session.py","snippet":"def test_auth_expiry()..."},{"path":"README.md","snippet":"intro"}]
OUTPUT: [{"path":"auth/session.py","snippet":"def test_auth_expiry()..."}]`

func buildPrompt(bodyText, goal string, keepIDs []string) string {
	g := strings.TrimSpace(goal)
	if g == "" {
		g = "(no explicit goal stated)"
	}
	if len(g) > 2000 {
		g = g[:2000]
	}
	keep := keepIDs
	if len(keep) > 60 {
		keep = keep[:60]
	}
	keepBlock := ""
	if len(keep) > 0 {
		kb, _ := json.Marshal(keep)
		keepBlock = "IDENTIFIERS THE AGENT REFERENCED RECENTLY — keep every record or field\n" +
			"whose value matches any of these, verbatim:\n" + string(kb) + "\n\n"
	}

	// Show the actual value (pretty-printed JSON if it parses), truncated.
	sample := bodyText
	if v := parseBody(bodyText); !isRawString(v) {
		if b, err := json.MarshalIndent(v, "", "  "); err == nil {
			sample = string(b)
		}
	}
	sample = truncate(sample, sampleChars)

	return "You filter ONE tool output down to only what the agent needs next.\n\n" +
		"WHAT THE AGENT IS DOING NOW (filter toward this):\n" + g + "\n\n" +
		keepBlock + sampleMarker + sample + "\n\n" + rules + "\n\n" + example
}

// codeRules is the Starlark code-writing contract: the model writes a program that
// runs over the real INPUT (it is NOT shown the full body), so the prompt stays cheap.
// Same "select, never summarize, recall-first" discipline as buildPrompt, retargeted
// from "return the value" to "write the filter".
const codeRules = `Write a Starlark program (a safe Python subset) that filters this ONE tool output
down to only what the agent needs next. Contract:
- The global string INPUT holds the FULL tool output. The module ` + "`json`" + ` is available.
- Start: data = json.decode(INPUT)
- Select a SMALLER value of the SAME shape (e.g. drop irrelevant list records / fields).
- End: OUTPUT = json.encode(result)   # OUTPUT must be a string
Rules, in priority order:
1. RECALL FIRST. When unsure whether a record/field is relevant, KEEP IT.
2. SELECT, NEVER SUMMARIZE. Keep whole records/values byte-for-byte. Never paraphrase,
   truncate, round, reformat, or invent values — only DROP clearly-irrelevant ones.
3. PRESERVE EXACTLY: ids, numbers, names, paths, timestamps, errors, stack traces —
   and anything matching the KEEP list.
4. NO imports (no load()), NO I/O, NO network. Use only json.decode/json.encode and
   plain Starlark (list comprehensions, dict/list ops, string ops).
5. If you cannot identify clearly-irrelevant content, set OUTPUT = INPUT.
Output ONLY the Starlark program — no prose, no markdown fences.`

const codeExample = `EXAMPLE
Goal: "Fix failing test test_auth_expiry; find the relevant hit."
KEEP: ["test_auth_expiry","auth/session.py"]
INPUT schema: list of {"path": str, "snippet": str}
PROGRAM:
data = json.decode(INPUT)
result = [r for r in data if "auth" in r["path"] or "test_auth_expiry" in r["snippet"]]
OUTPUT = json.encode(result)`

// buildCodePrompt builds the prompt for the Starlark code-writing strategy. Unlike
// buildPrompt it does NOT inline the full body — the program runs over the real INPUT.
// It shows the parsed shape and a small sample so the model knows the schema cheaply.
func buildCodePrompt(bodyText, goal string, keepIDs []string) string {
	g := strings.TrimSpace(goal)
	if g == "" {
		g = "(no explicit goal stated)"
	}
	if len(g) > 2000 {
		g = g[:2000]
	}
	keep := keepIDs
	if len(keep) > 60 {
		keep = keep[:60]
	}
	keepBlock := ""
	if len(keep) > 0 {
		kb, _ := json.Marshal(keep)
		keepBlock = "IDENTIFIERS THE AGENT REFERENCED RECENTLY — keep every record or field\n" +
			"whose value matches any of these, verbatim:\n" + string(kb) + "\n\n"
	}

	// A small sample of the parsed shape — enough to infer the schema, not the full body.
	sample := bodyText
	if v := parseBody(bodyText); !isRawString(v) {
		if b, err := json.MarshalIndent(v, "", "  "); err == nil {
			sample = string(b)
		}
	}
	sample = truncate(sample, codeSampleChars)

	return "You write a Starlark filter for ONE tool output, keeping only what the agent needs next.\n\n" +
		"WHAT THE AGENT IS DOING NOW (filter toward this):\n" + g + "\n\n" +
		keepBlock + "INPUT SAMPLE (the real INPUT at runtime is the FULL output of this same shape):\n" +
		sample + "\n\n" + codeRules + "\n\n" + codeExample
}

const codeSampleChars = 1500

func isRawString(v any) bool {
	_, ok := v.(string)
	return ok
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
