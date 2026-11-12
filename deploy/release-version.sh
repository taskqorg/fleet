#!/bin/sh
# Prints the release version of a commit on main: 1.<yymmdd>.<M>, yymmdd the UTC date of its commit time and M its
# 0-based position among that day's first-parent commits (docs/designs/fleet-delivery-pipeline.md#4-automatic-release).
# A function of the commit alone, so the merge queue run builds the exact version the release later publishes; the merge
# queue moves main to the tested commit, so the queue chain and main's first-parent chain are the same.
#   deploy/release-version.sh <commit>    (needs the commit's history back to the start of its day)
set -eu
day=$(TZ=UTC git log -1 --format=%cd --date=format-local:%y%m%d "$1")
m=$(TZ=UTC git log --first-parent --format=%cd --date=format-local:%y%m%d "$1" | awk -v d="$day" '$1 != d { exit } { n++ } END { print n - 1 }')
echo "1.$day.$m"
