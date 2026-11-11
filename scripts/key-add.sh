#!/usr/bin/env bash
# Adds one provider key to a Fleet connection (docs/designs/fleet-asset-pipelines.md §1). The value is read from stdin
# only, never from an argument, and never printed; the reply carries the fingerprint only.
# usage: security find-generic-password -s <item> -w | scripts/key-add.sh <connection>
# Runs as site-agents with ~/.config/ufobe-agents/site-access.env (SITE_ACCESS), against FLEET_URL.
set -euo pipefail
[ $# -eq 1 ] || { echo "usage: <value on stdin> | $0 <connection>" >&2; exit 2; }
[ ! -t 0 ] || { echo "the key value is read from stdin only" >&2; exit 2; }
. "${SITE_ACCESS:-$HOME/.config/ufobe-agents/site-access.env}"
url=${FLEET_URL:-https://fleet.ufobe.com}
# jq reads the value from stdin into the body, so it is never on a command line; empty stdin fails here.
body=$(jq -Rn --arg c "$1" '{connection: $c, value: (input | gsub("\\s"; ""))} | if .value == "" then error("empty value") else . end')
reply=$(printf '%s' "$body" | curl -sS -w '\n%{http_code}' -H "Origin: $url" -H 'content-type: application/json' \
    -H "CF-Access-Client-Id: $CF_ACCESS_CLIENT_ID" -H "CF-Access-Client-Secret: $CF_ACCESS_CLIENT_SECRET" \
    --data-binary @- "$url/api/ops/connections_key_add")
unset body
status=${reply##*$'\n'}
if [ "$status" != 200 ]; then echo "connections_key_add HTTP $status: ${reply%$'\n'*}" >&2; exit 1; fi
printf '%s' "${reply%$'\n'*}" | jq -ce '{fingerprint, state, connectionId} | select(.fingerprint != null)'
