#!/bin/sh
# Fleet nightly backup (design Phase 0 item 4). Runs as user fleet from fleet-backup.timer.
# ClickHouse BACKUP and a Garage bucket mirror into /backup/fleet/YYYY-MM-DD/, keep 5 days (tight D6).
# No service is stopped. Postgres is covered by the existing nightly pg_dump.
# Host-only env files (0600, never committed):
#   /home/fleet/.config/fleet/clickhouse.env      CLICKHOUSE_USER, CLICKHOUSE_PASSWORD
#   /home/fleet/.config/fleet/backup-garage.env   RCLONE_CONFIG_FLEETSTORE_* for a read-only Garage key
set -eu
umask 077
root=/backup/fleet
day=$(date -u +%F)
dest="$root/$day"
mkdir -p "$dest/garage"

set -a
# shellcheck source=/dev/null
. /home/fleet/.config/fleet/clickhouse.env
# shellcheck source=/dev/null
. /home/fleet/.config/fleet/backup-garage.env
set +a

# ClickHouse writes to /backups/<day>/clickhouse inside its container (host $dest/clickhouse).
# A second run on the same day fails here on purpose: BACKUP refuses an existing target.
# Credentials go in headers read from stdin, never in argv.
printf 'X-ClickHouse-User: %s\nX-ClickHouse-Key: %s\n' "$CLICKHOUSE_USER" "$CLICKHOUSE_PASSWORD" |
curl -fsS -H @- http://127.0.0.1:9480/ \
  --data-binary "BACKUP DATABASE fleet TO File('$day/clickhouse')"

# Garage: copy the fleet bucket over loopback S3. Objects are immutable and hash-keyed.
rclone sync fleetstore:fleet "$dest/garage/fleet" --checksum

# Keep the 5 newest day directories.
# shellcheck disable=SC2012 # names are fixed YYYY-MM-DD
ls -1d "$root"/????-??-?? | sort -r | tail -n +6 | while read -r old; do rm -rf -- "$old"; done
