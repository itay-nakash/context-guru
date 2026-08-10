package extract

import (
	"crypto/sha256"
	"encoding/hex"
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

	// Show the actual value (pretty-printed JSON only if the whole body is a JSON
	// container — never mangle a numeric-prefixed text/file read), truncated.
	sample := bodyText
	if isJSONContainer(bodyText) {
		if v := parseBody(bodyText); !isRawString(v) {
			if b, err := json.MarshalIndent(v, "", "  "); err == nil {
				sample = string(b)
			}
		}
	}
	sample = truncate(sample, sampleChars)

	return "You filter ONE tool output down to only what the agent needs next.\n\n" +
		"WHAT THE AGENT IS DOING NOW (filter toward this):\n" + g + "\n\n" +
		keepBlock + sampleMarker + sample + "\n\n" + rules + "\n\n" + example
}

// codeContract is the shared preamble: the sandbox API and the always-safe
// defaults. Because the program runs inside a function body (top-level assignments
// are locals), OUTPUT may be reassigned as many times as you like — there is no
// "assign once" restriction.
const codeContract = `Write a Starlark program (a safe Python subset) that reduces THIS ONE tool output
(shown in full below) to only what the agent needs next. Be SPECIFIC to the content
you see — target its exact noise, not a generic filter. This is GENERAL: the output
may come from any tool for any agent (coding, tool/API use, research, ops, a task for
a user), and you do not know exactly what the agent does next, so RECALL FIRST — when
unsure whether something matters, keep it. Deleting a line the agent needed makes it
re-run the tool and redo work; keeping a borderline line costs only a few tokens.
Sandbox:
- INPUT (string) holds the FULL tool output, identical to what's shown below.
- OUTPUT (string) starts equal to INPUT; assign it the reduced content. You MAY
  reassign OUTPUT freely (build it up step by step).
- SUMMARY (string) starts empty; OPTIONALLY set it to ONE short line naming the gist
  of what you elided (e.g. "pytest: 3 failed, 710 passed" or "npm install: ok, 42
  pkgs"). It is shown to the agent inline next to the recovery marker. Leave it "" if
  a plain reduction needs no digest.
- Available: the ` + "`json`" + ` module and regex helpers
  re_sub(pattern, repl, s) -> s, re_findall(pattern, s) -> [str],
  re_split(pattern, s) -> [str], re_match(pattern, s) -> bool. RE2 syntax.
- NO imports (no load()), NO I/O, NO network.
Rule for SOURCE-CODE FILES (a file read: imports + function/class defs). A large
file read is the MOST important thing to reduce — produce a SKELETON. Do NOT return it
unchanged; a large file always has bodies to elide, and bodies are recoverable via
expand, so PREFER eliding. Concretely, go line by line and KEEP a line verbatim iff it
is one of:
  * an import / from / require / #include / package / using line;
  * a signature or structural line — contains "def ", "class ", "func ", "function ",
    "interface ", "type ", "struct ", "enum ", "public/private/protected", a decorator
    ("@..."), or ends with "{" / ":" at a low indent;
  * a module-level constant / assignment (low indent);
  * a docstring/comment line immediately under a kept signature;
  * ANY line containing a KEEP-list identifier or clearly central to the goal.
Otherwise the line is body detail: DROP it, and collapse each run of dropped lines
into ONE marker line that keeps the run's indentation, e.g.
  "        # … 14 lines elided (call context_guru_expand) …".
KEEP the FULL body (do NOT elide) when ANY of these hold — this protects the agent from
having to re-read (which wastes far more than it saves):
  * the definition's name or a line in it matches the KEEP list or the goal;
  * the body is SHORT (a rough rule: ≤ ~15 lines) — eliding it saves little but risks a
    re-read;
  * the definition is adjacent to a KEEP-matching one (the agent is likely working nearby).
Only elide the LONG bodies (> ~15 lines) of definitions with no KEEP/goal relevance.
When in doubt about a body, KEEP it. Kept lines must be BYTE-IDENTICAL (keep any leading
line numbers). A big file with many unrelated long defs should still shrink a lot; a
small or highly-relevant file may barely shrink — that is correct.
Always PRESERVE EXACTLY, verbatim: ids, numbers, names, paths, signatures,
timestamps, error messages, stack traces, and anything matching the KEEP list.
For NON-code output, if nothing is clearly reducible, leave OUTPUT = INPUT.`

// codeRules is the DEFAULT (powerful) contract: the program may delete OR rewrite
// via regex/string ops — collapse repeated blocks, strip progress columns/banners,
// keep only relevant records/lines — as long as the verbatim-preservation rule holds.
const codeRules = codeContract + `
You MAY delete AND rewrite: collapse runs, strip noise columns, drop irrelevant
records/lines, replace verbose boilerplate with a shorter form — provided every
preserved item above stays byte-for-byte intact.
Output ONLY the Starlark program — no prose, no markdown fences.`

// codeDeletionRules is the strict (rewrite:false) contract: deletion only, verified
// as a character subsequence of INPUT (no reorder/reword/fabrication).
const codeDeletionRules = codeContract + `
DELETION ONLY: you may only DELETE characters — never add, reorder, reword, renumber,
translate, or rephrase. The result MUST be obtainable by removing characters from
INPUT (verified as a subsequence; a rewrite is rejected and wastes the call). SUMMARY
is the ONE exception — it is separate from OUTPUT and may be free text.
Output ONLY the Starlark program — no prose, no markdown fences.`

const codeExample = `EXAMPLE A (JSON search hits) — keep only the relevant records:
  data = json.decode(INPUT)
  kept = [r for r in data if "col_insert" in r["match"] or "common.py" in r["path"]]
  OUTPUT = json.encode(kept)
  SUMMARY = "search: %d of %d hits kept" % (len(kept), len(data))
EXAMPLE B (pytest log) — drop passing/progress noise, strip the % progress column,
keep failures and the summary line:
  lines = INPUT.split("\n")
  kept = [ln for ln in lines if "PASSED" not in ln and not re_match("^\\s*$", ln)]
  OUTPUT = re_sub(" +\\[ *[0-9]+%\\]", "", "\n".join(kept))
  SUMMARY = "pytest failures + summary kept; passing lines elided"
EXAMPLE C (verbose install log) — collapse the "already satisfied" noise:
  lines = [ln for ln in INPUT.split("\n") if "already satisfied" not in ln]
  OUTPUT = "\n".join(lines)
EXAMPLE D (source-code FILE READ; goal/KEEP mentions "parse_config") — skeleton:
keep imports, every signature, and the relevant def; collapse each run of other body
lines into one indented marker. Kept lines stay byte-identical (line numbers kept).
  keep_ids = ["parse_config"]   # from KEEP / the goal
  out = []
  pending = 0    # consecutive elided body lines
  indent = ""
  for ln in INPUT.split("\n"):
    s = ln.strip()
    struct = ("def " in s or "class " in s or "func " in s or "function " in s or
              s.startswith("import ") or s.startswith("from ") or s.startswith("@") or
              s.endswith(":") or s.endswith("{"))
    keep = s == "" or struct or any([k in ln for k in keep_ids])
    if keep:
      if pending > 0:
        out.append(indent + "# ... " + str(pending) + " lines elided (call context_guru_expand) ...")
        pending = 0
      out.append(ln)
    else:
      if pending == 0:
        indent = ln[:len(ln) - len(s)]
      pending = pending + 1
  if pending > 0:
    out.append(indent + "# ... " + str(pending) + " lines elided (call context_guru_expand) ...")
  OUTPUT = "\n".join(out)
  SUMMARY = "skeleton: imports + signatures + parse_config kept; bodies elided"`

// maxCodeContentChars bounds the full output shown to the model. Big enough to be
// content-specific (~8k tokens), bounded so a giant output can't blow up the prompt;
// beyond it we show head+tail and note the truncation (the program still runs over
// the full INPUT at runtime).
const maxCodeContentChars = 32000

// PromptVersion identifies the extractor's prompt + acceptance semantics. The result cache
// key includes it, so a change MISSES every stale entry rather than serving an extraction
// derived under different rules.
//
// DERIVED, not hand-maintained. A manual constant only works if every future editor of the
// prompt remembers to bump it, and the one time someone forgets, the cache serves
// extractions produced under rules that no longer exist — exactly the failure the issue
// warned about, and one with no symptom to notice. Hashing the prompt text makes the version
// a consequence of the prompt instead of a promise about it.
var PromptVersion = promptFingerprint()

// semanticsVersion covers result-affecting changes OUTSIDE the prompt strings — the
// validation gate (validateExtraction / extractionIsSane) and the sandbox contract. Bump it
// when what gets ACCEPTED changes while the prompt text does not.
const semanticsVersion = "s1"

// promptFingerprint hashes every prompt constant that can change what the model returns.
// Add new prompt text here as it is introduced: anything omitted is invisible to the key.
func promptFingerprint() string {
	h := sha256.New()
	for _, part := range []string{
		semanticsVersion,
		codeContract, codeRules, codeDeletionRules, codeExample,
		rules, example, sampleMarker,
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return "p" + hex.EncodeToString(h.Sum(nil))[:12]
}

// codeSystemPreamble is the INVARIANT half of the code-strategy prompt: the sandbox
// contract, the rules, and the worked examples. It is byte-identical on every call, so
// it is the cacheable prefix — sent as a `system` block with a cache_control breakpoint
// (see cheapmodel.Anthropic.CompleteSystem). ~1463 tokens as measured.
//
// It is split by REWRITE MODE, not per call: two possible values total, so each is a
// stable prefix that a provider can actually cache across calls. Anything that varies
// per call (the goal, the keep-list, the tool output) stays in the user message.
func codeSystemPreamble(rewrite bool) string {
	rules := codeRules
	if !rewrite {
		rules = codeDeletionRules
	}
	return "You write a Starlark program that reduces ONE tool output to what the agent needs next.\n\n" +
		rules + "\n\n" + codeExample
}

// buildCodePrompt builds the prompt for the Starlark code-writing strategy. It shows
// the model the FULL output (bounded) so it can write content-specific deletions
// rather than a blind generic filter. rewrite selects the (lossy, unverified) rewrite
// contract instead of the default deletion-only one.
//
// Deprecated in favor of buildCodePromptSplit; retained so the single-message shape
// stays testable and any caller without a system-capable client keeps working.
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

	// Show the FULL content. Pretty-print ONLY when the WHOLE body is a valid JSON
	// object/array — never for text that merely starts with a number (a line-numbered
	// file read like "55\t…" would otherwise be parsed as the JSON number 55 and shown
	// to the model as just "55", destroying the input).
	shown := bodyText
	if isJSONContainer(bodyText) {
		if v := parseBody(bodyText); !isRawString(v) {
			if b, err := json.MarshalIndent(v, "", "  "); err == nil {
				shown = string(b)
			}
		}
	}
	label := "FULL TOOL OUTPUT (INPUT is exactly this):"
	if len(shown) > maxCodeContentChars {
		half := maxCodeContentChars / 2
		shown = shown[:half] + "\n…[middle elided in this prompt; the real INPUT at runtime is the FULL output]…\n" + shown[len(shown)-half:]
		label = "TOOL OUTPUT (head+tail; the real INPUT at runtime is the FULL output):"
	}
	rules := codeRules
	if !rewrite {
		rules = codeDeletionRules
	}
	return "You write a Starlark program that reduces ONE tool output to what the agent needs next.\n\n" +
		"WHAT THE AGENT IS DOING NOW (reduce toward this):\n" + g + "\n\n" +
		keepBlock + label + "\n" + shown + "\n\n" + rules + "\n\n" + codeExample
}

// buildCodePromptSplit returns (system, user): the invariant preamble and the per-call
// variable part. Same total content as buildCodePrompt, reordered so the stable half can
// be a cacheable prefix. Order matters — the cacheable block must come FIRST on the
// wire, which is exactly what a `system` block gives us.
func buildCodePromptSplit(bodyText, goal string, keepIDs []string, rewrite bool) (system, user string) {
	return codeSystemPreamble(rewrite), buildCodeUserPart(bodyText, goal, keepIDs)
}

// buildCodeUserPart is the VARIABLE half: the goal, the keep-list, and the tool output.
func buildCodeUserPart(bodyText, goal string, keepIDs []string) string {
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
	shown := bodyText
	if isJSONContainer(bodyText) {
		if v := parseBody(bodyText); !isRawString(v) {
			if b, err := json.MarshalIndent(v, "", "  "); err == nil {
				shown = string(b)
			}
		}
	}
	label := "FULL TOOL OUTPUT (INPUT is exactly this):"
	if len(shown) > maxCodeContentChars {
		half := maxCodeContentChars / 2
		shown = shown[:half] + "\n…[middle elided in this prompt; the real INPUT at runtime is the FULL output]…\n" + shown[len(shown)-half:]
		label = "TOOL OUTPUT (head+tail; the real INPUT at runtime is the FULL output):"
	}
	return "WHAT THE AGENT IS DOING NOW (reduce toward this):\n" + g + "\n\n" +
		keepBlock + label + "\n" + shown
}

func isRawString(v any) bool {
	_, ok := v.(string)
	return ok
}

// isJSONContainer reports whether the WHOLE trimmed body is a valid JSON object or
// array. Used to decide whether to pretty-print the body for the model — a bare
// number, a line-numbered file read, or a log that merely begins with a digit must
// NOT be treated as JSON (json.Decode would consume a leading number and mangle it).
func isJSONContainer(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || (t[0] != '{' && t[0] != '[') {
		return false
	}
	return json.Valid([]byte(t))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
