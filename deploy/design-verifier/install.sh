#!/bin/sh
# Run as the operator from an already verified, root-owned release directory.
set -eu
cd "$(dirname "$0")/../.."
release=$(pwd -P)
base=/opt/fleet-design-verifier
case "$release" in
  "$base"/releases/v*) ;;
  *) echo 'Extract the verified release under /opt/fleet-design-verifier/releases/v<version> first' >&2; exit 1 ;;
esac
[ "$(id -u)" = 0 ] && [ -f release.json ] && [ -f deploy/design-verifier/service.mjs ] || {
  echo 'Run as root from a complete verified release' >&2
  exit 1
}
/usr/bin/node --input-type=module -e 'import { readFileSync } from "node:fs"; const { commit } = JSON.parse(readFileSync("release.json", "utf8")); if (!/^[0-9a-f]{40}$/.test(commit)) process.exit(1)'
/usr/bin/node --check deploy/design-verifier/service.mjs
install -m 0644 deploy/design-verifier/fleet-design-verifier.service /etc/systemd/system/fleet-design-verifier.service
ln -sfn "$release" "$base/.current"
mv -Tf "$base/.current" "$base/current"
systemctl daemon-reload
systemctl enable fleet-design-verifier.service
systemctl restart fleet-design-verifier.service
