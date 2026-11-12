#!/bin/sh
# Runs the selftest of the four binaries of an extracted release tarball with an empty environment, outside the tree
# (deploy/static-release.sh checks they are static).
#   deploy/selftest-release.sh <extracted release dir>
set -eu
dir=$(cd "$1" && pwd)
for b in fleet-coordinator fleet-gateway fleet-executor fleet-update; do (cd /tmp && env -i "$dir/$b" selftest); done
