# Integrating with Kagenti AuthBridge

The AuthBridge plugin lives in
[kagenti-extensions](https://github.com/kagenti/kagenti-extensions), **not** in this
repo. It depends on this module and wraps the engine in-process (the `sparc` plugin is
the structural precedent: a thin pipeline plugin delegating to a focused capability).
This repo's job is to expose a clean, importable Go API; it carries no AuthBridge code.

## The contract

```go
import (
    "github.com/kagenti/lab-context-engineering/config"
    "github.com/kagenti/lab-context-engineering/engine"
    "github.com/kagenti/lab-context-engineering/surfaces"
)

// Construct once (e.g. in the plugin's Init), reuse across requests.
eng := engine.New(config.Default(), nil, nil) // nil → in-memory rewind store + evictions
```

Per request, on the **outbound** LLM call (AuthBridge's `OnRequest` for an
`inference`-classified request), feed the body you already have through the matching
surface, transform, and write the rendered bytes back with `pctx.SetBody`:

```go
surface := surfaces.Anthropic{} // or surfaces.OpenAI{} per the detected wire format
req, token, err := surface.ToInternal(pctx.Body)
if err == surfaces.ErrUnsupported || err != nil {
    return // fail open: leave pctx.Body untouched
}
reduced, _ := eng.Transform(ctx, req)   // never errors; worst case is a no-op
out, err := surface.Render(reduced, token)
if err == nil {
    pctx.SetBody(out)                    // the only mutation AuthBridge needs
}
```

Declare `Capabilities{ReadsBody: true, WritesBody: true}` and
`RequiresAny: ["inference-parser"]` so a parser runs first. Ship it `on_error: observe`
to roll out in shadow mode.

## Recovering omitted content

Reductions are reversible. To serve an expand request, or to rehydrate before the
agent's own summarization turn:

```go
for _, id := range engine.FindMarkers(text) {
    if original, ok := eng.Expand(id); ok {
        // splice original back in
    }
}
```

## Notes

- **Fail open is the contract.** `Transform` never returns an error; on any internal
  fault it forwards the request unchanged. `ToInternal` returning `ErrUnsupported`
  (e.g. Gemini today) also means "forward untouched".
- **Why raw bytes, not parsed inference?** Feeding `pctx.Body` is the simplest path and
  avoids coupling to a parser's types. A `canon.Request` constructor from
  already-parsed messages can be added later if double-parsing ever shows up in a
  profile — it does not today.
- **State.** The default rewind store is in-memory (per process). For multi-replica
  recovery, supply a shared `store.Rewind` to `engine.New` (Redis/SQLite backends are a
  planned addition).
- **Observability.** Emit the engine's `Report` (tokens before/after/saved) via
  AuthBridge's OpenTelemetry GenAI conventions.
