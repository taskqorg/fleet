#!/bin/sh
# Builds the Go front door into the release tree (docs/designs/fleet-stack.md#go-code): dist/go/fleet-coordinator-<os>-<arch>
# for linux/amd64 and darwin/arm64, without cgo, -trimpath, -mod=readonly, each with a SHA-256 file.
set -eu
cd "$(dirname "$0")/../go"
go generate ./...
mkdir -p ../dist/go
for t in linux/amd64 darwin/arm64; do
  os=${t%/*} arch=${t#*/} file=fleet-coordinator-${t%/*}-${t#*/}
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -mod=readonly -trimpath -o "../dist/go/$file" ./coordinator
  go version -m "../dist/go/$file" | grep -q 'CGO_ENABLED=0' || { echo "$file: cgo enabled" >&2; exit 1; }
  (cd ../dist/go && { sha256sum "$file" 2>/dev/null || shasum -a 256 "$file"; } > "$file.sha256")
done
