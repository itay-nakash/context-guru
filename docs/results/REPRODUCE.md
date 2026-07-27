# Reproducing the SWE-bench Verified evaluation

How to install everything and run the three arms of the study — **baseline** (no
compaction), **context-guru**, and **headroom** — on SWE-bench Verified with the
Harbor harness and the `claude-code` agent on `aws/claude-sonnet-5`.

All three arms share one harness pattern: start a proxy (or none, for baseline) in
front of the model gateway, point Harbor's `claude-code` agent at it, run the tasks,
and parse each trial's `result.json` + `agent/trajectory.json` for reward, steps, and
cache-aware token/cost metrics.

## 1. Prerequisites (one-time)

- **OS**: Linux (RHEL/Fedora family here), passwordless `sudo`.
- **Go 1.26** + `CGO_ENABLED=1` (context-guru's `cg_skeleton` build tag needs cgo/tree-sitter).
- **Python 3.13** + [`uv`](https://docs.astral.sh/uv/) (Harbor needs ≥3.12).
- **Docker** (each task builds a container). Use it via `sg docker -c '...'` — do **not**
  loosen the socket permissions.
- **Harbor** checked out at `/home/vpcuser/projects/context-engineering/harbor`
  (`uv sync` in it).
- **Model gateway creds** in `~/.claude/settings.json` under `env`
  (`ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`) — the IBM LiteLLM gateway exposing
  `aws/claude-sonnet-5` (agent) and `aws/claude-haiku-4-5` (context-guru's cheap
  compaction model).

### Docker Hub authentication (required — avoids the 429 pull-quota wall)

SWE-bench task images live on Docker Hub (`docker.io/swebench/…`). Harbor pulls one per
task; ~100 pulls exhausts the **anonymous** quota (HTTP 429) and environments fail to
build. Authenticate once (an authenticated account has a separate 200-pulls/6h quota):

```
sg docker -c 'docker login -u <your-dockerhub-user>'   # paste a Read-only Personal Access Token
```

The harness also passes `--no-delete` so images persist and are **not** re-pulled across
runs. (Optionally `gh auth login` for `ghcr.io`, but the SWE images are not mirrored there.)

### Task list

`/tmp/cg-runs/swe50.txt` — 50 tasks (every 10th of SWE-bench Verified's 500).
`/tmp/cg-runs/swe3-verify.txt` — a 3-task smoke subset.

## 2. Build context-guru

```
cd /home/vpcuser/projects/context-engineering/context-guru
CGO_ENABLED=1 go build -tags cg_skeleton -o /tmp/cg-runs/cg-proxy-d1 ./cmd/context-guru-proxy
CGO_ENABLED=1 go test -tags cg_skeleton ./...      # all green
```

## 3. Run baseline + context-guru (one command)

The harness [`deploy/harbor/swebench.py`](https://github.com/rossoctl/context-guru/blob/main/deploy/harbor/swebench.py) starts the
proxy per config (`off` = transparent passthrough baseline; `codesmart` = the tuned
cache-aware config), points Harbor at it (`ANTHROPIC_BASE_URL=http://<LAN-IP>:4000/anthropic`),
runs the tasks, and writes `summary.json` + `rows-*.json`.

```
cd /home/vpcuser/projects/context-engineering/context-guru
# baseline + context-guru, 50 tasks, 2 trials each, dump context-guru's change log:
nohup python3 -u deploy/harbor/swebench.py \
  --tasks /tmp/cg-runs/swe50.txt \
  --configs off codesmart \
  --jobs-root /tmp/cg-runs/study --n 2 \
  --dump-configs codesmart > /tmp/cg-runs/study.log 2>&1 &
```

Notes / gotchas learned the hard way:
- The login shell has **`errexit`**: a leading `pkill` that matches nothing returns 1 and
  aborts the whole launch. Launch with a bare `nohup python3 -u <abs-path> … &` (no
  leading `pkill`/`cd`, no trailing `sleep`).
- `codesmart` uses `CACHE_MODE=auto` (cache-aware on a prompt-caching agent) and
  `CHEAP_MODEL=aws/claude-haiku-4-5` for its own compaction calls.

## 4. Run headroom

Headroom (`headroom-ai`, an HTTP proxy like context-guru) via its harness
`/tmp/hd-runs/swebench_headroom.py` (a copy of
`swebench.py` pointed at the headroom binary/port). Two flags are **required** for the
claude-code + Bedrock-gateway combo (both are real findings):
- `HEADROOM_TOOL_SEARCH=0` — headroom otherwise injects first-party-only
  `tool_search_tool_*` that the Bedrock gateway can't honor → `Content block not found`.
- `--no-ccr` — headroom's reversible-retrieval SSE re-emission corrupts claude-code's
  stream; its own `--help` says `--no-ccr` is right for streaming (compressors stay
  active, only reversibility is lost).

```
python3 /tmp/hd-runs/swebench_headroom.py --tasks /tmp/cg-runs/swe50.txt \
  --jobs-root /tmp/hd-runs/study --n 2
```

Use a **different proxy port** (e.g. 4010) and jobs-root than context-guru so the two
never collide; reuse the same authenticated Docker Hub + `--no-delete`.

## 4b. Run rtk (Rust Token Killer)

rtk is **not** a request-stream proxy — it is a Claude Code **`PreToolUse` hook** that
rewrites Bash commands (`pytest …` → `rtk pytest …`, `cat f` → `rtk read f`,
`git status` → `rtk git status`) **inside the task container**, compressing bash output
at the shell before it enters the model context. So there is nothing to proxy for
compaction: model routing is made **identical to the baseline** by running the same
context-guru `off` passthrough proxy on `:4000`. The only difference from baseline is
the in-container bash compression.

**1) Fetch the rtk binary** (static-musl, runs on any SWE-bench image):

```
mkdir -p /tmp/rtk-runs && cd /tmp/rtk-runs
curl -fsSL -o rtk.tgz https://github.com/rtk-ai/rtk/releases/download/v0.43.0/rtk-x86_64-unknown-linux-musl.tar.gz
tar xzf rtk.tgz && chmod +x rtk && ./rtk --version   # rtk 0.43.0
```

**2) Add the `claude-code-rtk` agent to the Harbor checkout** (one new file + two lines).
The agent subclasses the stock `claude-code` agent and, per trial: uploads the rtk binary
to `/usr/local/bin/rtk`, installs the rtk `PreToolUse` hook into the exact
`CLAUDE_CONFIG_DIR` Claude Code reads (`rtk init -g --auto-patch`, which honors that
env var — `--auto-patch` is **required** headless, else the piped-stdin patch defaults to
"no"), and dumps `rtk gain --all --format json` to `/logs/agent/rtk-gain.json`.

- add `harbor/src/harbor/agents/installed/claude_code_rtk.py` (a copy lives at
  [`deploy/harbor/claude_code_rtk_agent.py`](https://github.com/rossoctl/context-guru/blob/main/deploy/harbor/claude_code_rtk_agent.py));
- register it: `CLAUDE_CODE_RTK = "claude-code-rtk"` in `harbor/src/harbor/models/agent/name.py`
  and `AgentName.CLAUDE_CODE_RTK: "harbor.agents.installed.claude_code_rtk:ClaudeCodeRTK"`
  in `harbor/src/harbor/agents/factory.py`.

**3) Run the 50 tasks** (starts the `off` proxy, runs `-a claude-code-rtk`):

```
cd /home/vpcuser/projects/context-engineering/context-guru
RTK_BIN_HOST=/tmp/rtk-runs/rtk python3 -u deploy/harbor/swebench_rtk.py \
  --tasks /tmp/cg-runs/swe50.txt --jobs-root /tmp/rtk-runs/swe50 --n 2
```

End-to-end metrics (reward, steps, cache-read/write, billed cost, cache-hit, wall) come
from the unchanged claude-code trajectory parser — measured **identically** to the other
three arms. Each trial's `agent/rtk-gain.json` also carries rtk's own bash-output savings
ledger (its native `bytes/4` estimate, a bash-output denominator — **not** the whole-request
content% the proxies report).

## 5. Analyze & plot

```
# Four-way matched analysis (baseline vs context-guru vs headroom vs rtk) — every dimension,
# per-task + aggregate + per-component, cumulative & unique tokens → deep_analysis.json:
python3 deploy/harbor/deep_analysis.py --out /tmp/cg-runs/deep
# Figures (validated CVD-safe palette) → docs/img/benchmark/:
/tmp/cg-runs/plotenv/bin/python deploy/harbor/deep_plots.py /tmp/cg-runs/deep/deep_analysis.json --out docs/img/benchmark
# Per-config result pages (per-task tables + totals):
python3 deploy/harbor/gen_result_docs.py <rows.json> "<label>" docs/results/<config>.md [--summary <summary.json>]
# Per-component unique-vs-cumulative token savings from the change-log dump:
python3 deploy/harbor/dump_unique.py /tmp/cg-runs/dump-swebench-codesmart.jsonl
```
(`deep_analysis.py` reads each run's `rows-*.json` at the paths in its `SRC` map —
`/tmp/cg-runs/final50/rows-off.json`, `/tmp/cg-runs/final50-v6/rows-codesmart.json`,
`/tmp/hd-runs/swe50/rows-hd-cache.json`, and — when present —
`/tmp/rtk-runs/swe50/rows-rtk.json`. Arms whose rows file is missing are dropped, so the
matched set is the intersection of tasks scored with **no exception** in every arm
supplied.)

Metrics collected per trial: reward, steps, prompt/cached/creation/read/completion
tokens, cache-aware billed cost (fresh $2/M · cache-read $0.20/M · cache-write $2.50/M ·
output $10/M), cache-hit rate, proxy savings %, per-component savings + own latency,
context-guru's own cheap-model cost (priced at the haiku rate), and expand/restoration
bounces.

## 6. Result docs

- [`baseline.md`](baseline.md) — baseline (`off`) full results.
- [`context-guru.md`](context-guru.md) — context-guru `codesmart` full results.
- [`headroom.md`](headroom.md) — headroom full results.
- [`rtk.md`](rtk.md) — rtk (Rust Token Killer) full results.
- [`comparison.md`](comparison.md) — the four-way comparison across all metrics.
