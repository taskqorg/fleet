#!/usr/bin/env bash
# Agent access to the web app (docs/designs/fleet-people-access.md#agent-access-to-the-web-app): with the ufobe-site-agents
# service token, /, /tasks, /lab and /api/ops reads (me, project_list) open, and the coordinator sees person site-agents with owner-equivalent rights.
# The token is sourced in a subshell and goes into a 0600 header file read by curl (-H @file): never printed, never in ps.
# Usage: deploy/infrastructure/agent-access-smoke.sh [https://fleet.ufobe.com]
set -euo pipefail
URL="${1:-https://fleet.ufobe.com}"
HDR="$(mktemp)"; chmod 600 "$HDR"; trap 'rm -f "$HDR"' EXIT
( . "${FLEET_SITE_ACCESS_ENV:-$HOME/.config/ufobe-agents/site-access.env}"
  printf 'CF-Access-Client-Id: %s\nCF-Access-Client-Secret: %s\n' "$CF_ACCESS_CLIENT_ID" "$CF_ACCESS_CLIENT_SECRET" ) >"$HDR"
fail=0
for path in / /tasks /lab /api/accounts; do
  code=$(curl -s -o /dev/null -w '%{http_code}' -H @"$HDR" "$URL$path")
  [[ "$code" == 200 ]] && echo "ok: $path" || { echo "FAIL: $path returned HTTP $code" >&2; fail=1; }
done
op() { curl -s -H @"$HDR" -H 'content-type: application/json' -H "origin: $URL" -d '{}' "$URL/api/ops/$1"; }
op me | grep -q '"email":"site-agents"' && echo "ok: me is site-agents" || { echo "FAIL: me is not site-agents" >&2; fail=1; }
op project_list | grep -q '"id"' && echo "ok: project_list lists projects" || { echo "FAIL: project_list empty" >&2; fail=1; }
exit $fail
