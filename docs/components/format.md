# format

!!! info "Reformat — lossless"
    Re-encodes a pretty-printed JSON tool output as compact JSON — same value, fewer whitespace tokens.

## How it works

`format` only acts on tool messages whose trimmed text starts with `{`/`[`, is valid JSON, is
≥ `min_tokens`, and gets smaller. It repacks the value as compact JSON, stripping the indentation
and newlines that dominate a pretty-printed payload's token cost. v1 is json-compact only (a TOON
encoder is planned — see [`toon`](toon.md)).

## Before → After

```
before:  { "id": 1,           after:  {"id":1,"name":"ada","tags":["x","y"]}
           "name": "ada",
           "tags": [ "x", "y" ] }
```

## Lossiness

None — nothing stashed. The value is identical; only whitespace tokens are removed.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `min_tokens` | 50 | Skip tool outputs smaller than this token count. |

## When it shines

Verbose pretty-printed JSON/MCP payloads.

## When it's inert

Already-compact JSON, non-JSON text, small outputs.

See also: [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
