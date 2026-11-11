#!/bin/sh
# Adds one provider key to a Fleet connection (docs/designs/fleet-asset-pipelines.md §1). The value is read from stdin
# only, never from an argument, and never printed; the reply carries the fingerprint only.
# usage: security find-generic-password -s <item> -w | scripts/key-add.sh <connection>
# Runs as site-agents with ~/.config/ufobe-agents/site-access.env, or with FLEET_URL and the headers that file sets.
set -eu
[ $# -eq 1 ] || { echo "usage: <value on stdin> | $0 <connection>" >&2; exit 2; }
[ ! -t 0 ] || { echo "the key value is read from stdin only" >&2; exit 2; }
. "${SITE_ACCESS:-$HOME/.config/ufobe-agents/site-access.env}"
url=${FLEET_URL:-https://fleet.ufobe.com}
# jq reads the value from stdin into the body, so it is never on a command line.
jq -Rn --arg c "$1" '{connection: $c, value: (input | gsub("\\s"; ""))}' |
    curl -sS --fail-with-body -H "Origin: $url" -H 'content-type: application/json' \
        -H "CF-Access-Client-Id: $CF_ACCESS_CLIENT_ID" -H "CF-Access-Client-Secret: $CF_ACCESS_CLIENT_SECRET" \
        --data-binary @- "$url/api/ops/connections_key_add" | jq -c '{fingerprint, state, connectionId}'
