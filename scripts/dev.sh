#!/bin/bash
set -e
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [ -f .env ]; then
  export $(grep -v '^#' .env | grep -v '^$' | xargs)
fi

export AMQP_URL="${AMQP_URL:-amqp://guest:guest@localhost:5672/}"
export API_PORT="${API_PORT:-8080}"
export SANDBOX_HOST="${SANDBOX_HOST:-localhost}"
export DOCKER_NETWORK="${DOCKER_NETWORK:-bridge}"

PIDS=()
cleanup() {
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null; done
  wait 2>/dev/null
}
trap cleanup EXIT INT TERM

start_svc() {
  local name=$1
  (cd "$ROOT/services/$name" && go run ./main.go 2>&1 | sed "s/^/[$name] /") &
  PIDS+=($!)
  sleep 0.3
}

start_svc gateway
start_svc orchestrator
start_svc figma-parser
start_svc codegen
start_svc sandbox
start_svc differ
start_svc notifier

(cd "$ROOT/web" && VITE_API_URL="http://localhost:$API_PORT" VITE_WS_URL="ws://localhost:$API_PORT/ws" npm run dev 2>&1 | sed 's/^/[web] /') &
PIDS+=($!)

echo "✅ All services running — UI: http://localhost:5173  API: http://localhost:$API_PORT"
wait -n 2>/dev/null || wait
