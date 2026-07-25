#!/usr/bin/env bash
# Start context-guru-proxy in front of the IBM LiteLLM gateway, forcing the agent model.
# Env knobs: CG_PRESET (default off), CG_PIPELINE (comma list, wins over preset),
# CG_CONFIG_YAML (full yaml, wins over both), CG_MODEL (default aws/claude-sonnet-5),
# CG_DUMP (dump file), CG_PORT (default 4000).
set -euo pipefail
export PATH=$PATH:/usr/local/go/bin
CG=/home/vpcuser/projects/context-engineering/context-guru
creds=$(python3 -c 'import json,os;e=json.load(open(os.path.expanduser("~/.claude/settings.json")))["env"];print(e["ANTHROPIC_BASE_URL"]+"|"+e["ANTHROPIC_AUTH_TOKEN"])')
G=${creds%%|*}; T=${creds#*|}
PORT=${CG_PORT:-4000}; MODEL=${CG_MODEL:-aws/claude-sonnet-5}
export ANTHROPIC_UPSTREAM="$G" ANTHROPIC_API_KEY="$T" OPENAI_UPSTREAM="$G" OPENAI_API_KEY="$T" \
       FORCE_MODEL="$MODEL" LISTEN_ADDR=":$PORT" CONTEXT_GURU_DEBUG=1
[ -n "${CG_DUMP:-}" ] && export CONTEXT_GURU_DUMP="$CG_DUMP"
cd "$CG"
if [ -n "${CG_CONFIG_YAML:-}" ]; then printf '%s' "$CG_CONFIG_YAML" > /tmp/cg-run.yaml; exec "${BIN:-./bin/context-guru-proxy}" --config /tmp/cg-run.yaml
elif [ -n "${CG_PIPELINE:-}" ]; then printf 'pipeline: [%s]\n' "$CG_PIPELINE" > /tmp/cg-run.yaml; exec "${BIN:-./bin/context-guru-proxy}" --config /tmp/cg-run.yaml
else exec "${BIN:-./bin/context-guru-proxy}" --preset "${CG_PRESET:-off}"; fi
