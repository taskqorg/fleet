#!/bin/sh
# Checks an extracted release tarball: every fleet-* binary is static (CGO_ENABLED=0, read from its Go build info) and
# the four of every release pass their selftest with an empty environment, outside the tree.
#   deploy/selftest-release.sh <extracted release dir>
set -eu
dir=$(cd "$1" && pwd)
"$(dirname "$0")/static-release.sh" "$dir"
for b in fleet-coordinator fleet-gateway fleet-executor fleet-update; do (cd /tmp && env -i "$dir/$b" selftest); done
