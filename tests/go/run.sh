#!/bin/sh
# Outside tests of the Go coordinator: tests/trial and tests/go against go/bin/fleet-coordinator on a fresh local Postgres
# database (tests/trial/run.ts). Build first: pnpm run go:build
# run.ts exits non-zero when a test fails or the coordinator does not start; set -e stops at the first failing file.
#   tests/go/run.sh [index total]   run only every total-th file starting at index (CI shards; default all files).
set -e
cd "$(dirname "$0")/../.."
index=${1:-0} total=${2:-1}
[ "$index" -ge 0 ] && [ "$index" -lt "$total" ] || { echo "shard index $index outside total $total"; exit 1; }
secrets=$(mktemp -d)
trap 'rm -rf "$secrets"' EXIT
# One run per file: each file registers its own hosts, so each gets a fresh database.
# ponytail: round-robin sharding; weight by measured duration if one shard stays much longer than the others.
i=0 ran=0
for f in tests/trial/*.test.ts tests/go/*.test.ts; do
  n=$i; i=$((i + 1))
  [ $((n % total)) -eq "$index" ] || continue
  ran=$((ran + 1))
  FLEET_TRIAL_SERVER=go/bin/fleet-coordinator FLEET_STORE_SECRETS="$secrets" FLEET_STORE_ENDPOINT_HOSTS=127.0.0.1 \
  FLEET_DEFAULT_ARTIFACT_LOCATIONS=trial-host \
  FLEET_SECRET_DIR="$secrets/secret" FLEET_GATEWAY_DIR="$secrets/gateway" FLEET_GATEWAY_URL=http://127.0.0.1:47811 FLEET_CONNECTION_TICK_MS=200 FLEET_PIPELINE_TICK_MS=200 FLEET_STREAM_PING_MS=300 FLEET_WATCH_TICK_MS=200 FLEET_LOST_AFTER_MS=6000 \
  FLEET_TRIAL_TESTS="$f" node tests/trial/run.ts
done
[ "$ran" -gt 0 ] || { echo "shard $index/$total ran no files"; exit 1; }
echo "shard $index/$total ran $ran files"
