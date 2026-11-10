# deploy

Service files and scripts for running Fleet. Nothing here is applied automatically.

* `build-release.sh <commit> <out-dir> [os/arch ...]`: one release tarball per target (`fleet-<commit>-<os>-<arch>.tgz` and `.sha256`) from a clean `git archive`. Every tarball has the same layout: `fleet-coordinator`, `fleet-gateway`, `fleet-executor` (static Go binaries), `dist/app/`, `config/`, `release.json`.
* `install-release.sh <tarball> | --rollback`: verifies, unpacks into `~/releases/<name>/`, points `~/releases/current` at it (the old one becomes `previous`) and restarts the Fleet services installed on this host.
* `status.sh`: sourced by both scripts. When stdout is a terminal they print [OSC 7501 program status](https://www.superlogical.com/rex/docs/build/program-status): `state=working` with `progress`, then `done` or `error`, app `fleet-deploy`, base64 `msg`.
* `infrastructure/`: PDS18 units following tight: the coordinator (`fleet.service.candidate`), the gateway and its socket, the Access origin guard (`fleet-access-guard.service`, which runs `fleet-gateway guard`), ClickHouse and Garage Quadlets, Postgres role setup (`fleet-postgres.sql`), backups, the root-owned `fleet-launch@.service` helper, and the Caddy and Cloudflare tunnel candidates. Files ending in `.candidate` are proposed, not installed.
* `hosts/linux/fleet-executor.service`: the executor as a systemd user unit (PDS18, WSL).
* `hosts/macos/`: launchd agents for the Mac (executor, Garage, cloudflared).

Design: [data foundation](../docs/designs/fleet-data-foundation.md) Phase 0 and §10.

## Runbook: PDS18, the owner's Mac, WSL

Each host runs the binaries from `~/releases/current` of its service user. PDS18 runs the coordinator, the gateway, the guard and an executor; the Mac and WSL run an executor. Environment files under `~/.config/fleet/` stay on the host, mode 0600, and are never printed.

1. **Build** on any machine with Go, Node and corepack, from the commit to deploy:
   `deploy/build-release.sh <commit> ./out linux/amd64 darwin/arm64`. Copy the `linux-amd64` tarball and its `.sha256` to PDS18 and WSL, and the `darwin-arm64` pair to the Mac.
2. **First install only**, per host:
   * PDS18, user `fleet`: copy `infrastructure/fleet.service.candidate` as `fleet.service`, `fleet-gateway.service`, `fleet-gateway.socket`, `fleet-access-guard.service` and `hosts/linux/fleet-executor.service` to `~/.config/systemd/user/`; write `coordinator.env`, `access-guard.env` (`FLEET_BROWSER_SECRET`), `gateway.json` (see the unit's comment; `state_dir` is `<FLEET_GATEWAY_DIR>/state`) and `executor.env`; `systemctl --user daemon-reload && systemctl --user enable fleet fleet-gateway.socket fleet-gateway fleet-access-guard fleet-executor`. Caddy uses `infrastructure/fleet.caddy.candidate` (ports 9470 coordinator, 8742 gateway, 9471 guard).
   * Mac, executor service user: install `hosts/macos/fleet-executor.plist` as its comment says and write `executor.env`.
   * WSL: set `systemd=true` in `/etc/wsl.conf`, then install `hosts/linux/fleet-executor.service` and `executor.env` as in its comment.
3. **Install** on each host, PDS18 first: `deploy/install-release.sh <tarball>`. It stops on a checksum mismatch, a failed `fleet-gateway selftest` or, on PDS18, a failed gateway `/healthz`.
4. **Check**: on PDS18 `systemctl --user status fleet fleet-gateway fleet-access-guard fleet-executor`, then open https://fleet.ufobe.com and see each host's executor reconnect; on the Mac `launchctl print gui/$(id -u)/com.ufobe.fleet-executor`; on WSL `systemctl --user status fleet-executor`.
5. **Roll back** a host: `deploy/install-release.sh --rollback` points `current` at the previous release and restarts the same services.
