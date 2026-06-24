# lab-context-engineering

Context engineering for LLM agents — reduce token cost without changing the agent or
hurting the result.

`lab-context-engineering` is a [Kagenti](https://github.com/kagenti/kagenti) platform
component. It is a single Go core that intercepts the traffic between an LLM agent and
the model API, reduces the context it carries (caching, lossless reduction, and
verified-lossless extraction), and forwards a cheaper request upstream. It fails open:
on any error the original request is forwarded untouched.

It is designed to run in three places from **one codebase**:

- **As a standalone proxy** (`lab-cx proxy`) — point any agent's `ANTHROPIC_BASE_URL` /
  `OPENAI_BASE_URL` at it. Works with Claude Code, Bob/OpenClaw, Codex, Cursor, Aider.
- **As an importable Go library** (`engine`, `surfaces`) — so a Kagenti
  [AuthBridge](https://github.com/kagenti/kagenti-extensions) plugin can wrap the engine
  in-process, with no extra network hop. The plugin lives in `kagenti-extensions` and
  depends on this repo; this repo contains no AuthBridge-specific code.
- **Inside eval-containers** — as a proxy placed before or after the eval gateway, so the
  component can be benchmarked across many agents and benchmarks.

## What it does

Three independent, toggleable levers (the lineage is the `winnow` research prototype):

| Lever      | What it does                                                                 | Loss profile          |
|------------|------------------------------------------------------------------------------|-----------------------|
| **Cache**  | Injects Anthropic `cache_control` breakpoints on the stable prefix.          | Lossless              |
| **Reduce** | Collapses stale/duplicate/empty content; re-encodes bulky output to cheaper, lossless forms; skeletonizes code. | Lossless, reversible  |
| **Extract**| A cheap model proposes a structured projection of huge tool/MCP output; accepted only if structurally contained in the original. | Verified-lossless, reversible |

Every reduction is reversible via a namespaced marker and a content-addressed rewind
store, so a downstream consumer (or the agent) can always recover the original bytes.

## Quick start

```sh
make build
./bin/lab-cx version
```

(`lab-cx proxy` and the engine library are landing incrementally — see `docs/`.)

## Repository layout

```
cmd/proxy/        the lab-cx binary
engine/           Engine: Transform / Expand, stage orchestration
surfaces/         anthropic | openai | gemini  wire <-> canonical request
config/           Settings + presets (safe | balanced | aggressive | coding | mcp)
observability/    OpenTelemetry GenAI emitter
docs/             design proposals, developer + integration guides
deploy/           eval-containers wiring
```

## License

Apache-2.0. See [LICENSE](LICENSE).
