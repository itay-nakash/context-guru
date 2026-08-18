# collapse

!!! info "Offload — lossy, reversible"
    Content-agnostic fallback for an oversized tool output: keep a head + tail window, stash the full original.

## How it works

`collapse` is the content-agnostic fallback for an oversized tool output nothing more specific
handled: it keeps a `head_lines` + `tail_lines` window and stashes the full original behind a
`<<cg:HASH>>` marker. It runs late (after `cmdfilter`/`format`) and skips content already marked.

## Before → After

```
before:  <2,000-line log>
after:   <first 20 lines>
         ... (1960 lines omitted) <<cg:44ab…>> [full output: call context_guru_expand]
         <last 20 lines>
```

## Lossiness

Lossy but reversible — the full original is stashed and recovered via `context_guru_expand` /
`GET /expand`.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `max_tokens` | 2000 | Threshold above which an output is collapsed. |
| `head_lines` | 20 | Lines kept from the start. |
| `tail_lines` | 20 | Lines kept from the end. |
| `max_frac` | 0 (off) | Threshold as a **fraction of the model's context window**. When the window is known this wins over `max_tokens`. |
| `marker_mode` | `full` | `full` (stash + resolvable marker) / `summary` / `off`. |

## When it shines

A catch-all last stage for huge outputs.

## When it's inert

Output ≤ `max_tokens`, or too few lines for head/tail to help.

See also: [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
