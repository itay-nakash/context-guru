#!/usr/bin/env bash
# Run a harbor benchmark through the context-guru proxy with a clean, explicit env.
# Usage: run-bench.sh <proxy_anthropic_url> <jobs_dir> <harbor args...>
# Forces the agent's ANTHROPIC_BASE_URL to the proxy (overriding any inherited gateway),
# uses a placeholder key (proxy injects the real one), routes through the docker group.
set -euo pipefail
export PATH="$PATH:$HOME/.local/bin"
PROXY_URL="$1"; JOBS_DIR="$2"; shift 2
cd /home/vpcuser/projects/context-engineering/harbor
# clean env of any inherited gateway creds, set proxy target explicitly
export ANTHROPIC_BASE_URL="$PROXY_URL"
export ANTHROPIC_API_KEY="sk-proxy"
export ANTHROPIC_AUTH_TOKEN="sk-proxy"
unset CLAUDE_CODE_OAUTH_TOKEN CLAUDE_FORCE_OAUTH 2>/dev/null || true
exec setsid sg docker -c "ANTHROPIC_BASE_URL='$PROXY_URL' ANTHROPIC_API_KEY='sk-proxy' ANTHROPIC_AUTH_TOKEN='sk-proxy' PATH='$PATH' uv run harbor run $* --jobs-dir '$JOBS_DIR' --ae ANTHROPIC_BASE_URL='$PROXY_URL' --ae ANTHROPIC_API_KEY='sk-proxy'"
