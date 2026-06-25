# CLAUDE.md

Guidance for working in `lab-context-engineering`, a Kagenti platform component.

## What this repo is

A single **Go** core that reduces the token cost of LLM agent traffic. It ships as a
standalone proxy binary (`cmd/proxy`), an importable library (`engine`, `surfaces`), and
eval-containers wiring. Its lineage is the Python `winnow` prototype (`../winnow`), which
is the behavioral reference — port its *logic*, re-implement its transport in Go.

## Hard boundaries

- **No AuthBridge / kagenti-extensions code lives here.** That plugin is built in
  `kagenti-extensions` and depends on this repo. Keep the public API (`engine`,
  `surfaces`, `config`) clean and importable; never reach into another repo.
- **Fail open, always.** Any error in any compactor forwards the original request untouched.
  Reductions must be reversible (markers + rewind store). Never drop content that is only
  *predicted* unused — `provable_only` is on by default.

## Conventions

- Go 1.25, module `github.com/kagenti/lab-context-engineering`. `make fmt lint test build`.
- Match the surrounding code's style; keep packages small and single-purpose.
- **Commits: DCO sign-off is mandatory** — `git commit -s`. Author as the repo owner.
  AI attribution uses `Assisted-By:` — never `Co-Authored-By`, never a "Generated with"
  line. Conventional-commit titles.
- Observability follows OpenTelemetry **GenAI semantic conventions** (`gen_ai.*`).

## Layout

`cmd/proxy` (binary) · `engine` (Transform/Expand + Compactor pipeline) · `surfaces` (wire⇄canonical)
· `internal/*` (types, extract, relevance, signals, taxonomy, actions, cache, markers,
rewind, zones, session, compaction, tokens) · `config` · `observability` · `deploy` ·
`docs`.
