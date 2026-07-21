# toon

!!! info "Reformat — lossless"
    Re-encodes a JSON array of uniform, flat objects as TOON (Token-Oriented Object Notation) — one header, one row per element, no repeated keys or quotes.

## How it works

`toon` re-encodes a JSON array of uniform, flat objects as **TOON** (Token-Oriented Object
Notation): one header listing the field names once, then one comma-separated row per element. It
drops the braces, repeated keys, and quotes that dominate a JSON array's token cost. It's a
Reformat (repack in place, nothing stashed): every scalar value is preserved, with one small
representational simplification — JSON `null` renders as an empty cell (indistinguishable from
`""`). Only arrays whose elements share one shared scalar key-set are encoded; anything nested,
ragged, or non-array is left untouched, and the pipeline's never-worse guard reverts any case that
fails to shrink.

## Before → After

```
before:  [{"id":1,"name":"Alice"},{"id":2,"name":"Bob"}]
after:   [2]{id,name}:
         1,Alice
         2,Bob
```

## Lossiness

None — nothing stashed. Every scalar is preserved; the only representational change is JSON `null`
→ empty cell (indistinguishable from `""`).

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `min_tokens` | 50 | Skip tool outputs smaller than this token count. |

## When it shines

Long homogeneous JSON arrays (the llm-d TOON config).

## When it's inert

Nested/ragged/non-array output, or output that is not smaller after re-encoding.

See also: [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
