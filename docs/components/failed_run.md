# failed_run

!!! info "Offload — lossy, reversible"
    Keeps the most recent test/build run in full, collapses every earlier superseded run to a pointer + marker.

## How it works

`failed_run` recognizes test/build run output (regex: `N passed/failed`, `BUILD SUCCESS/FAIL`,
`Traceback`, `FAILED`, `panic:`, `npm ERR!`, pytest session banners). It keeps the **most recent**
run in full and collapses every earlier run to a pointer + `<<cg:HASH>>` marker — a superseded run
is safely recoverable. Needs ≥2 run-like outputs. False positives cost only an expand round-trip,
never data.

## Before → After

```
before:  [run 1] 3 failed, 5 passed …   [run 2 after fix] 8 passed
after:   [superseded by a later run] <<cg:7d1c…>> [full output: …]   [run 2] 8 passed
```

## Lossiness

Lossy but reversible — superseded runs are stashed and recovered via `context_guru_expand` /
`GET /expand`.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `min_tokens` | 100 | Skip runs smaller than this token count. |

## When it shines

Iterative fix→re-run loops.

## When it's inert

<2 runs detected, small outputs.

See also: [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
