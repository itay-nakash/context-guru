# Quickstart: Compaction service

Run context-guru as a **stateless HTTP compaction service** — the pattern
[`llm-d-router`](https://github.com/ronenkat/llm-d-router)'s
`request-inline-compaction` step calls. It shrinks a request body and hands it
straight back, with no state and no markers.

## The `POST /compact` contract

```
POST <service>/compact         body = the inference request JSON
  200 + non-empty JSON  ->  caller swaps that body in (now smaller)
  anything else          ->  caller keeps the original (passthrough)
```

!!! note "One-way by design"
    This mode sets `store: { enabled: false }` and `marker_mode: "off"`, so
    nothing is stored and no `<<cg:HASH>>` markers appear — the returned body is
    a clean, directly-usable inference request. Compaction is irreversible here.
    It never calls upstream and it never errors your caller: unparseable bodies,
    bodies without a `messages` array, or any component failure return the
    **original body with `200`**.

## Build

```sh
./examples/llm-d-service/build.sh        # -> bin/context-guru-proxy
```

## Run with a config

The deterministic `toon` config needs no credentials:

```sh
bin/context-guru-proxy --config examples/llm-d-service/configs/toon.yaml
# listens on :4000 (set LISTEN_ADDR to change)
```

## Try it

```sh
curl -s -XPOST localhost:4000/compact -H 'content-type: application/json' -d '{
  "model": "gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "list users"},
    {"role": "tool", "tool_call_id": "c1",
     "content": "[{\"id\":1,\"name\":\"Alice\",\"role\":\"admin\"},{\"id\":2,\"name\":\"Bob\",\"role\":\"user\"},{\"id\":3,\"name\":\"Carol\",\"role\":\"admin\"},{\"id\":4,\"name\":\"Dave\",\"role\":\"user\"},{\"id\":5,\"name\":\"Eve\",\"role\":\"admin\"}]"}
  ]
}'
```

The response is the same request with the tool output re-encoded as TOON (field
names once, then one row per element) — smaller, no markers:

```
[5]{id,name,role}:
1,Alice,admin
2,Bob,user
...
```

!!! tip "Below the gate = passthrough"
    Outputs below a component's `min_tokens` gate (or below an LLM config's
    `trigger`) pass through unchanged — that's expected. Send a larger tool
    output to see it act.

## Next

The full walkthrough — LLM-backed configs (`extract`, `summarize`), the Go
client, per-request overrides, and wiring into `llm-d-router` — is in the
[llm-d compaction service example](../examples/llm-d-service.md).
