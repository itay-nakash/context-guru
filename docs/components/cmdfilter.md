# cmdfilter

!!! info "Offload — lossy, reversible"
    Shrinks tool output with declarative DSL filters, stashing the original and appending a recovery hint only when the filter was actually lossy.

## How it works

`cmdfilter` shrinks tool output with **declarative DSL filters** (see
[The DSL filter engine](dsl.md)). It matches a filter on the output's first non-empty line, applies
its 8-stage pipeline, stashes the original, and appends a recovery hint only when the filter was
actually lossy. It ships builtin `pytest` / `npm-install` / `make` filters, and is `Enabled` only
when ≥1 filter is loaded.

## Before → After

```
before:  pytest … 100 lines of PASSED + warnings + 1 failure
after:   <failures + summary, passing noise stripped, ≤80 lines> <<cg:…>> [full output: …]
```

## Lossiness

Lossy but reversible — the original is stashed and recovered via `context_guru_expand` /
`GET /expand`. A recovery hint is appended only when the filter actually dropped content.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `filters` | `[]` | Inline filter YAML docs, added with no recompile. |
| `disable_builtins` | `false` | Disable the shipped `pytest` / `npm-install` / `make` filters. |

## When it shines

Noisy but structured command/log output (test runners, package managers, build tools).

## When it's inert

Output whose first line matches no filter, or where filtering doesn't shrink it.

See also: [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
