#!/bin/sh
# Make a host's own secret on the host itself (docs/designs/fleet-people-access.md §3). The secret goes straight into a 0600
# file and never leaves the host; only its SHA-256 is printed, for hosts_register / hosts_rotate_secret (secretSha256).
# Usage: scripts/host-secret.sh <file>   (refuses to overwrite an existing file)
set -eu
f=${1:?usage: host-secret.sh <file>}
[ -e "$f" ] && { echo "$f exists; move it away first" >&2; exit 1; }
umask 077
set -C  # noclobber: the redirect itself fails if the file appeared meanwhile
openssl rand -hex 32 > "$f"
tr -d '\n' < "$f" | { sha256sum 2>/dev/null || shasum -a 256; } | cut -d' ' -f1
