# Quickstart: Proxy

Run context-guru as a standalone proxy in front of your LLM provider, point an
agent at it, and watch the tokens drop. One port serves both the OpenAI and
Anthropic dialects.

## Prerequisites

- **Go 1.26** and a **C toolchain** — `CGO_ENABLED=1` is required (the
  `skeleton` component binds tree-sitter via cgo).
- The **bifrost** repo checked out beside this one — `go.mod` pins it with
  `replace github.com/maximhq/bifrost/core => ../bifrost/core`, so build from the
  parent directory that holds both repos.

!!! warning "Build from the parent directory"
    The local `replace` only resolves when `context-guru/` and `bifrost/` are
    siblings:

    ```
    <parent>/
      context-guru/     ← this repo (dir: lab-context-engineering)
      bifrost/          ← https://github.com/maximhq/bifrost
    ```

## Build

=== "go build"

    ```sh
    cd .../context-engineering            # dir containing lab-context-engineering/ and bifrost/
    CGO_ENABLED=1 go build -o bin/context-guru-proxy \
      ./lab-context-engineering/cmd/context-guru-proxy
    ```

=== "make"

    ```sh
    make build
    ```

=== "docker"

    ```sh
    # build context is the parent dir that holds both repos
    docker build -f lab-context-engineering/Dockerfile -t context-guru:local .
    ```

## Run

```sh
context-guru-proxy --preset balanced          # or --config cg.yaml
```

It listens on `:4000` by default (set `LISTEN_ADDR` to change). See
[Config & environment](../reference/config.md) for all flags and env vars, and
[Presets](../reference/presets.md) to pick a pipeline.

## Point an agent at it

Set the base URL to the proxy — one port, both dialects:

=== "Anthropic"

    ```sh
    ANTHROPIC_BASE_URL=http://localhost:4000/anthropic
    ```

=== "OpenAI"

    ```sh
    OPENAI_BASE_URL=http://localhost:4000/openai/v1
    ```

In gateway mode, set `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` on the proxy and it
injects the real key on forward; leave them empty to pass the client's auth
through.

## Send a request

```sh
curl -s -XPOST localhost:4000/openai/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "list users"},
      {"role": "tool", "tool_call_id": "c1",
       "content": "[{\"id\":1,\"name\":\"Alice\"},{\"id\":2,\"name\":\"Bob\"}]"}
    ]
  }'
```

## Check savings

```sh
curl -s localhost:4000/stats | jq        # token-weighted savings rollup
```

`/stats` reports `tokens_before/after`, `saved_tokens`, `savings_pct`
(token-weighted), plus `wasted_tokens`/`bounces` and per-component rollups. See
[Measure savings](../how-to/measure-savings.md).

## Recover an offloaded original

A lossy Offload leaves a `<<cg:HASH>>` marker; recover the stashed original by
its id:

```sh
curl -s 'localhost:4000/expand?id=<HASH>'
```

In normal operation the model recovers it automatically via the
`context_guru_expand` tool — see [Recover offloaded context](../how-to/recover-context.md).

## Per-request headers

| Header | Effect |
|---|---|
| `x-context-guru-session: <id>` | Set the session key explicitly (otherwise a content hash). |
| `x-context-guru-bypass: true` | Skip the pipeline entirely for this request. |

The full route and header reference is in [Routes & headers](../reference/routes.md).
