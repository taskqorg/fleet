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
    # A new file renamed into place: root never writes through a link the directory's user may have planted.
    tmp=$(mktemp "$dir/.import.XXXXXX")
    printf '%s\n' "$value" > "$tmp"
    chown "$user:$user" "$tmp"; chmod 0600 "$tmp"; mv -f "$tmp" "$dir/$fp"
    n=$((n + 1)); echo "$connection key $fp"
done < "$keys"
echo "$n keys in $dir"
