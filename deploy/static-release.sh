#!/bin/sh
# Every fleet-* binary in an extracted release tarball is static (CGO_ENABLED=0, read from its Go build info), for any
# target: a darwin binary is checked on linux too.   deploy/static-release.sh <extracted release dir>
set -eu
for b in "$1"/fleet-*; do go version -m "$b" | grep -q 'CGO_ENABLED=0' || { echo "$b: cgo enabled" >&2; exit 1; }; done
