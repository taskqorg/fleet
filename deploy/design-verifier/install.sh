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
[ "$(id -u)" = 0 ] && [ -f release.json ] && [ -f deploy/design-verifier/service.mjs ]
/usr/bin/node --check deploy/design-verifier/service.mjs
install -m 0644 deploy/design-verifier/fleet-design-verifier.service /etc/systemd/system/fleet-design-verifier.service
ln -sfn "$release" "$base/.current"
mv -Tf "$base/.current" "$base/current"
systemctl daemon-reload
systemctl enable fleet-design-verifier.service
systemctl restart fleet-design-verifier.service
