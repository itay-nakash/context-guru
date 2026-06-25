# Integrating with Bob (IBM Bob Shell) — real run

[Bob Shell](https://bob.ibm.com) is an OpenAI-compatible CLI coding agent. It integrates
with `lab-cx` by pointing its `CUSTOM_BASE_URL` at the proxy; the proxy reduces the
OpenAI-shaped traffic and forwards to Bob's backend. Bob is **not self-caching**, so
unlike Claude Code lab-cx's cache-injection lever fires on every request.

## Setup

```sh
# Bob's API key (here from ../winnow/.env: BOBSHELL_API_KEY=bob_...)
export BOBSHELL_API_KEY=...

# Start lab-cx: agent upstream = Bob's backend; cheap-model extractor = your gateway.
./bin/lab-cx proxy --addr 127.0.0.1:8093 --preset balanced \
  --upstream https://api.us-east.bob.ibm.com \
  --extract-model claude-haiku-4-5 --extract-provider anthropic \
  --extract-auth bearer --extract-base "$ANTHROPIC_BASE_URL"
```

Run Bob routed through the proxy (Bob's `custom` auth mode treats `CUSTOM_BASE_URL` as a
plain OpenAI provider and does its own auth handshake — drive the real `bob` CLI, a raw
curl with the `bob_` key is rejected by the backend as "invalid jwt"):

```sh
CUSTOM_BASE_URL=http://127.0.0.1:8093/v1 \
BOBSHELL_DEFAULT_AUTH_TYPE=custom \
BOBSHELL_API_KEY="$BOBSHELL_API_KEY" \
bob "List the function names defined in calc.py, comma-separated." \
  --accept-license --yolo --hide-intermediary-output
```

Then read `curl -s http://127.0.0.1:8093/stats`.

## Results (real, 2026-06-24, Bob → lab-cx → Bob backend)

**Correctness: 5/5 verifiable tasks correct, including a file edit — no harm.**

| Task | Expected | Bob's answer | OK |
|---|---|---|---|
| `17 * 23` | 391 | `391` | ✓ |
| function names in calc.py | add, sub | `add, sub` | ✓ |
| add `multiply(a,b)` to calc.py | file edited | `multiply` added to calc.py | ✓ |
| analyze 300-record data.json | 200 active, first inactive id 1000 | `200 active … id 1000` | ✓ |
| functions in calc.py (variant) | add, sub | correct | ✓ |

`/stats` after the 3-task suite:

```json
{ "requests": 6, "tokens_before": 2919, "tokens_after": 2919, "tokens_saved": 0,
  "reduction_ratio": 0, "cache_injected": 6, "extracted": 0, "stage_errors": 0,
  "added_latency_p50_ms": 3, "added_latency_p95_ms": 12 }
```

**What this shows:**
- **The integration works end-to-end** and **preserves correctness** (5/5, including a
  write) with negligible added latency (~3ms p50, 0 stage errors).
- **`cache_injected: 6/6`** — lab-cx injects `cache_control` breakpoints Bob never sends.
  On a multi-turn Bob session this bills the growing transcript prefix at provider
  cache-read rates (~10× cheaper) from turn 2 on — the lever a self-caching client like
  Claude Code declines. (Our content-token metric doesn't price the provider-side
  cache-read discount; `cache_injected` is the signal that it's engaged.)
- **`tokens_saved: 0` (content) here** because Bob *condensed the files itself* (it ran
  logic over `data.json` rather than dumping its 43 KB into the model context), so the
  Reduce/Extract content levers had no large raw output to act on.

## Where Bob content-reduction savings come from

Content reduction (Reduce/Extract) needs large **raw** tool outputs in the model context
— e.g. an MCP tool returning a big JSON array, or a `Read` of a large file kept across
turns. That path is proven:
- Component level on real fixtures: −93% aggregate ([../RESULTS-offline.md](../RESULTS-offline.md)),
  cheap-model extractor −56%…−80% ([../RESULTS-extract.md](../RESULTS-extract.md)).
- End-to-end on a self-caching agent reading a large file:
  [claude-code.md](claude-code.md) shows **50% token reduction** with correctness
  preserved. The same Reduce levers apply to Bob when it surfaces large raw outputs;
  enable extraction with a lower `floor` (see `configs/lab-cx.yaml`) to catch
  medium-size structured results.

## Reproduce

The full flow (proxy + 5-task suite + a large-file read) is scriptable exactly as above;
swap the `bob "<task>"` line for your own. Keep `--extract-*` pointed at a cheap model to
also exercise the extractor on large structured tool outputs.
