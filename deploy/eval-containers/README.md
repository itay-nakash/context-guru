# Running lab-cx inside eval-containers

[eval-containers](https://github.com/exgentic/eval-containers) runs `one benchmark +
one agent + one model`, with the agent's LLM traffic always flowing through a
**gateway** (bifrost/litellm) on port 4000. `lab-cx` slots into that request path as a
plain HTTP proxy — no changes to the agent images, no changes to the gateway.

The image is built from this repo's root `Dockerfile`:

```sh
docker build -t ghcr.io/kagenti/lab-context-engineering:latest .
```

## Placement A — before the gateway (recommended)

```
runner (agent) ──▶ lab-cx ──▶ gateway ──▶ provider
```

lab-cx sees what the agent sends, in its native protocol, before any model-name
rewrite — the right place to control the agent's context. Use
[`compose.override.yaml`](compose.override.yaml):

```sh
docker compose -f compose.yaml -f .../deploy/eval-containers/compose.override.yaml up \
  -y --abort-on-container-exit
```

It adds a `winnow` service on the `internal` network, repoints the runner's
`ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` / `GOOGLE_GEMINI_BASE_URL` at it, and sets
`--upstream http://gateway:4000` so lab-cx forwards the full (prefixed) path on to the
gateway. The agent still holds only `sk-proxy`; the real key stays in the gateway.

## Placement B — after the gateway

```
runner (agent) ──▶ gateway ──▶ lab-cx ──▶ provider
```

lab-cx sees normalized (OpenAI-shaped) traffic. Point the gateway's `OPENAI_API_BASE`
at lab-cx (`http://winnow:8080`) and put `winnow` on the `upstream` network, with
`--upstream` set to the real provider base.

## Notes

- **Suffix routing**: lab-cx reduces any path ending in `/v1/messages` or
  `/chat/completions`, so the eval prefixes (`/anthropic`, `/openai/v1`) work
  unchanged. The Gemini prefix (`/genai`) is forwarded untouched (fail-open) until the
  Gemini surface lands.
- **Isolation/cost invariants are preserved**: lab-cx adds no egress (before-gateway it
  lives on `internal` only), doesn't touch `sk-proxy`/`TASK_ID`, and the gateway keeps
  emitting OTel + enforcing `EVAL_MODEL_MAX_BUDGET`.
- **Measuring the effect**: compare `trajectory.jsonl` / `result.json` token + cost
  totals with and without the override on the same `EVAL_TASK_ID`.
