#!/bin/sh
# Builds a coordinator release tarball from a commit, the way the PDS18 releases are made:
#   deploy/build-release.sh <commit> <out-dir>   writes <out-dir>/fleet-<12-char commit>.tgz
# The tarball holds dist/, package.json, the pnpm lockfile and workspace settings, and release.json; the server installs
# production dependencies with `corepack pnpm install --prod --frozen-lockfile`. COPYFILE_DISABLE=1 keeps macOS tar from
# adding AppleDouble ._* files (release 4a4e006 carried 151 of them, and the coordinator read ._*.json as registry files).
set -eu
[ $# -eq 2 ] || { echo 'usage: deploy/build-release.sh <commit> <out-dir>' >&2; exit 2; }
out=$(cd "$2" && pwd)
cd "$(dirname "$0")/.."
commit=$(git rev-parse --short=12 "$1^{commit}")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
git archive "$commit" | tar -x -C "$work"
cd "$work"
# corepack runs the pnpm pinned in package.json, the same one the server installs with.
corepack pnpm install --frozen-lockfile
corepack pnpm run build
printf '{"commit":"%s","built":"%s"}\n' "$commit" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > release.json
COPYFILE_DISABLE=1 tar -czf "$out/fleet-$commit.tgz" dist package.json pnpm-lock.yaml pnpm-workspace.yaml release.json
if tar -tzf "$out/fleet-$commit.tgz" | grep -q '\(^\|/\)\._'; then echo "AppleDouble files in the tarball" >&2; exit 1; fi
shasum -a 256 "$out/fleet-$commit.tgz" 2>/dev/null || sha256sum "$out/fleet-$commit.tgz"
