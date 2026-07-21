# dedup

!!! info "Offload — lossy, reversible"
    Replaces a tool output byte-identical to an earlier one in the same request with a short pointer + marker.

## How it works

`dedup` replaces a tool output byte-identical to an earlier one in the same request with a short
pointer + `<<cg:HASH>>` marker. Exact match only (near-duplicate is deferred).

## Before → After

```
before:  <big config dump>  … (later, identical) <same big config dump>
after:   <big config dump>  … [identical to an earlier tool output] <<cg:1c8e…>>
```

## Lossiness

Lossy but reversible — the duplicated output is stashed and recovered via `context_guru_expand` /
`GET /expand`.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `min_tokens` | 100 | Skip outputs smaller than this token count. |

## When it shines

Agents that re-read the same file/command output repeatedly.

## When it's inert

No exact repeats, small outputs.

See also: [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
