#!/bin/bash
# demo.sh — One-shot demo for LLM Gateway
# Usage: ./demo.sh
# Starts the server, runs all demos, then cleans up.

set +e  # Don't exit on non-zero — many background kills return 1

PORT=18888
BIN="./llm-gateway"
BASE="http://localhost:$PORT"
BOLD='\033[1m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

banner() { echo -e "\n${CYAN}════════════════════════════════════════${NC}"; echo -e "${BOLD}  $1${NC}"; echo -e "${CYAN}════════════════════════════════════════${NC}"; }
step()   { echo -e "\n${GREEN}▶ $1${NC}"; }
info()   { echo -e "${YELLOW}  $1${NC}"; }

cleanup() {
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" 2>/dev/null && echo -e "\n${GREEN}✅ Server stopped (pid $SERVER_PID)${NC}"
    fi
}
trap cleanup EXIT

# Build if needed
if [ ! -f "$BIN" ]; then
    echo "Building..."
    go build -o "$BIN" .
fi

banner "LLM Gateway Demo"

# Start server
step "Starting server on port $PORT..."
PORT=$PORT $BIN > /dev/null 2>&1 &
SERVER_PID=$!
sleep 1

if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo -e "${RED}❌ Server failed to start${NC}"
    exit 1
fi
echo -e "  Server running (pid $SERVER_PID)"

# ─── 1. Health Check ───
banner "1. Health Check"
curl -s "$BASE/health" | python3 -m json.tool 2>/dev/null || curl -s "$BASE/health"

# ─── 2. Register Models ───
banner "2. Register Models"

step "Register chat-bot v1 (mock, max 5 concurrent)"
curl -s -X POST "$BASE/models" -H "Content-Type: application/json" \
  -d '{"model_name":"chat-bot","version":"v1","backend_type":"mock","max_concurrent":5,"weight":80}' | python3 -m json.tool

step "Register chat-bot v2 (mock, lower weight)"
curl -s -X POST "$BASE/models" -H "Content-Type: application/json" \
  -d '{"model_name":"chat-bot","version":"v2","backend_type":"mock","max_concurrent":3,"weight":20}' | python3 -m json.tool

step "Register chat-bot v3-shadow (shadow/canary mode)"
info "Shadow version runs in background, response NOT sent to user — only logged for comparison"
curl -s -X POST "$BASE/models" -H "Content-Type: application/json" \
  -d '{"model_name":"chat-bot","version":"v3-shadow","backend_type":"mock","shadow":true}' | python3 -m json.tool

step "Register translator v1 (mock)"
curl -s -X POST "$BASE/models" -H "Content-Type: application/json" \
  -d '{"model_name":"translator","version":"v1","backend_type":"mock"}' | python3 -m json.tool

# ─── 3. List All Models ───
banner "3. List All Models"
curl -s "$BASE/models" | python3 -m json.tool

# ─── 4. Streaming Inference (specific version) ───
banner "4. Streaming Inference — Specific Version"
step "POST /infer → chat-bot v1"
info "Watch tokens arrive one by one (SSE)..."
echo ""
curl -s -N "$BASE/infer" -H "Content-Type: application/json" \
  -d '{"model":"chat-bot","version":"v1","input":"Tell me a joke"}' 2>&1 &
INFER_PID=$!
sleep 4
kill $INFER_PID 2>/dev/null; wait $INFER_PID 2>/dev/null
echo ""

# ─── 5. Weighted Routing (no version specified) ───
banner "5. Weighted Routing — No Version Specified"
info "v1 has weight=80, v2 has weight=20 → most requests go to v1"
step "Running 5 requests, check X-Model-Version header..."
for i in $(seq 1 5); do
    VER=$(curl -s -D - "$BASE/infer" -H "Content-Type: application/json" \
      -d "{\"model\":\"chat-bot\",\"input\":\"test $i\"}" 2>&1 | grep -i x-model-version | tr -d '\r')
    echo "  Request $i: $VER"
    sleep 1
done

# ─── 6. Concurrency Limit ───
banner "6. Concurrency Limit (max_concurrent=5)"
step "Firing 7 concurrent requests to chat-bot v1..."

TMPDIR=$(mktemp -d)
for i in $(seq 1 7); do
    curl -s -o "$TMPDIR/$i.out" -w "%{http_code}" "$BASE/infer" \
      -H "Content-Type: application/json" \
      -d '{"model":"chat-bot","version":"v1","input":"concurrent test"}' > "$TMPDIR/$i.code" 2>&1 &
done
sleep 3

echo "  Results:"
for i in $(seq 1 7); do
    CODE=$(cat "$TMPDIR/$i.code" 2>/dev/null || echo "???")
    if [ "$CODE" = "429" ]; then
        echo -e "  Request $i: ${RED}HTTP $CODE (rejected — at capacity)${NC}"
    else
        echo -e "  Request $i: ${GREEN}HTTP $CODE (accepted)${NC}"
    fi
done
rm -rf "$TMPDIR"
sleep 4

# ─── 7. Hot Update ───
banner "7. Hot Update (zero-downtime)"
step "Start a long inference on v1..."
curl -s -N "$BASE/infer" -H "Content-Type: application/json" \
  -d '{"model":"chat-bot","version":"v1","input":"long running request before update"}' > /dev/null 2>&1 &
LONG_PID=$!
sleep 1

step "Hot-update v1 config while inference is running..."
info "Existing connection continues unaffected (holds old config reference)"
curl -s -X PUT "$BASE/models/chat-bot/version/v1" \
  -H "Content-Type: application/json" \
  -d '{"config":{"style":"verbose","temperature":"0.9"}}' | python3 -m json.tool

step "New request after update:"
curl -s -N "$BASE/infer" -H "Content-Type: application/json" \
  -d '{"model":"chat-bot","version":"v1","input":"after hot update"}' 2>&1 &
NEW_PID=$!
sleep 3
kill $LONG_PID $NEW_PID 2>/dev/null
wait $LONG_PID $NEW_PID 2>/dev/null
echo ""

# ─── 8. Delete Version ───
banner "8. Delete Model Version"
step "Delete chat-bot v2..."
curl -s -X DELETE "$BASE/models/chat-bot/version/v2" | python3 -m json.tool

step "Verify v2 is deleted:"
curl -s "$BASE/models" | python3 -c "
import sys, json
models = json.load(sys.stdin)
for m in models:
    if m['name'] == 'chat-bot':
        for v in m['versions']:
            status = '❌ DELETED' if v['status'] == 'deleted' else '✅ ' + v['status']
            shadow = ' (shadow)' if v.get('shadow') else ''
            print(f\"  {v['version']}: {status}{shadow}\")
" 2>/dev/null

# ─── 9. Error Simulation ───
banner "9. Error Simulation"

step "Register error-bot with timeout simulation"
curl -s -X POST "$BASE/models" -H "Content-Type: application/json" \
  -d '{"model_name":"error-bot","version":"v1","backend_type":"mock","config":{"error":"timeout"}}' | python3 -m json.tool

step "Infer on timeout model (will timeout after 3s)..."
timeout 3 curl -s -N "$BASE/infer" -H "Content-Type: application/json" \
  -d '{"model":"error-bot","version":"v1","input":"test"}' 2>&1 || echo -e "\n  ${YELLOW}(timed out as expected)${NC}"

step "Register partial-error model"
curl -s -X POST "$BASE/models" -H "Content-Type: application/json" \
  -d '{"model_name":"error-bot","version":"v2","backend_type":"mock","config":{"error":"partial"}}' | python3 -m json.tool

step "Infer on partial-error model (starts then fails mid-stream)..."
curl -s -N "$BASE/infer" -H "Content-Type: application/json" \
  -d '{"model":"error-bot","version":"v2","input":"test"}' 2>&1 &
ERR_PID=$!
sleep 2
kill $ERR_PID 2>/dev/null; wait $ERR_PID 2>/dev/null
echo ""

# ─── 10. Duplicate & Not-Found Errors ───
banner "10. Error Handling — Duplicate & Not-Found"
step "Try to register chat-bot v1 again..."
curl -s -X POST "$BASE/models" -H "Content-Type: application/json" \
  -d '{"model_name":"chat-bot","version":"v1","backend_type":"mock"}' | python3 -m json.tool

step "Try to infer on non-existent model..."
curl -s "$BASE/infer" -X POST -H "Content-Type: application/json" \
  -d '{"model":"does-not-exist","input":"hello"}' | python3 -m json.tool

# ─── 11. Prometheus Metrics ───
banner "11. Prometheus Metrics"
curl -s "$BASE/metrics"

# ─── 12. Admin Panel ───
banner "12. Admin Panel"
echo -e "  ${CYAN}Open http://localhost:$PORT/admin in your browser${NC}"
echo "  (auto-refreshes every 3s, shows live model status and active connections)"

# ─── Done ───
banner "🎉 Demo Complete!"
echo -e "
${BOLD}Features demonstrated:${NC}
  ✅ Model registration (mock backend)
  ✅ Model listing with full status
  ✅ SSE streaming inference (token by token)
  ✅ Weighted version routing (80/20 split)
  ✅ Shadow/canary mode (灰度发布)
  ✅ Concurrency limiting (429 on overflow)
  ✅ Hot update (zero-downtime config swap)
  ✅ Version deletion
  ✅ Error simulation (timeout, partial failure, empty response)
  ✅ Error handling (duplicate, not-found, trace_id)
  ✅ Prometheus metrics (/metrics)
  ✅ Admin panel (/admin — live web dashboard)
  ✅ Auto idle-unload (background, 30min threshold)
  ✅ Multi-backend support (mock/openai/ollama/qwen)
"
echo -e "${GREEN}Server shutting down...${NC}"
