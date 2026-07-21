# cacheinject

!!! info "Reformat — lossless"
    Places an Anthropic `cache_control` breakpoint at a stable prefix boundary so the provider KV cache hits across turns.

## How it works

`cacheinject` places an Anthropic `cache_control: {type: ephemeral}` breakpoint on the last content
block of the message just **before** the newest turn (a stable prefix boundary), so the provider KV
cache hits across turns. It adds a control directive and changes no model-visible content.

The savings lever is provider-side cache hits, invisible to `/stats` token counts. `/stats` will
list it under `top_passthrough` since it saves no *content* tokens — that's expected, not dead
weight.

## Before → After

```
before:  [ … prev turn last block ]                after:  [ … prev turn last block  {cache_control: ephemeral} ]
         [ newest turn ]                                    [ newest turn ]
```

## Lossiness

None. It only attaches a cache directive; model-visible content is unchanged.

## Configuration

_No configuration._

## When it shines

Anthropic/Bedrock/Vertex agents that don't self-cache (the savings lever is provider-side cache
hits, invisible to `/stats` token counts).

## When it's inert

Non-cache-aware providers, string-content messages (can't carry a block breakpoint), a breakpoint
already present.

See also: [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
