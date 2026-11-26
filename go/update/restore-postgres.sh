#!/bin/bash
# Restore a Fleet custom archive without using the live PostgreSQL cluster.
set -euo pipefail
umask 077
archive=$1
pg=$(pg_config --bindir)
scratch=$(mktemp -d -- /var/tmp/fleet-pg-restore.XXXXXX)
cleanup() {
    if "$pg/pg_ctl" -D "$scratch/data" status >/dev/null 2>&1; then
        "$pg/pg_ctl" -D "$scratch/data" -m immediate -w stop >/dev/null || return
    fi
    rm -rf -- "$scratch"
}
trap cleanup EXIT

"$pg/initdb" -D "$scratch/data" --auth=peer --no-instructions >/dev/null
# No TCP listener. Port 5432 is used only in the private socket's filename.
"$pg/pg_ctl" -D "$scratch/data" -l "$scratch/postgres.log" \
    -o "-c listen_addresses='' -c unix_socket_directories='$scratch' -p 5432" -w start >/dev/null
"$pg/createdb" -w -h "$scratch" -p 5432 fleet_restore
"$pg/pg_restore" -w -h "$scratch" -p 5432 -d fleet_restore \
    --exit-on-error --no-owner --no-privileges "$archive"
"$pg/psql" -XwAt -h "$scratch" -p 5432 -d fleet_restore -v ON_ERROR_STOP=1 \
    -c 'SELECT count(*) FROM schema_migration; SELECT count(*) FROM task; SELECT count(*) FROM execution;' >/dev/null
cleanup
trap - EXIT
printf 'PostgreSQL restore test passed: %s\n' "${archive##*/}"
