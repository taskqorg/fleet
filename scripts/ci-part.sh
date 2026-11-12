#!/usr/bin/env bash
# One CI part's compile and test commands, shared by its ci.yml job and by cache-warm (which runs only `build` to fill the
# part's Go build cache from main), so the cached artifacts always match what the part compiles.
#   scripts/ci-part.sh <vet|unit|outside-N|browser|release> <build|test>
#   scripts/ci-part.sh clickhouse <start|wait>   ClickHouse for go/obs and telemetry tests, started in the background
# BIN: where binaries go (default go/bin; cache-warm puts them outside go/, whose hash keys the caches).
# OUTSIDE_SHARDS: total outside shards (default 1). VERSION: release version (default 0.0.0).
set -euo pipefail
part=${1:?part} mode=${2:?mode}
root=$(cd "$(dirname "$0")/.." && pwd) self=$root/scripts/ci-part.sh
# Absolute (BIN as the caller gave it): go builds run in go/, the checks read it from the repository root.
[ "$part" = clickhouse ] || bin=$(b=${BIN:-$root/go/bin}; mkdir -p "$b" && cd "$b" && pwd)
cd "$root"
case "$part:$mode" in
  clickhouse:start)
    # Same LTS as deploy/infrastructure/fleet-clickhouse.container. In the background: docker run pulls before it
    # detaches, and the pull should overlap the job's setup and build.
    nohup docker run -d --name clickhouse -p 127.0.0.1:8123:8123 -e CLICKHOUSE_DB=fleet -e CLICKHOUSE_USER=t \
      -e CLICKHOUSE_PASSWORD=t clickhouse/clickhouse-server:26.8 > "${RUNNER_TEMP:-/tmp}/clickhouse-start.log" 2>&1 & ;;
  clickhouse:wait)
    for _ in $(seq 120); do
      curl -fsS -u t:t 'http://127.0.0.1:8123/?query=SELECT%201' >/dev/null 2>&1 && exit 0
      # Pulled and started, then exited: fail now, not after the whole wait.
      [ "$(docker inspect -f '{{.State.Status}}' clickhouse 2>/dev/null)" = exited ] && break
      sleep 1
    done
    cat "${RUNNER_TEMP:-/tmp}/clickhouse-start.log" 2>/dev/null; docker logs clickhouse 2>&1 | tail -20; echo "ClickHouse not ready"; exit 1 ;;
  vet:build)
    (cd go && go vet ./... && GOOS=windows GOARCH=amd64 go vet ./coordinator && CGO_ENABLED=0 go build -o "$bin/fleet-executor" ./executor) ;;
  vet:test)
    # Generated code is committed (go/contract): regenerating must not change it.
    (cd go && go generate ./...) && { test -z "$(git status --porcelain)" || { git status --porcelain; git diff; exit 1; }; }
    "$self" vet build
    # The executor's MCP adapter from Node against the Go binary: replayed stdio exchanges and artifact path confinement.
    FLEET_EXECUTOR_BIN="$bin/fleet-executor" node --test tests/executor/mcp-replay.test.ts tests/executor/artifact-tools.test.ts ;;
  # -race needs cgo on Linux; release binaries stay CGO_ENABLED=0 (deploy/build-release.sh).
  unit:build) (cd go && CGO_ENABLED=1 go test -race -count=1 -run '^$' ./...) ;;
  unit:test) (cd go && CGO_ENABLED=1 go test -race -count=1 ./...) ;;
  outside-*:build) (cd go && CGO_ENABLED=1 go build -race -o "$bin/fleet-coordinator" ./coordinator) ;;
  outside-*:test)
    "$self" "$part" build
    # /api/health reports vcs.modified as dirty; a build from the CI checkout must be clean (untracked files count).
    go version -m "$bin/fleet-coordinator" | grep -q 'vcs.modified=false' || { git status --porcelain; exit 1; }
    # The outside tests run a -race coordinator; GORACE halts it at the first race, so the test of that moment fails.
    PATH="$(ls -d /usr/lib/postgresql/*/bin | tail -1):$PATH" GORACE=halt_on_error=1 tests/go/run.sh "${part#outside-}" "${OUTSIDE_SHARDS:-1}" ;;
  browser:build) pnpm run app:bundle && (cd go && CGO_ENABLED=0 go build -trimpath -o "$bin/fleet-coordinator" ./coordinator) ;;
  release:build)
    rm -rf "$bin/release" && mkdir -p "$bin/release"
    deploy/build-release.sh HEAD "$bin/release" "${VERSION:-0.0.0}" "" linux/amd64 darwin/arm64 ;;
  *) echo "unknown part or mode: $part $mode" >&2; exit 64 ;;
esac
