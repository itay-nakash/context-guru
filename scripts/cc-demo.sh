#!/usr/bin/env bash
# Real Claude Code integration demo: route `claude` through lab-cx and read /stats.
# Requires: lab-cx built (make build), and ANTHROPIC_BASE_URL + ANTHROPIC_AUTH_TOKEN in
# the env (an Anthropic-compatible endpoint). Uses claude-haiku-4-5 as the agent model.
#
# Usage: ANTHROPIC_BASE_URL=... ANTHROPIC_AUTH_TOKEN=... scripts/cc-demo.sh
set -euo pipefail

PORT="${PORT:-8090}"
MODEL="${LCX_MODEL:-claude-haiku-4-5}"
BIN="${BIN:-./bin/lab-cx}"
: "${ANTHROPIC_BASE_URL:?set ANTHROPIC_BASE_URL to the upstream model endpoint}"
: "${ANTHROPIC_AUTH_TOKEN:?set ANTHROPIC_AUTH_TOKEN}"

demo=$(mktemp -d)
printf 'package main\nimport "fmt"\nfunc main(){ fmt.Println(greet("world")) }\nfunc greet(n string) string { return "hello "+n }\n' > "$demo/main.go"
printf '# cc-demo\nTiny repo for the lab-cx Claude Code integration demo.\n' > "$demo/README.md"

"$BIN" proxy --addr "127.0.0.1:$PORT" --preset balanced \
  --upstream "$ANTHROPIC_BASE_URL" \
  --extract-model "$MODEL" --extract-provider anthropic --extract-auth bearer \
  --extract-base "$ANTHROPIC_BASE_URL" >/tmp/cc_proxy.log 2>&1 &
proxy=$!
trap 'kill $proxy 2>/dev/null' EXIT
sleep 2

settings=$(mktemp)
cat > "$settings" <<JSON
{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:$PORT","ANTHROPIC_AUTH_TOKEN":"$ANTHROPIC_AUTH_TOKEN","ANTHROPIC_MODEL":"$MODEL","ANTHROPIC_SMALL_FAST_MODEL":"$MODEL"}}
JSON

echo "== stats before ==" ; curl -s "http://127.0.0.1:$PORT/stats"; echo
( cd "$demo" && claude -p "Read main.go and README.md and summarize each in one line." \
    --settings "$settings" --dangerously-skip-permissions )
echo "== stats after =="  ; curl -s "http://127.0.0.1:$PORT/stats"; echo
rm -f "$settings"
