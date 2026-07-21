# smartcrush

!!! info "Offload — lossy, reversible"
    Statistical JSON-array compressor: keep first/last items plus any error item, drop the rest, stash the full original.

## How it works

`smartcrush` is a statistical JSON-**array** compressor: it parses the array, keeps `keep_first` +
`keep_last` items plus any item whose raw JSON carries an error signal, drops the rest, and stashes
the full original behind a `<<cg:HASH>>` marker. Kept items are verbatim (schema-preserving). v1
uses fixed anchors (headroom's Kneedle adaptive-K is a documented refinement).

## Before → After

```
before:  [ {…}, {…}, … 200 items … ]
after:   [ item0, item1, item2, item198, item199 ] [5 of 200 items shown; full array: call …] <<cg:…>>
```

## Lossiness

Lossy but reversible — the full original array is stashed and recovered via `context_guru_expand` /
`GET /expand`. Kept items are byte-verbatim.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `min_items` | 5 | Minimum array length before crushing. |
| `min_tokens` | 200 | Output floor before crushing. |
| `keep_first` | 3 | Leading items kept verbatim. |
| `keep_last` | 2 | Trailing items kept verbatim. |

## When it shines

Long homogeneous JSON arrays (list endpoints, search hits) — the `mcp` preset.

## When it's inert

Non-array output, fewer than `min_items`, nothing to drop.

See also: [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
