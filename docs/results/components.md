# Component internals & real examples — context-guru vs headroom

How each tool actually compacts context: what every component does, when it triggers,
how it works, and **real before→after examples pulled from the 50-task run logs**. Both
tools are **live-zone-only** (they only touch the newest, not-yet-cached content and
leave the provider's cached prefix byte-identical) — that shared discipline is why
neither busts the cache. They differ in *how* they compress: context-guru is a **hybrid**
(deterministic passes + a cheap LLM for relevance-aware skeletonization), headroom is
**fully deterministic** (AST / ML-scorer / structural compressors, no generative model).

All numbers are from the matched 48-task run (see [comparison.md](comparison.md)).

---

## context-guru — pipeline `[format, dedup, failed_run, cmdfilter, extract_llm, extract, cacheinject]`

Two type-enforced kinds: **Reformat** (lossless repack, no stash) and **Offload**
(lossy-but-reversible: leaves a `<<cg:HASH>>` marker + stashes the original, recoverable
via the `context_guru_expand` tool). Every component is fail-open isolated — an error or
a size-growth reverts *that component only*.

**Cache-safety invariant.** An offloader only ever *creates* a new compaction on content
in the uncached tail (`TailOnly`, message index > `MaxCachedIdx`), and once it compacts an
output it **freezes the exact replacement bytes and replays them on every later turn**
(`state.go` freeze/reapply). The agent re-sends the full original each turn, so replaying
identical compacted bytes keeps the request prefix byte-stable and the cache warm — an
output never flips compacted→full→compacted (which would churn the cache).

### 1. `format` (Reformat, lossless)
Compact-re-encodes pretty-printed JSON tool outputs. Triggers on tool-role JSON ≥ 50 tok
that shrinks. **0 acts on SWE** (terminal/file text, not JSON). Latency ~41 ms total.

### 2. `dedup` (Offload)
Replaces a tool output byte-identical to an earlier one *in the same request* with a
pointer. Triggers on tool-role text ≥ 100 tok whose content-hash was seen earlier; the
pointer+marker must be strictly smaller.
> **Real example** (git diff re-sent): `181 → 21 tok` →
> `[identical to an earlier tool output] <<cg:3da56b20…>>`
Run: 7 acts, 1,120 cumulative / **160 unique** tok.

### 3. `failed_run` (Offload — auto-off on cached agents)
Collapses *earlier failed* test/build runs superseded by a later run (keeps passed runs +
the latest run verbatim). **On a cached agent it auto-disables new collapses** (`if
c.CacheAware { continue }`): a superseded run is old/already-cached, so collapsing it would
force a full-suffix cache-write for almost no saving (this was the dominant +cost in an
earlier design — 121 such transitions). Frozen collapses are still replayed for stability.
Run: **0 acts** (cache-aware), but still scans every run-like output (~6.7 s total — the
costliest *deterministic* detection).

### 4. `cmdfilter` (Offload)
Declarative DSL filters keyed on a command output's first line (builtin `pytest`,
`npm-install`, `make`): strip blank/`PASSED`/progress lines, cap length, keep failures.
> **Real example** (pytest session): `1140 → 1068 tok` — passing/blank noise stripped,
> failures + warnings kept verbatim.
Run: 3 acts.

### 5. `extract_llm` (Offload — the relevance-aware LLM pass)
A cheap **haiku**-class model writes a sandboxed **Starlark program** that trims *one*
tool output to what the agent needs next. It may delete or regex-rewrite, must preserve
ids/paths/errors verbatim, and may emit a one-line `SUMMARY` shown inline in the marker.

**When:** request ≥ 3000 tok; per-output floor **≥ 3000 tok** (`max(min_tokens, frac·window)`);
only NEW outputs in the uncached **tail** (`TailOnly`); ≤ **4 calls/request**, cadence every
request; skips already-expanded content (`MarkKeptVerbatim`). Reapply-first: a re-sent
output is replayed from the frozen result with **no model call**.

**How:** goal + KEEP-identifiers → `buildCodePrompt` → the model's program runs sandboxed
(json module + `re_*`, no imports/IO, 2 s limit) → validated (strictly smaller +
`IsContained` subsequence proof, unless rewrite mode) before splicing. **Reward-safe
skeletonization**: for a source file, keep imports / every signature / docstrings / any
KEEP- or goal-relevant line verbatim, and **keep the full body of any def that is short
(≤~15 lines), relevant, or adjacent to relevant code** — elide only *long unrelated*
bodies into `# … N lines elided (call context_guru_expand) …`. Per-output calls run in
**parallel** (a single-call batch was measured ~3× worse on tokens).

> **Real example — skeletonization** (sympy `normalforms.py` file read): `3928 → 2863 tok`
> — imports, docstring, and every `def` signature kept byte-identical; long unrelated
> bodies replaced with elision markers.
>
> **Real example — rewrite + SUMMARY** (a huge symbolic determinant): `4825 → 264 tok`,
> with the model's digest spliced next to the marker:
> `… [f(5) raw determinant (verify simplifies to 0); full expr preserved] <<cg:f824977c…>>`

Run: **31 model calls, 42 acts, 129,966 cumulative / ~18k unique tok, $0.31** haiku cost,
~167 s cumulative own-latency (the dominant latency contributor).

### 6. `extract` (Offload — deterministic, zero-latency)
No-LLM, conservative noise removal that runs every turn (byte-stable → cache-safe). First
`stripTerminalNoise` — **strip ANSI escapes and collapse carriage-return progress
redraws** (keep the final rendered line) — then collapse blank runs, progress-bar lines,
and consecutively-repeated blocks (k≤12), keeping every unique informative line.
> **Real example** (Django test run with repeated "Cloning test database…" lines):
> `409 → 121 tok` — duplicate lines collapsed to one; the result summary + failures kept.
Run: **245 acts, 34,293 cumulative / ~2.9k unique tok**, ~0.6 s total (near-zero).

### 7. `cacheinject` (Reformat)
Stamps an ephemeral `cache_control` breakpoint on the prefix boundary so the provider KV
cache hits across turns. No content change. Its payoff is systemic — the 97.8% cache-hit
rate the whole cache-aware design is tuned around.

---

## headroom (`headroom-ai` v0.32.1) — a `ContentRouter` of deterministic compressors

Runs as an HTTP proxy; a `ContentRouter` detects each content block's type (Magika +
regex) and dispatches to a type-specific compressor. **Live-zone-only** via
`frozen_message_count` (leading cache-anchored messages are never rewritten) + protection
windows (skip user messages, protect the 4 most-recent code blocks + all Read outputs +
error outputs). Ran in **`cache` mode** (compress only the live zone; `token` mode would
also rewrite the frozen prefix for ~25–35% more savings but busts the cache).
**All compressors are deterministic — no generative LLM** (`added_llm_cost $0`); the only
models are a *local* ONNX token-scorer (Kompress) and the Magika detector.

| compressor | what it does | run: events / tokens saved |
|---|---|---|
| **text** (Kompress) | ML-scored lossy prose compression (local ModernBERT scorer keeps top-value tokens) — the fallback for plain text/markdown | 140 / **18,417** |
| **code_aware** (AST) | tree-sitter AST compression: preserves imports/signatures/types/error-handlers, compresses function bodies, "output always parses" | 63 / **10,559** |
| **log** | collapses repeated/templated log lines, keeps ERROR/FAIL | 9 / 1,267 |
| **diff** | trims unified-diff context/noise hunks, keeps change lines (never lossy-chained — would break `git apply`) | 10 / 234 |
| **smart_crusher** (JSON) | structural compression of JSON arrays-of-dicts (column dedup/fold) | 3 / 162 |
| **tabular** | CSV/TSV/markdown tables via SmartCrusher | 1 / 26 |
| **search** | clusters grep/ripgrep `file:line:` matches (lossless) | 7 / — |

> **Real per-request examples** (`stats-hd-cache.json → recent_requests`):
> `29255 → 27978` (log), `26324 → 25473` (code_aware), `26100 → 25249` (text),
> `23356 → 22509` (mixed). Best single compression: **16,042 → 13,860 (13.6%)**.

**Why code_aware/text dominate and JSON is tiny:** coding tool-output is source code,
prose, and logs — not JSON arrays-of-dicts, so `smart_crusher` barely fires. **Note on the
headline number:** headroom reports `proxy_compression_saved = 675,564`, but ~660k of that
is `anthropic:tool_schema_compaction` (~825 tok/req of tool-*schema*, fired on nearly every
request), not tool-output content. The live-zone *content* compression is **30,665 tokens
across 233 events** → `content_savings_pct = 2.64%`.

**Reversibility (CCR)** stashes originals under `<<ccr:HASH>>` + a `headroom_retrieve`
tool — but its streaming handler re-indexes SSE content blocks, which **corrupts
claude-code's stream ("Content block not found")**, so it must run with `--no-ccr`. On
this run compression was therefore **one-way lossy** (0 retrievals). Run: **63.35 ms/req**
overhead, 0 failures.

---

## Head-to-head

| axis | context-guru | headroom | winner |
|---|---|---|---|
| approach | hybrid (deterministic + haiku LLM) | fully deterministic | — |
| trigger | pipeline; `extract_llm` on newest output ≥3000 tok, ≤4/req | per-block content-type detect, ≥500 chars | — |
| live-zone-only | yes (TailOnly + freeze/reapply) | yes (frozen prefix + protection windows) | tie |
| **billed cost** (matched) | **$25.71** | $28.19 | **context-guru** |
| **cache-read / cache-write** | **80.6M / 1.70M** | 91.1M / 1.76M | **context-guru** |
| reward (solved/48) | **42** | 40 | **context-guru** |
| mean steps | **31.0** | 34.6 | **context-guru** |
| added latency / req | 117 ms | **63 ms** | **headroom** |
| tool's own LLM cost | $0.31 | **$0** | **headroom** |
| raw content removed / req | 1.09% | **2.64%** | **headroom** |
| reversibility on streaming | **`expand` works** (SSE aggregation) | CCR off (corrupts claude-code SSE) | **context-guru** |
| exceptions (of 50) | 2 | **0** | **headroom** |

**The key nuance:** headroom removes more *raw* content per request (2.64% vs 1.09%), yet
context-guru ends up **cheaper** with **lower cache-read** (80.6M vs 91.1M). Why?
context-guru **freezes each compaction and replays it byte-identically every turn**, so a
reduction compounds across the whole session's re-sent history; and its LLM
skeletonization targets the biggest file reads that headroom's deterministic passes leave
larger. Both keep cache-write at/below baseline (1.70M vs 1.76M vs 1.77M) — **neither busts
the cache**.

**Verdict.** context-guru wins the dollar-and-reward metrics (cost, cache usage, steps,
reward-vs-headroom) and stays reversible on streaming; headroom wins the overhead metrics
(latency, $0 tool cost, zero exceptions) because it never puts a model on the hot path.
The gap on each side is the direct consequence of hybrid-LLM vs fully-deterministic
compaction.
