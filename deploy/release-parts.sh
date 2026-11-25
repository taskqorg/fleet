#!/bin/sh
# Prints the content digest of each release part of a commit as JSON (docs/designs/fleet-host-updates.md section 7):
#   deploy/release-parts.sh <commit>
# Run in a checkout of that commit (the package lists come from `go list -deps` on its go/ tree). A part's digest is the
# SHA-256 of the sorted lines "<git blob id> <path>" of its files at the commit, without *_test.go, *.md and testdata/.
set -eu
commit=$(git rev-parse "$1^{commit}")
[ "$(git rev-parse HEAD)" = "$commit" ] || { echo "release-parts: checkout is not $commit" >&2; exit 2; }
top=$(git rev-parse --show-toplevel)
# Directories of the module's own packages the commands import, for both release targets.
pkgs() {
  list=$(cd "$top/go" && for os in linux darwin; do
    GOOS=$os go list -deps -f '{{if .Module}}{{if .Module.Main}}{{.Dir}}{{end}}{{end}}' "$@" || exit 1
  done) || return 1
  printf '%s\n' "$list" | sed "s|^$top/||" | sort -u
}
# blobs <dir/>... lists the files directly in each package directory; trees <path>... lists every file below each path.
blobs() { for d in "$@"; do git ls-tree "$commit" -- "$d/"; done; }
trees() { git ls-tree -r "$commit" -- "$@"; }
digest() {
  awk '$2 == "blob" { print $3, $4 }' | grep -Ev '(_test\.go|\.md)$|(^| |/)testdata/' | sort -u | { shasum -a 256 2>/dev/null || sha256sum; } | cut -d' ' -f1
}
# A failed go list must fail the run: a digest without the packages would look unchanged forever.
exec_pkgs=$(pkgs ./executor ./update) && [ -n "$exec_pkgs" ] || { echo "release-parts: go list failed for the executor" >&2; exit 1; }
coord_pkgs=$(pkgs ./coordinator ./gateway) && [ -n "$coord_pkgs" ] || { echo "release-parts: go list failed for the coordinator" >&2; exit 1; }
executor=$( { blobs $exec_pkgs; trees go/go.mod go/go.sum config/executor-defaults.json deploy/hosts scripts/lab; } | digest)
coordinator=$( { blobs $coord_pkgs; trees go/go.mod go/go.sum config deploy; } | digest)
web=$(trees app packages package.json pnpm-lock.yaml | digest)
cli=$(trees config/harness-pins.json | digest)
tool=$(trees config/tool-pins.json | digest)
printf '{"executor":"%s","cliPins":"%s","toolPins":"%s","coordinator":"%s","web":"%s"}\n' "$executor" "$cli" "$tool" "$coordinator" "$web"
