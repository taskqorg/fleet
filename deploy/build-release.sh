#!/bin/sh
# Builds the Fleet release from a commit: one tarball per target with the Go binaries, the built web app, the shipped
# config and deploy/, all from a clean `git archive` (docs/designs/fleet-self-update.md, section 1):
#   deploy/build-release.sh <commit> <out-dir> <version> <previous version or ""> [os/arch ...]
# default targets linux/amd64 darwin/arm64. Writes <out-dir>/fleet-<version>-<os>-<arch>.tgz and <out-dir>/SHA256SUMS.
# Layout inside (the same on every host):
#   fleet-coordinator  fleet-gateway  fleet-executor  fleet-update  dist/app/  config/  deploy/  scripts/lab/  release.json
# Binaries are static (CGO_ENABLED=0). PDS18 and WSL use linux/amd64, the Mac darwin/arm64. Windows is not built.
# COPYFILE_DISABLE=1 keeps macOS tar from adding AppleDouble ._* files.
set -eu
. "$(dirname "$0")/status.sh"
[ $# -ge 4 ] || { echo 'usage: deploy/build-release.sh <commit> <out-dir> <version> <previous> [os/arch ...]' >&2; exit 2; }
out=$(cd "$2" && pwd) && rm -f "$out/SHA256SUMS"
cd "$(dirname "$0")/.."
commit=$(git rev-parse "$1^{commit}") version=$3 previous=$4
shift 4
[ $# -gt 0 ] || set -- linux/amd64 darwin/arm64
work=$(mktemp -d)
trap 'code=$?; rm -rf "$work"; [ "$code" -eq 0 ] || status error "" "build failed"' EXIT
status working 5 "extracting $commit"
git archive "$commit" | tar -x -C "$work"
cd "$work"
status working 15 "building the web app"
corepack pnpm install --frozen-lockfile
node app/build.mjs
step=0
for target in "$@"; do
  os=${target%/*} arch=${target#*/} stage="$work/stage-$os-$arch"
  status working $((30 + 60 * step / $#)) "building $os/$arch"
  mkdir -p "$stage/dist"
  (cd go && for cmd in coordinator gateway executor update; do
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-X main.version=$version" -o "$stage/fleet-$cmd" "./$cmd"
  done)
  cp -R dist/app "$stage/dist/" && cp -R config deploy "$stage/" && mkdir -p "$stage/scripts" && cp -R scripts/lab "$stage/scripts/"
  printf '{"version":"%s","previous":"%s","commit":"%s","target":"%s","builtAt":"%s"}\n' \
    "$version" "$previous" "$commit" "$os-$arch" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$stage/release.json"
  file="fleet-$version-$os-$arch.tgz"
  COPYFILE_DISABLE=1 tar --no-xattrs -czf "$out/$file" -C "$stage" .
  if tar -tzf "$out/$file" | grep -q '\(^\|/\)\._'; then echo "AppleDouble files in $file" >&2; exit 1; fi
  (cd "$out" && { shasum -a 256 "$file" 2>/dev/null || sha256sum "$file"; } | tee -a SHA256SUMS)
  step=$((step + 1))
done
status done 100 "built $version ($commit) for $*"
