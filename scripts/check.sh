#!/usr/bin/env bash
# Every check a change must pass, the same in CI (ci.yml jobs go and app) and locally
# (docs/designs/fleet-delivery-pipeline.md#checks-per-change). Run from anywhere:
#   scripts/check.sh go    generated code unchanged, vet, the four binaries, go test (with the schema drift checks on
#                          the test PostgreSQL), the smoke on the real coordinator
#   scripts/check.sh app   pnpm install, typecheck and bundle, the app unit tests
#   scripts/check.sh       both
# The smoke (tests/trial/run.ts) needs the PostgreSQL server programs; on Ubuntu they are found under /usr/lib/postgresql.
set -eo pipefail
cd "$(dirname "$0")/.."
pg=$(ls -d /usr/lib/postgresql/*/bin 2>/dev/null | tail -1 || true)
[ -z "$pg" ] || PATH=$pg:$PATH
step() { local t=$SECONDS; "$@"; echo "check: $* ($((SECONDS - t)) s)"; }
go_checks() {
  # Generated code is committed (go/contract): regenerating must not change the tree (local edits may be in it).
  local tree; tree() { { git status --porcelain -- go contracts; git diff -- go contracts; } | git hash-object --stdin; }
  local before; before=$(tree)
  step bash -c 'cd go && go generate ./...'
  [ "$(tree)" = "$before" ] || { echo "check: go generate changed the tree"; git status --porcelain -- go contracts; exit 1; }
  step bash -c 'cd go && go vet ./... && GOOS=windows GOARCH=amd64 go vet ./coordinator'
  step bash -c 'cd go && for c in coordinator executor gateway update; do CGO_ENABLED=0 go build -o bin/fleet-$c ./$c || exit 1; done'
  # FLEET_REQUIRE_PG: the schema drift checks (TestMigrationSqlcSchema, TestMigrationSchemaFiles) never skip unnoticed.
  step bash -c 'cd go && FLEET_REQUIRE_PG=1 go test ./...'
  step node tests/trial/run.ts
}
app_checks() {
  step pnpm install --frozen-lockfile
  step pnpm run app:build
  step node --test tests/ui/*.test.ts
}
case ${1:-all} in
  go) go_checks ;;
  app) app_checks ;;
  all) go_checks; app_checks ;;
  *) echo "usage: scripts/check.sh [go|app]" >&2; exit 64 ;;
esac
