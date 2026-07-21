# phi_evict

!!! info "Offload — lossy, reversible"
    Ranks tool outputs by a lean-ctx-style context-field score and evicts the lowest until the transcript fits the budget.

## How it works

`phi_evict` ranks tool outputs by a lean-ctx-style context-field score and evicts the lowest until
the transcript fits `budget_tokens`. It never evicts the most recent tool output. Evicted outputs
are stashed behind a `<<cg:HASH>>` marker.

$$\Phi = w_R\cdot\text{relevance} + w_H\cdot\text{recency} - w_C\cdot\text{cost} - w_D\cdot\text{redundancy}$$

- **relevance** = keyword overlap with the last user message
- **recency** = position (newest = 1)
- **cost** = share of total tool tokens
- **redundancy** = 1 if a duplicate seen earlier

This is the scalar-ranking essence of lean-ctx's Context Field (the full heat-diffusion/MMR/bandit
machinery is a documented refinement).

## Before → After

```
before:  [ … many tool outputs, transcript over budget_tokens … ]
after:   [ … lowest-Φ outputs evicted to <<cg:…>> markers until it fits; newest kept … ]
```

## Lossiness

Lossy but reversible — evicted outputs are stashed and recovered via `context_guru_expand` /
`GET /expand`.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `budget_tokens` | 120000 | Target transcript size; evict until it fits. |
| `weights` | `balanced` | Φ weight preset (see below). |

Weight presets (`{R, H, C, D}`):

| Preset | R (relevance) | H (recency) | C (cost) | D (redundancy) |
|---|---|---|---|---|
| `balanced` | .40 | .20 | .30 | .10 |
| `aggressive` (punish cost) | .30 | .10 | .45 | .15 |
| `conservative` | .45 | .20 | .05 | .05 |

## When it shines

Very long transcripts that must fit a context budget.

## When it's inert

Total tool tokens ≤ budget, or ≤1 tool output.

See also: [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
