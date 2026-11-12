#!/bin/sh
# Checks the release tarballs deploy/build-release.sh wrote to <out>: every binary of every target is static
# (CGO_ENABLED=0, from its Go build info; a darwin binary is checked on linux too), and the linux binaries pass their
# selftest with an empty environment, outside the tree. Extracts the linux tarball to <new> for the migration check.
#   deploy/verify-release.sh <out> <new>
set -eu
out=$(cd "$1" && pwd)
mkdir "$2" && new=$(cd "$2" && pwd)
tar -xzf "$out"/fleet-*-linux-amd64.tgz -C "$new"
darwin=$(mktemp -d) && trap 'rm -rf "$darwin"' EXIT
tar -xzf "$out"/fleet-*-darwin-arm64.tgz -C "$darwin" --wildcards './fleet-*'
for b in "$new"/fleet-* "$darwin"/fleet-*; do
  info=$(go version -m "$b") || { echo "$b: no Go build info" >&2; exit 1; }
  printf '%s\n' "$info" | grep -q 'CGO_ENABLED=0' || { echo "$b: cgo enabled" >&2; exit 1; }
done
for b in fleet-coordinator fleet-gateway fleet-executor fleet-update; do (cd /tmp && env -i "$new/$b" selftest); done
