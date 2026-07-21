# mask

!!! info "Offload — lossy, reversible"
    Age-based garbage collection: keep the newest few tool outputs verbatim, replace older ones with a short marker + stash.

## How it works

`mask` is age-based garbage collection: it keeps the newest `keep_recent` tool outputs verbatim and
replaces older ones (≥ `min_tokens`) with a short `<<cg:HASH>>` marker + stash. It is complementary
to the content-based offloaders.

## Before → After

```
after (older):  [older tool output masked] <<cg:…>> [full output: call context_guru_expand]
```

## Lossiness

Lossy but reversible — masked outputs are stashed and recovered via `context_guru_expand` /
`GET /expand`.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `keep_recent` | 3 | Newest tool outputs kept verbatim. |
| `min_tokens` | 100 | Only mask older outputs at least this large. |

## When it shines

Long agent trajectories where old tool results are unlikely to matter. In the `agent` preset it is
the biggest lever (~27% content-token savings, no reward loss — see
[RESULTS.md](../RESULTS.md)).

## When it's inert

≤ `keep_recent` tool outputs, small outputs.

See also: [Components overview](../components.md) · [Choose a preset](../how-to/choose-a-preset.md)
