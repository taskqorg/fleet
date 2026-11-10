#!/bin/sh
# Outside tests of the Go coordinator: tests/trial and tests/go against go/bin/fleet-coordinator on a fresh local Postgres
# database (tests/trial/run.ts). Build first: cd go && go generate ./... && go build -o bin/fleet-coordinator ./coordinator
set -e
cd "$(dirname "$0")/../.."
secrets=$(mktemp -d)
trap 'rm -rf "$secrets"' EXIT
# One run per file: each file registers its own hosts, so each gets a fresh database.
for f in tests/trial/*.test.ts tests/go/*.test.ts; do
  FLEET_TRIAL_SERVER=go/bin/fleet-coordinator FLEET_STORE_SECRETS="$secrets" FLEET_STORE_ENDPOINT_HOSTS=127.0.0.1 \
  FLEET_DEFAULT_ARTIFACT_LOCATIONS=trial-host FLEET_TRIAL_TESTS="$f" node tests/trial/run.ts 2>&1 | tee /dev/stderr | grep -q "result=pass" # run.ts exits 0 on failure
done
