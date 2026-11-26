#!/bin/bash
# Invoked by the root-owned Fleet updater. Access the backup tree only as fleet.
set -euo pipefail

root=$1
restore_script=$2
as_fleet() { runuser -u fleet -- "$@"; }

day=$(date -u +%Y%m%d)
name=fleet-$day.dump
as_fleet mkdir -m 0700 -p -- "$root/daily" "$root/weekly"
tmp=$(as_fleet mktemp -- "$root/daily/.$name.XXXXXX")
trap 'as_fleet rm -f -- "$tmp"' EXIT

# pg_dump runs with the local PostgreSQL identity that can read FORCE RLS tables.
# pipefail prevents an interrupted or failed dump from replacing a completed one.
runuser -u postgres -- pg_dump --no-password -Fc -d fleet |
    as_fleet dd "of=$tmp" conv=fsync status=none
as_fleet test -s "$tmp"
as_fleet mv -fT -- "$tmp" "$root/daily/$name"
printf 'Nightly PostgreSQL dump written: daily/%s\n' "$name"

if [ "$(date -u -d "$day" +%u)" = 7 ]; then
    as_fleet ln -fT -- "$root/daily/$name" "$root/weekly/$name"
fi
prune() {
    as_fleet /bin/bash -euo pipefail -c '
        cd "$1"
        find . -maxdepth 1 -regextype posix-extended -type f -regex "./fleet-[0-9]{8}\.dump" -printf "%f\n" |
            LC_ALL=C sort -r | tail -n "+$2" | xargs -r rm --' _ "$root/$1" "$2"
}
prune daily 8
prune weekly 5

if ! as_fleet test -s "$root/.restore-tested"; then
    as_fleet /bin/bash -c "$restore_script" _ "$root/daily/$name"
    as_fleet /bin/bash -es -- "$root" "$name" <<'MARKER'
umask 077
marker=$(mktemp -- "$1/.restore-tested.XXXXXX")
trap 'rm -f -- "$marker"' EXIT
printf '%s\n' "$2" > "$marker"
mv -fT -- "$marker" "$1/.restore-tested"
MARKER
fi
