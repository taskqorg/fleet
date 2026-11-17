#!/bin/sh
# Outside tests of the Go coordinator: tests/trial and tests/go against go/bin/fleet-coordinator on a fresh local Postgres
# database per file (tests/trial/run.ts). Build first: pnpm run go:build
#   tests/go/run.sh [index total]   run only every total-th file starting at index (CI shards; default all files).
# OUTSIDE_JOBS: files run at once (default 1). Each file gets its own database, secret directories and gateway port; its
# output is printed whole when it ends, with its duration. A failed file starts no new ones; the run fails after the
# running files end, naming the failed files. All files share the one local PostgreSQL server, each in its own database.
set -e
cd "$(dirname "$0")/../.."
index=${1:-0} total=${2:-1}
[ "$index" -ge 0 ] && [ "$index" -lt "$total" ] || { echo "shard index $index outside total $total"; exit 1; }
files=
i=0
for f in tests/trial/*.test.ts tests/go/*.test.ts; do
  n=$i; i=$((i + 1))
  [ $((n % total)) -eq "$index" ] && files="$files $f"
done
[ -n "$files" ] || { echo "shard $index/$total ran no files"; exit 1; }
# shellcheck disable=SC2086
node tests/go/run-files.ts "${OUTSIDE_JOBS:-1}" $files
