#!/bin/bash

if [[ -z "$1" ]]; then
  echo "Usage: bash vllm_traffic.sh <hostname>"
  exit 1
fi

HOST="$1"
BASE_URL="http://${HOST}"
SLEEP_BETWEEN=${2:-5}  # optional 2nd arg, default 5s

PROMPTS=(
  "What is a Kubernetes pod?"
  "Explain Helm charts briefly."
  "What is a ClusterIP service?"
  "Describe a Kubernetes namespace?"
  "What is a container image?"
)

# ── Detect model info ────────────────────────────────────────────────────────
echo "🔍 Detecting model..."
MODEL_INFO=$(curl -s --max-time 10 "${BASE_URL}/v1/models")
MODEL_ID=$(echo "$MODEL_INFO" | jq -r '.data[0].id')
MODEL_ROOT=$(echo "$MODEL_INFO" | jq -r '.data[0].root')

echo "   Model ID   : $MODEL_ID"
echo "   Model Root : $MODEL_ROOT"

# ── Detect thinking support from model name ──────────────────────────────────
echo "🧠 Detecting thinking support from model name..."
if echo "$MODEL_ROOT" | grep -qiE '2507|-NI$'; then
  THINKING_SUPPORTED=false
  echo "   ⚠️  Non-thinking variant detected"
elif echo "$MODEL_ROOT" | grep -qiE 'Qwen3|qwen3'; then
  THINKING_SUPPORTED=true
  echo "   ✅ Qwen3 thinking variant detected"
else
  THINKING_SUPPORTED=false
  echo "   ⚠️  Unknown model — defaulting to no thinking params"
fi

# ── Build request body ───────────────────────────────────────────────────────
build_request() {
  local prompt="$1"
  if [[ "$THINKING_SUPPORTED" == "true" ]]; then
    jq -n \
      --arg model "$MODEL_ID" \
      --arg content "$prompt" \
      '{"model": $model, "messages": [{"role": "user", "content": $content}], "max_tokens": 4096, "temperature": 0.7, "chat_template_kwargs": {"enable_thinking": false}}'
  else
    jq -n \
      --arg model "$MODEL_ID" \
      --arg content "$prompt" \
      '{"model": $model, "messages": [{"role": "user", "content": $content}], "max_tokens": 4096, "temperature": 0.7}'
  fi
}

# ── Main loop ────────────────────────────────────────────────────────────────
echo ""
echo "🚀 Starting traffic loop (1000 requests) — ${SLEEP_BETWEEN}s between requests..."
echo "──────────────────────────────────────────"

PASS=0
FAIL=0

for i in $(seq 1 1000); do
  PROMPT=${PROMPTS[$((RANDOM % ${#PROMPTS[@]}))]}
  echo "--- Request $i: $PROMPT ---"

  RESULT=$(curl -s --max-time 240 -X POST "${BASE_URL}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "$(build_request "$PROMPT")" \
    | jq -r '
        if .choices[0].message.content then
          .choices[0].message.content
        else
          "ERROR: \(.error.message // "unknown error")"
        end
      ')

  if [[ "$RESULT" == ERROR:* ]]; then
    echo "❌ $RESULT"
    (( FAIL++ ))
  else
    echo "$RESULT"
    (( PASS++ ))
  fi

  sleep "$SLEEP_BETWEEN"
done

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "──────────────────────────────────────────"
echo "✅ Passed : $PASS"
echo "❌ Failed : $FAIL"
echo "📊 Total  : $((PASS + FAIL))"