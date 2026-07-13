package extract

import (
	"encoding/json"
	"strings"
)

// Prompt building. Because the model returns the filtered VALUE (which containment
// then verifies), it must SEE the values — so the prompt shows the actual JSON/text
// (truncated). For very large lists the RLM strategy chunks the body so each chunk is
// shown in full. The rule set is the reference prototype's "select, never summarize, recall-first"
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
	if len(g) > 8000 {
		g = g[:8000]
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

// codeRules is the Starlark code-writing contract for the DEFAULT deletion-only
// mode: the model sees the FULL output (below) and writes a program specific to
// THIS content that DELETES the irrelevant parts. The result is verified to be a
// character subsequence of the input (no fabrication/reorder/rewrite), so the
// model can trim freely and still never corrupt what the agent relies on.
const codeRules = `Write a Starlark program (a safe Python subset) that trims THIS ONE tool output
(shown in full below) down to only what the agent needs next. Be SPECIFIC to the
content you see — target the exact noise in it, not a generic filter.
Contract:
- The global string INPUT holds the FULL tool output (identical to what's shown).
- Assign a string global OUTPUT with the kept content.
- Available: the ` + "`json`" + ` module, and regex helpers
  re_sub(pattern, repl, s) -> s, re_findall(pattern, s) -> [str],
  re_split(pattern, s) -> [str], re_match(pattern, s) -> bool.
- JSON input (starts with { or [): result = json.decode(INPUT); drop irrelevant
  records/fields; OUTPUT = json.encode(result).
- Text input: keep relevant lines AND trim within them — drop irrelevant words,
  sentences, columns, repeated whitespace, banners, progress bars. Use string ops
  and re_sub for surgical removal. OUTPUT = the trimmed text.
Hard rule — DELETION ONLY:
1. You may only DELETE characters. NEVER add, reorder, reword, renumber, translate,
   or rephrase. The output MUST be obtainable by removing characters from INPUT
   (it is verified as a subsequence — a rewrite is rejected and wastes the call).
2. PRESERVE EXACTLY, verbatim: ids, numbers, names, paths, signatures, timestamps,
   error messages, stack traces, and anything matching the KEEP list.
3. RECALL FIRST: when unsure whether something is relevant, keep it.
4. NO imports (no load()), NO I/O, NO network.
5. If nothing is clearly irrelevant, set OUTPUT = INPUT.
Output ONLY the Starlark program — no prose, no markdown fences.`

// codeRewriteRules is the opt-in (rewrite:true) contract: containment is NOT
// enforced, so the model may reword/summarize/rewrite freely. Lossy + unverified —
// used only when a caller explicitly accepts that (e.g. AuthBridge summary mode).
const codeRewriteRules = `Write a Starlark program (a safe Python subset) that rewrites THIS ONE tool output
(shown in full below) down to only what the agent needs next.
Contract:
- The global string INPUT holds the FULL tool output.
- Assign a string global OUTPUT with the condensed content.
- Available: the ` + "`json`" + ` module and regex helpers re_sub/re_findall/re_split/re_match.
- You MAY delete, reword, summarize, collapse, or rewrite freely — but PRESERVE
  exactly every id, number, path, error message, and KEEP-list identifier verbatim.
- Keep it strictly smaller than INPUT. NO imports, NO I/O, NO network.
Output ONLY the Starlark program — no prose, no markdown fences.`

const codeExample = `EXAMPLE A (JSON) — drop irrelevant records:
  data = json.decode(INPUT)
  OUTPUT = json.encode([r for r in data if "col_insert" in r["match"] or "common.py" in r["path"]])
EXAMPLE B (pytest log) — delete the passing/progress noise, keep the failure:
  lines = INPUT.split("\n")
  kept = [ln for ln in lines if "PASSED" not in ln and not re_match("^\\s*$", ln)]
  OUTPUT = "\n".join(kept)
EXAMPLE C (within-line trim) — strip trailing progress columns with regex:
  OUTPUT = re_sub(" +\\[ *[0-9]+%\\]", "", INPUT)`

// maxCodeContentChars bounds the full output shown to the model. Big enough to be
// content-specific (~8k tokens), bounded so a giant output can't blow up the prompt;
// beyond it we show head+tail and note the truncation (the program still runs over
// the full INPUT at runtime).
const maxCodeContentChars = 32000

// buildCodePrompt builds the prompt for the Starlark code-writing strategy. It shows
// the model the FULL output (bounded) so it can write content-specific deletions
// rather than a blind generic filter. rewrite selects the (lossy, unverified) rewrite
// contract instead of the default deletion-only one.
func buildCodePrompt(bodyText, goal string, keepIDs []string, rewrite bool) string {
	g := strings.TrimSpace(goal)
	if g == "" {
		g = "(no explicit goal stated)"
	}
	if len(g) > 8000 {
		g = g[:8000]
	}
	keep := keepIDs
	if len(keep) > 60 {
		keep = keep[:60]
	}
	keepBlock := ""
	if len(keep) > 0 {
		kb, _ := json.Marshal(keep)
		keepBlock = "IDENTIFIERS THE AGENT REFERENCED RECENTLY — keep every one verbatim:\n" +
			string(kb) + "\n\n"
	}

	// Show the FULL content (pretty-printed if JSON) so the program can be specific.
	shown := bodyText
	if v := parseBody(bodyText); !isRawString(v) {
		if b, err := json.MarshalIndent(v, "", "  "); err == nil {
			shown = string(b)
		}
	}
	label := "FULL TOOL OUTPUT (INPUT is exactly this):"
	if len(shown) > maxCodeContentChars {
		half := maxCodeContentChars / 2
		shown = shown[:half] + "\n…[middle elided in this prompt; the real INPUT at runtime is the FULL output]…\n" + shown[len(shown)-half:]
		label = "TOOL OUTPUT (head+tail; the real INPUT at runtime is the FULL output):"
	}
	rules := codeRules
	if rewrite {
		rules = codeRewriteRules
	}
	return "You write a Starlark program that reduces ONE tool output to what the agent needs next.\n\n" +
		"WHAT THE AGENT IS DOING NOW (reduce toward this):\n" + g + "\n\n" +
		keepBlock + label + "\n" + shown + "\n\n" + rules + "\n\n" + codeExample
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
