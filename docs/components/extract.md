# extract

!!! info "Offload — lossy, reversible (deterministic, no LLM)"
    Collapses only *obvious, provably redundant* noise in a tool output — consecutively
    repeated lines/blocks, runs of blank lines, and progress-bar/spinner churn — keeping every
    unique informative line verbatim.

## How it works

`extract` is the **deterministic, no-LLM** tool-output reducer. It runs on every request (cheap)
and is deliberately **conservative**: it removes only what is unambiguously redundant, so it can
never hide content the agent needs and force it to redo work. Specifically it:

- strips terminal noise (ANSI escapes, carriage-return progress churn);
- collapses a run of blank lines to a single blank;
- drops progress-bar / spinner lines (`… 45%`, `[####....]`, spinner glyphs);
- collapses a consecutively **repeated** line or block (up to 12 lines) to one copy.

It stashes the full original behind a `<<cg:HASH>>` marker (reversible), and — like every offloader
— only commits if the result (marker included) is actually smaller. Because it is a pure function of
the message content, its output is **byte-stable across turns**, so it never busts the KV cache.

!!! note "Relevance-aware trimming is a different component"
    Query-relevance projection, regex rewriting, and summarization are the job of the separate
    **[`extract_llm`](extract_llm.md)** component. Configure them together (`[extract, extract_llm]`,
    as in the `agent`/`general` presets) so the cheap deterministic pass runs every step and the LLM
    pass only fires on large outputs every few steps.

## Before → After

Captured live through the proxy (`pipeline: [extract]`) — 15 identical warning lines collapse to one,
blank runs collapse, and the original is stashed:

```
before:  Cloning into 'repo'...
         ...download progress...
         resolved 200 packages
         warning: peer dependency unmet          ← repeated 15×
         warning: peer dependency unmet
         … (13 more identical lines)


         build complete in 4.2s

after:   Cloning into 'repo'...
         ...download progress...
         resolved 200 packages
         warning: peer dependency unmet          ← one copy kept
         build complete in 4.2s
         <<cg:40b571fdebccdcd4>> [full output: call context_guru_expand]
```

## Lossiness

Lossy but reversible — the original is stashed and recovered via `context_guru_expand` /
`GET /expand`. In practice the only bytes dropped are exact duplicates and progress churn, so a
recovery is rarely needed.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `min_tokens` | 300 | Output floor before extraction runs (folds into `trigger.min_output_tokens`). |
| `trigger` | — | Optional gate: `min_output_tokens`, `min_request_tokens`, `min_messages`. |
| `marker_mode` | `full` | How the recovery marker is emitted: `full` \| `summary` \| `off`. |

## When it shines

Build/install logs, package-manager output, and any stream with repeated warnings, progress bars, or
duplicated blocks. Cheap enough to leave on for every request.

## When it's inert

Output below the floor, nothing obviously redundant to collapse, or a collapse that wouldn't shrink
the message once the marker is added.

See also: [`extract_llm`](extract_llm.md) · [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
