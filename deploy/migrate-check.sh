#!/usr/bin/env bash
# Migration check (docs/designs/fleet-delivery-pipeline.md#5-compatibility-across-skipped-versions): upgrade, then expand
# and contract. The new coordinator migrates a database the latest release built, and each of the last 5 releases'
# coordinators must still serve on the database the new one migrated, so a rollback on PDS18 from up to 5 releases behind
# returns to a checked pair. Run from a checkout of the new commit:
#   VERSION=<new> RECENT="<newest> ..." DIGESTS="<v>=sha256:<hex> ..." GH_TOKEN=... GH_REPO=... \
#     deploy/migrate-check.sh <extracted new release> <dir with its tarball and SHA256SUMS>
# The recent coordinators stay extracted under ~/fleet-releases/v between runs (one download per new release; a hosted
# runner starts empty) and the throwaway PostgreSQL skips fsync.
set -eu
new=$(cd "$1" && pwd) out=$(cd "$2" && pwd)
set -o pipefail
export PATH="$(ls -d /usr/lib/postgresql/*/bin | tail -1):$PATH" pg=$RUNNER_TEMP/pg
# The runner is persistent: a failed step must not leave a coordinator on the port or a PostgreSQL behind, and a
# cancelled one (killed before the trap) is cleared here.
stale() { "$@" -u "$(id -u)" -f '(^|/)fleet-coordinator$|postgres .*-p 54329'; }
stale pkill || true
i=0; while stale pgrep >/dev/null && [ $i -lt 10 ]; do i=$((i+1)); sleep 1; done
stale pkill -KILL || true
# A killed postmaster leaves its dynamic shared memory; logind no longer removes it (RemoveIPC=no).
find /dev/shm -maxdepth 1 -user "$(id -u)" -name 'PostgreSQL.*' -delete 2>/dev/null || true
stop() { kill "$1" || return 0; for _ in 1 2 3 4 5; do kill -0 "$1" || return 0; sleep 1; done; kill -KILL "$1"; wait "$1"; }
pid=; trap '{ [ -z "$pid" ] || stop $pid; pg_ctl -D "$pg" -m immediate -w stop; } >/dev/null 2>&1 || true' EXIT
initdb -D "$pg" -U postgres -A trust -E UTF8 --no-instructions --no-sync >/dev/null
pg_ctl -D "$pg" -l "$pg/log" -w -o "-k $pg -p 54329 -c listen_addresses= -c fsync=off -c synchronous_commit=off -c full_page_writes=off" start
psql -h "$pg" -p 54329 -U postgres -v ON_ERROR_STOP=1 -q -d postgres -f deploy/infrastructure/fleet-postgres.sql
# Each entry holds the release's fleet-coordinator, config/ and release.json and the sha256 digest of the asset it
# came from; a replaced asset (new digest) is fetched again, and the download is checked against that digest.
# An asset GitHub reports no digest for is fetched every run, unchecked, as before the cache.
# One extra level: a coordinator's repoRoot() probes two directories above its binary for config/, never $HOME.
cache=${HOME:?}/fleet-releases/v; mkdir -p "$cache"
for d in "$cache"/*/; do d=${d%/}; [ -d "$d" ] || continue; case " $RECENT " in *" ${d##*/} "*) rm -rf "${d:?}/var";; *) rm -rf "$d";; esac; done
members="./fleet-coordinator ./config ./release.json"
for v in $RECENT; do
  file=fleet-$v-linux-amd64.tgz
  digest=$(printf '%s\n' $DIGESTS | awk -F= -v v="$v" '$1 == v { print $2 }')
  [ -n "$digest" ] && [ -x "$cache/$v/fleet-coordinator" ] && [ "$(cat "$cache/$v/.digest" 2>/dev/null)" = "$digest $members" ] && continue
  rm -rf "$cache/$v.tmp"; mkdir "$cache/$v.tmp"
  gh release download "v$v" -p "$file" -D "$RUNNER_TEMP" --clobber || { echo "::error::v$v: download failed"; exit 1; }
  [ -z "$digest" ] || echo "${digest#sha256:}  $RUNNER_TEMP/$file" | sha256sum -c --quiet || { echo "::error::v$v: $file does not match its digest"; exit 1; }
  tar -xzf "$RUNNER_TEMP/$file" -C "$cache/$v.tmp" $members || { echo "::error::v$v: extraction failed"; exit 1; }
  rm -f "$RUNNER_TEMP/$file"; echo "$digest $members" > "$cache/$v.tmp/.digest"
  rm -rf "${cache:?}/$v"; mv "$cache/$v.tmp" "$cache/$v"
done
url() { echo "postgresql://$1@/fleet?host=$pg&port=54329"; }
export FLEET_DATABASE_URL=$(url fleet_app) FLEET_MIGRATION_DATABASE_URL=$(url fleet) \
  FLEET_PEOPLE_FILE=$PWD/tests/trial/people.json FLEET_BROWSER_SECRET=migrate-check FLEET_HOST_ID=pds18
# exec: kill stops the coordinator itself; a leftover one on the port shows as the fresh process exiting.
# The latest release builds the database first, as PDS18's is: the new coordinator then applies only its new
# migrations on top (a fresh database alone hides a migration that depends on an edited released one).
for v in "${RECENT%% *}" "$VERSION" $RECENT; do
  dir=$cache/$v; [ "$v" = "$VERSION" ] && dir=$new
  # A free port per start: the PDS18 pool runs jobs side by side on one guest.
  port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
  export PORT=$port
  (cd "$dir" && FLEET_PROJECT_CONFIG_DIRECTORY=$dir/config FLEET_PROFILES_DIRECTORY=$dir/config/agents FLEET_LAB_DIRECTORY=$dir/config/lab exec ./fleet-coordinator) & pid=$!
  t=$SECONDS; until kill -0 $pid 2>/dev/null && curl -fsS -m 3 -o "$RUNNER_TEMP/health" http://127.0.0.1:$port/api/health && grep -q "\"version\":\"$v\"" "$RUNNER_TEMP/health"; do
    kill -0 $pid 2>/dev/null || { echo "::error::$dir coordinator exited"; exit 1; }
    [ $((SECONDS - t)) -lt 120 ] || { echo "::error::$dir coordinator not healthy after 120 s"; exit 1; }; sleep 1; done
  echo; kill -0 $pid || { echo "::error::$dir coordinator exited after health"; exit 1; }; stop $pid || true; pid=
done
# Seed the cache with this release (GitHub's asset digest is the sha256 of the uploaded bytes), so the next run
# downloads nothing; an entry for a version that never publishes is pruned by the sweep above. Best effort.
file=fleet-$VERSION-linux-amd64.tgz sum=$(awk -v f="$file" '$2 == f { print $1 }' "$out/SHA256SUMS")
[ -n "$sum" ] && rm -rf "$cache/$VERSION.tmp" && mkdir "$cache/$VERSION.tmp" \
  && tar -xzf "$out/$file" -C "$cache/$VERSION.tmp" $members && echo "sha256:$sum $members" > "$cache/$VERSION.tmp/.digest" \
  && rm -rf "${cache:?}/$VERSION" && mv "$cache/$VERSION.tmp" "$cache/$VERSION" || echo "::warning::cache seed of $VERSION skipped"
