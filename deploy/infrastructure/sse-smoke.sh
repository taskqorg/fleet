#!/usr/bin/env bash
# Manual post-deploy check of the SSE chain (docs/designs/fleet-operator-app.md §9, item 7): browser -> Cloudflare Access ->
# origin_guard -> coordinator. Passes when /api/stream sends a ping within 30 s and an event within 2 s of a task_update.
# Auth: the owner's own Access session through `cloudflared access login` (the guard accepts only the owner's email, so a
# service token does not pass). The token is passed in a header and never printed.
# Usage: FLEET_SMOKE_TASK_ID=<test task id> deploy/infrastructure/sse-smoke.sh [https://fleet.ufobe.com]
set -euo pipefail
URL="${1:-https://fleet.ufobe.com}"; URL="$(printf %s "$URL" | sed -E 's#^(https?://[^/]+).*#\1#')" # origin only: the guard compares Origin exactly
TASK="${FLEET_SMOKE_TASK_ID:?set FLEET_SMOKE_TASK_ID to a test task id}"
TOKEN="$(cloudflared access token -app="$URL")" || { echo "run: cloudflared access login $URL" >&2; exit 2; }
OUT="$(mktemp)"; trap 'kill "${PID:-}" 2>/dev/null || true; rm -f "$OUT"' EXIT
curl -sN --max-time 40 -H "cf-access-token: $TOKEN" "$URL/api/stream" >"$OUT" &
PID=$!

wait_for() { # pattern, seconds
  local end=$((SECONDS + $2)); while ((SECONDS < end)); do grep -q "$1" "$OUT" && return 0; sleep 0.2; done; return 1; }

wait_for '^event: ready' 10 || { echo "FAIL: no ready event within 10 s" >&2; exit 1; }
before=$(grep -c '^id: ' "$OUT" || true)
code=$(curl -s -o /dev/null -w '%{http_code}' -H "cf-access-token: $TOKEN" -H 'content-type: application/json' -H "origin: $URL" \
  -d "{\"taskId\":\"$TASK\",\"title\":\"sse smoke $(date +%s)\"}" "$URL/api/ops/task_update")
[[ "$code" == 200 ]] || { echo "FAIL: task_update returned HTTP $code" >&2; exit 1; }
end=$((SECONDS + 2)); got=0
while ((SECONDS < end)); do (( $(grep -c '^id: ' "$OUT" || true) > before )) && { got=1; break; }; sleep 0.1; done
((got)) || { echo "FAIL: no event within 2 s of task_update" >&2; exit 1; }
echo "ok: event within 2 s"
wait_for '^: ping' 30 || { echo "FAIL: no ping within 30 s" >&2; exit 1; }
echo "ok: ping within 30 s"
