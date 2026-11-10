#!/bin/sh
# Moves provider keys from a one-key-per-line file into Fleet's secret directory (docs/designs/fleet-connections.md, C0).
# Run as root on the coordinator host; values are never printed. The coordinator registers the files on its next start.
# usage: connections-import.sh <keys file> <connection> <secret dir> <owner user>
# example: connections-import.sh /etc/pipe-read/tavily-keys tavily /home/fleet/.config/fleet/secrets fleet
set -eu
keys=$1 connection=$2 dir=$3/connections/$2 user=$4
umask 077
install -d -m 0700 -o "$user" -g "$user" "$3" "$3/connections" "$dir"
n=0
while IFS= read -r line || [ -n "$line" ]; do
    value=$(printf '%s' "$line" | tr -d '[:space:]')
    [ -n "$value" ] || continue
    fp=$(printf '%s' "$value" | sha256sum | cut -c1-12)
    # Written as the directory's user, never as root inside a directory that user controls.
    printf '%s\n' "$value" | runuser -u "$user" -- sh -c 'umask 077; t=$(mktemp "$1/.import.XXXXXX") && cat > "$t" && mv -f "$t" "$1/$2"' _ "$dir" "$fp"
    n=$((n + 1)); echo "$connection key $fp"
done < "$keys"
echo "$n keys in $dir"
