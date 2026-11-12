#!/bin/sh
# Runs each binary's selftest from an extracted release tarball, with an empty environment and outside the tree.
#   deploy/selftest-release.sh <extracted release dir>
set -eu
dir=$(cd "$1" && pwd)
for b in fleet-coordinator fleet-gateway fleet-executor fleet-update; do (cd /tmp && env -i "$dir/$b" selftest); done
