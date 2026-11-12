#!/bin/sh
# Build a source-bound UI archive; publication uses the protected release signer.
set -eu
[ "$#" -eq 4 ] || { echo 'usage: build-ui-release.sh <commit> <out-dir> <version> <backend-version>' >&2; exit 2; }
commit=$(git rev-parse "$1^{commit}")
out=$(cd "$2" && pwd)
version=$3 backend=$4
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
git archive "$commit" | tar -x -C "$work"
cd "$work"
corepack pnpm install --frozen-lockfile
node app/build.mjs
VERSION="$version" BACKEND="$backend" COMMIT="$commit" node --input-type=module <<'JS'
import { writeFileSync } from 'node:fs';
writeFileSync('dist/app/release.json', JSON.stringify({kind: 'ui', version: process.env.VERSION, backendVersion: process.env.BACKEND, commit: process.env.COMMIT}));
JS
file="fleet-ui-$version.tgz"
COPYFILE_DISABLE=1 tar --no-xattrs -czf "$out/$file" -C dist/app .
(cd "$out" && { shasum -a 256 "$file" 2>/dev/null || sha256sum "$file"; } > SHA256SUMS)
