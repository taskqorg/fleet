#!/bin/sh
# Installs a Fleet release tarball on this host and restarts the Fleet services it runs (deploy/README.md runbook):
#   deploy/install-release.sh <fleet-<commit>-<os>-<arch>.tgz>    the .sha256 file must sit next to it
#   deploy/install-release.sh --rollback                          back to the release before the current one
# Releases unpack into $FLEET_RELEASES (default ~/releases) as <name>/; `current` points at the active one and `previous`
# at the one before. Restarted: the systemd user units fleet, fleet-gateway, fleet-access-guard and
# fleet-executor that are installed here (Linux, WSL), or the launchd agent com.ufobe.fleet-executor (macOS).
set -eu
. "$(dirname "$0")/status.sh"
releases=${FLEET_RELEASES:-$HOME/releases}
mkdir -p "$releases"
if [ "${1:-}" = --rollback ]; then
  target=$(readlink "$releases/previous") || { echo "no previous release" >&2; exit 1; }
else
  [ $# -eq 1 ] || { echo 'usage: deploy/install-release.sh <tarball> | --rollback' >&2; exit 2; }
  status working 10 "verifying $(basename "$1")"
  (cd "$(dirname "$1")" && { shasum -a 256 -c "$(basename "$1").sha256" 2>/dev/null || sha256sum -c "$(basename "$1").sha256"; }) >/dev/null
  target=$releases/$(basename "$1" .tgz)
  status working 30 "unpacking into $target"
  rm -rf "$target.partial" && mkdir "$target.partial"
  tar -xzf "$1" -C "$target.partial"
  "$target.partial/fleet-gateway" selftest >/dev/null
  rm -rf "$target" && mv "$target.partial" "$target"
fi
old=$(readlink "$releases/current" || true)
[ -n "$old" ] && [ "$old" != "$target" ] && ln -sfn "$old" "$releases/previous"
ln -sfn "$target" "$releases/current"
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
  if systemctl --user is-active --quiet fleet-gateway.socket; then
    curl -fsS http://127.0.0.1:8742/healthz >/dev/null || { echo "gateway health check failed" >&2; exit 1; }
  fi
fi
status done 100 "running $(basename "$target")"
