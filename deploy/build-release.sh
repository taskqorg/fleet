#!/bin/sh
# Builds the Fleet release from a commit: one tarball per target with the Go coordinator, gateway and executor,
# the built web app and the shipped config, all from a clean `git archive`:
#   deploy/build-release.sh <commit> <out-dir> [os/arch ...]     default targets: linux/amd64 darwin/arm64
# writes <out-dir>/fleet-<12-char commit>-<os>-<arch>.tgz and its .sha256. Layout inside (the same on every host):
#   fleet-coordinator  fleet-gateway  fleet-executor  dist/app/  config/  release.json
# Binaries are static (CGO_ENABLED=0). PDS18 and WSL use linux/amd64, the Mac darwin/arm64. Windows is not built.
# COPYFILE_DISABLE=1 keeps macOS tar from adding AppleDouble ._* files.
set -eu
. "$(dirname "$0")/status.sh"
[ $# -ge 2 ] || { echo 'usage: deploy/build-release.sh <commit> <out-dir> [os/arch ...]' >&2; exit 2; }
out=$(cd "$2" && pwd)
cd "$(dirname "$0")/.."
commit=$(git rev-parse --short=12 "$1^{commit}")
shift 2
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
  (cd go && go generate ./... && for cmd in coordinator gateway executor; do
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-X main.version=$commit" -o "$stage/fleet-$cmd" "./$cmd"
  done)
  cp -R dist/app "$stage/dist/" && cp -R config "$stage/"
  printf '{"commit":"%s","target":"%s","built":"%s"}\n' "$commit" "$target" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$stage/release.json"
  file="fleet-$commit-$os-$arch.tgz"
  COPYFILE_DISABLE=1 tar --no-xattrs -czf "$out/$file" -C "$stage" .
  if tar -tzf "$out/$file" | grep -q '\(^\|/\)\._'; then echo "AppleDouble files in $file" >&2; exit 1; fi
  (cd "$out" && { shasum -a 256 "$file" 2>/dev/null || sha256sum "$file"; } > "$file.sha256" && cat "$file.sha256")
  step=$((step + 1))
done
status done 100 "built $commit for $*"
