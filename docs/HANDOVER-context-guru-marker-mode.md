# Handover — context-guru changes for AuthBridge integration (v1)

**Audience:** a coding agent that will **review, verify, and fix** the changes below in
`lab-context-engineering` (context-guru). A separate track continues on `kagenti-extensions`
(the AuthBridge `context-guru` plugin) + a demo — **that is not your job here.** Your job is to
make the context-guru side correct, safe, and well-tested so the AuthBridge plugin can rely on it.

**Repo:** `github.com/kagenti/context-guru` (dir `lab-context-engineering`). Go 1.26.
**Branch/commit:** changes are currently uncommitted in the working tree. Nothing has been pushed.

---

## Why these changes exist

AuthBridge embeds context-guru in-process and compacts the outbound LLM request in its pre-LLM
hook. For the **first** integration we decided (with the human):

1. **No restoration in v1.** The AuthBridge plugin does not run the model-driven expand loop. So
   context-guru needs a way to reduce context **without** requiring the `<<cg:HASH>>` marker +
   Store stash that reversibility depends on.
2. **AuthBridge must build pure-Go** (`CGO_ENABLED=0`, static sidecar). context-guru's `skeleton`
   component pulls a cgo tree-sitter binding, which must not leak into the default build.

Two features implement this:

- **`marker_mode`** — a per-offload-component knob: `full` (default, current behavior) | `summary`
  (drop content, leave a non-resolvable breadcrumb, no stash) | `off` (drop, no marker, no stash).
- **`cg_skeleton` build tag** — isolates `skeleton` + `internal/treesitter` so the default build is
  pure-Go; skeleton is opt-in via `-tags cg_skeleton` (with cgo).

---

## What changed (files + intent)

### 1. `marker_mode` core
- **`components/component.go`** — added `Irreversible bool` to `Report`; updated the `Offload`
  interface doc to note the deliberate-lossy-drop exception.
- **`components/pipeline.go`** — `runOne` guard now `… && !rep.Irreversible`. This is the
  **load-bearing** change: the existing guard reverts an offload that shrinks the request but
  stashes nothing (a broken-reversibility bug). A `summary`/`off` drop shrinks without stashing
  **on purpose**, so it sets `rep.Irreversible` and is exempt. The never-worse growth guard below
  it is untouched.
- **`components/offload/marker.go`** (NEW) — the whole knob:
  - `type markerMode` + `parseMarkerMode(string)` (unknown/empty → `full`, back-compatible).
  - `mark(c, rep, mode, original, hint) (token, key string)` — the single seam every marker-emitting
    component calls where it used to write `Store.Put` + `expand.Marker(key)+hint`:
    - `full` → stash, return `<<cg:HASH>>`+hint and the key.
    - `summary` → set `rep.Irreversible`, return `expand.SummaryMarker` (`⟪cg⟫`), no key, no stash.
    - `off` → set `rep.Irreversible`, return `""`, no key, no stash.
- **`expand/expand.go`** — added `SummaryMarker = "⟪cg⟫"` and `HasPlaceholder(s)` (matches a
  resolvable `<<cg:HASH>>` marker OR the summary sentinel). Purpose: cross-turn skip-detection must
  recognize summary/off placeholders too, or a later component/turn re-processes them and busts the
  prefix cache.

### 2. `marker_mode` wired into every marker-emitting component
Each got a `MarkerMode string \`yaml:"marker_mode"\`` config field (default `full`), a `mode
markerMode` struct field, the `expand.ParseMarkers(...) > 0` skip check swapped to
`expand.HasPlaceholder(...)`, the emit site routed through `mark(...)`, and the end-of-Offload
`if len(keys)==0 { rep.Skipped = true }` replaced with a `changed`/`emitted` counter (because
summary/off produce **no keys** even when they act):
- `components/offload/{collapse,dedup,cmdfilter,mask,failed_run,phi_evict,smartcrush,extract}.go`
- `components/offload/skeleton.go` (also behind the `cg_skeleton` tag — see below)
- `components/offload/summarize.go` — special: mode-aware `summaryWrapper(summary, key, mode)`;
  stashes the span **only in full mode**; sets `rep.Irreversible` otherwise; returns no key in
  summary/off. `tryReuse` / `state.go` checkpoints (`cg:sum:`, `cg:res:`) are **orthogonal** and
  still run in all modes — do NOT disable them when reviewing marker_mode. Guarded the checkpoint
  `Store.Put(cp.Key,…)` on `cp.Key != ""`.

> `cmdfilter` does NOT call `mark()` — it inlines the mode switch because it has a pre-commit
> never-worse token check and `mark()` stashes eagerly. Review this one carefully (below).

### 3. `cg_skeleton` build tag
- **`components/offload/skeleton.go`** — `//go:build cg_skeleton` header + a comment explaining it.
- **`internal/treesitter/treesitter.go`**, **`treesitter_test.go`** — `//go:build cg_skeleton`.
- **`internal/treesitter/stub.go`** (NEW) — `//go:build !cg_skeleton`, empty `package treesitter`
  so `go build ./...` under `CGO_ENABLED=0` links no tree-sitter grammars.
- **`components/all/skeleton_test.go`** (NEW) — the two skeleton tests moved here behind the tag;
  removed from `components/all/more_test.go` and `p4_test.go` (left one-line pointers).

### 4. New test
- **`components/all/markermode_test.go`** — asserts, using `collapse`: `full` stashes+resolves;
  `summary` leaves `⟪cg⟫` + no resolvable marker; `off` leaves no placeholder; and all three
  **actually reduce tokens** (i.e. the guard fix works — no revert).

---

## How to build & verify

```bash
cd lab-context-engineering

# Default build MUST be pure-Go (this is what AuthBridge relies on):
CGO_ENABLED=0 go build ./...            # expect: clean
CGO_ENABLED=0 go test ./...             # expect: all ok, skeleton pkg shows [no test files]
CGO_ENABLED=0 go vet ./...              # expect: clean

# skeleton opt-in still compiles (needs a C toolchain):
CGO_ENABLED=1 go build -tags cg_skeleton ./...
CGO_ENABLED=1 go test  -tags cg_skeleton ./components/all/ -run Skeleton
```

All of the above passed when this handover was written. If any fail, that's a regression to fix.

---

## Review checklist (scrutinize these — highest risk first)

1. **The guard change (`pipeline.go` `runOne`).** Confirm `!rep.Irreversible` only relaxes the
   "dropped-without-stash" case and NOT the never-worse growth case. Confirm a genuine bug (a
   `full`-mode component that shrinks but forgets to stash) is still reverted. Consider a test that
   a fake offload with `full` mode + no stash IS reverted, while `Irreversible=true` is not.
2. **`cmdfilter.go`** — the only component that reproduces the mode logic by hand (not via `mark`).
   Verify: (a) the never-worse check still compares the FULL rewritten text incl. the token; (b)
   `full` stashes + appends the key, summary/off set `rep.Irreversible` and append nothing; (c) the
   `changed` counter is correct. This is the most error-prone edit.
3. **`summarize.go`** — verify `state.go` reuse still works in summary/off (byte-stable summary
   re-emission, no re-LLM call), that `cp.Key == ""` is handled everywhere it's used, and that a
   config that switches modes between turns doesn't crash or corrupt the checkpoint.
4. **Idempotency across turns.** For each component, confirm `expand.HasPlaceholder` correctly
   skips both `<<cg:…>>` and `⟪cg⟫`. In `off` mode there is **no** placeholder, so a message can be
   re-processed next turn — check each component is naturally idempotent on its own output in `off`
   mode (re-reducing already-reduced text should be a no-op / stable), or document the limitation.
5. **`⟪cg⟫` sentinel choice.** Non-ASCII. Confirm it survives JSON round-trip through
   `apply.Body`/bifrost splice and doesn't get mangled by any provider normalization. If risky,
   propose an ASCII sentinel (e.g. `[[cg]]`) — but keep `HasPlaceholder` and the regex in sync.
6. **Metrics semantics.** In summary/off, `Report.CacheKeys` is empty but `Report.Saved()` still
   reflects the token drop. Confirm emitters/telemetry don't misreport these as skips. The
   `rep.Skipped` flag must be false when a component actually acted.
7. **`marker_mode` reaches components.** Confirm the per-component `components:<name>: {marker_mode:
   …}` config actually flows through `config.Build` → each constructor (it's decoded per-component
   from `Components map[string]yaml.Node`). Add a config-level test if missing.
8. **Presets.** No preset sets `marker_mode`, so all presets keep `full` (reversible) behavior —
   confirm that's intended and unchanged. The AuthBridge side chooses non-full modes explicitly.

---

## Constraints to preserve (the AuthBridge side depends on these)

- **Default build stays pure-Go.** Do not import cgo/tree-sitter from any package that the default
  offload/reformat build pulls in. `skeleton` and `internal/treesitter` must remain behind
  `cg_skeleton`. If you refactor, re-run `CGO_ENABLED=0 go build ./...`.
- **Public API stability.** AuthBridge imports `apply` (`Body`, `BodyWithModel`), `config`
  (`LoadBytes`, `Build`, `NewStore`), `components` (`Pipeline`, `ModelSpec`, `Model`), `store`
  (`Store`), and blank-imports `components/offload` + `components/reformat`. Don't rename/break
  these. `apply.BodyWithModel` and `components.ModelSpec{Incoming,Static}` are load-bearing.
- **`marker_mode` default = `full`.** Existing configs and the proxy path must be unchanged.

---

## Known open questions / nice-to-haves (not blockers)

- Should `off` mode emit a minimal ASCII sentinel too (for skip-detection parity with `summary`)?
  Currently `off` leaves nothing, relying on per-component idempotency.
- A config-decode test that a bad `marker_mode` value is rejected or safely defaults (currently
  unknown → `full` silently; consider erroring instead).
- `summarize` in `off` mode still stores the checkpoint (`cg:sum:`) for reuse — confirm that's the
  desired trade (cache-stability vs "off means store nothing").

## Out of scope for this review
- The AuthBridge plugin (`kagenti-extensions/authbridge/authlib/plugins/contextguru/`), its
  build wiring, and the demo — handled on the other track. Don't edit `kagenti-extensions`.
- The model-driven expand/restoration loop (deferred to a later integration).
