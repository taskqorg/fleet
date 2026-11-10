#!/bin/sh
# Installs a Fleet release tarball on this host and restarts the Fleet services it runs (deploy/README.md runbook):
#   deploy/install-release.sh <fleet-<version>-<os>-<arch>.tgz>   SHA256SUMS of the release must sit next to it
#   deploy/install-release.sh --rollback                           back to the release before the current one
# First install and manual recovery only: fleet-update installs every later release (docs/designs/fleet-self-update.md).
# Releases unpack into $FLEET_RELEASES (default ~/releases) as <version>/ (version from release.json); `current`
# points at the active one and `previous` at the one before. Restarted: the systemd user units fleet, fleet-gateway, fleet-access-guard and
# fleet-executor that are installed here (Linux, WSL), or the launchd agent com.ufobe.fleet-executor (macOS).
# Where fleet runs, it waits up to 30 s for GET /api/health on 127.0.0.1:9470 (database reachable).
set -eu
. "$(dirname "$0")/status.sh"
releases=${FLEET_RELEASES:-$HOME/releases}
mkdir -p "$releases"
if [ "${1:-}" = --rollback ]; then
  target=$(readlink "$releases/previous") || { echo "no previous release" >&2; exit 1; }
else
  [ $# -eq 1 ] || { echo 'usage: deploy/install-release.sh <tarball> | --rollback' >&2; exit 2; }
  status working 10 "verifying $(basename "$1")"
  (cd "$(dirname "$1")" && grep " $(basename "$1")\$" SHA256SUMS | { shasum -a 256 -c 2>/dev/null || sha256sum -c; }) >/dev/null
  partial=$releases/install.tmp-$$
  rm -rf "$partial" && mkdir "$partial"
  tar -xzf "$1" -C "$partial"
  version=$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' "$partial/release.json")
  [ -n "$version" ] || { echo "no version in release.json" >&2; exit 1; }
  target=$releases/$version
  status working 30 "unpacking into $target"
  for bin in fleet-coordinator fleet-gateway fleet-executor fleet-update; do
    if [ -x "$partial/$bin" ]; then "$partial/$bin" selftest >/dev/null; fi
  done
  find "$partial" -type f -exec chmod a-w {} +   # directories stay 0755, so rm -rf still works
  rm -rf "$target" && mv "$partial" "$target"
fi
old=$(readlink "$releases/current" || true)
[ -n "$old" ] && [ "$(basename "$old")" != "$(basename "$target")" ] && ln -sfn "$(basename "$old")" "$releases/previous"
ln -sfn "$(basename "$target")" "$releases/current"  # relative links: fleet-update works through os.Root
status working 60 "switched to $(basename "$target")"
if [ "$(uname)" = Darwin ]; then
  agent=gui/$(id -u)/com.ufobe.fleet-executor
  launchctl print "$agent" >/dev/null 2>&1 && launchctl kickstart -k "$agent"
else
  for unit in fleet fleet-gateway fleet-access-guard fleet-executor; do
    systemctl --user cat "$unit.service" >/dev/null 2>&1 || continue
    status working 75 "restarting $unit"
    systemctl --user restart "$unit.service"
  done
  if systemctl --user cat fleet.service >/dev/null 2>&1; then
    status working 90 "waiting for coordinator health"
    i=0; until curl -fsS -m 3 http://127.0.0.1:9470/api/health >/dev/null 2>&1; do
      i=$((i+1)); [ $i -lt 30 ] || { echo "coordinator health check failed" >&2; exit 1; }; sleep 1; done
  fi
  if systemctl --user is-active --quiet fleet-gateway.socket; then
    curl -fsS http://127.0.0.1:8742/healthz >/dev/null || { echo "gateway health check failed" >&2; exit 1; }
  fi
fi
status done 100 "running $(basename "$target")"
