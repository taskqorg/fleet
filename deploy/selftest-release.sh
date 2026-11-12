#!/bin/sh
# Checks an extracted release tarball: every binary is static (CGO_ENABLED=0, when go is on PATH to read its build
# info) and passes its selftest with an empty environment, outside the tree.
#   deploy/selftest-release.sh <extracted release dir>
set -eu
dir=$(cd "$1" && pwd)
for b in fleet-coordinator fleet-gateway fleet-executor fleet-update; do
  if command -v go >/dev/null; then go version -m "$dir/$b" | grep -q 'CGO_ENABLED=0' || { echo "$b: cgo enabled" >&2; exit 1; }; fi
  (cd /tmp && env -i "$dir/$b" selftest)
done
