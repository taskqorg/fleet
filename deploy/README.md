# deploy

Service files and scripts for running Fleet. Nothing here is applied automatically.

* `build-release.sh <commit> <out-dir> <version> <previous> [os/arch ...]`: one release tarball per target (`fleet-<version>-<os>-<arch>.tgz`) and `SHA256SUMS` from a clean `git archive`; run by `.github/workflows/release.yml` and for local trials. Every tarball has the same layout: `fleet-coordinator`, `fleet-gateway`, `fleet-executor`, `fleet-update` (static Go binaries), `dist/app/`, `config/`, `deploy/`, `scripts/lab/` (Lab script steps), `release.json` (`version`, `previous`, `commit`, `target`, `builtAt`).
* `install-release.sh <tarball> | --rollback`: first install and manual recovery. Checks the tarball against `SHA256SUMS` next to it, selftests every binary, unpacks into `~/releases/<version>/`, points `~/releases/current` at it (the old one becomes `previous`; relative links) and restarts the Fleet services installed on this host.
* `status.sh`: sourced by both scripts. When stdout is a terminal they print [OSC 7501 program status](https://www.superlogical.com/rex/docs/build/program-status): `state=working` with `progress`, then `done` or `error`, app `fleet-deploy`, base64 `msg`.
* `infrastructure/`: PDS18 units following tight: the coordinator (`fleet.service`, `fleet.socket`), the gateway and its socket, the Access origin guard (`fleet-access-guard.service` and `.socket`, which runs `fleet-gateway guard`), ClickHouse and Garage Quadlets, Postgres role setup (`fleet-postgres.sql`), backups, the root-owned `fleet-launch@.service` helper, and the Caddy and Cloudflare tunnel candidates. Files ending in `.candidate` are proposed, not installed.
  `fleet-update.service` and `.timer` run `fleet-update pds18` as root every 10 seconds (`AccuracySec=1s`); `fleet-executor-pds18.conf` points the PDS18 executor at its root-owned releases.
* [`infrastructure/pds18-ci/`](infrastructure/pds18-ci/README.md): isolated trusted-main CI guest provisioning, admission and rollback; separate from production Fleet services.
* `hosts/linux/`: the executor and `fleet-update` (service and timer) as systemd user units (PDS18 executor, WSL).
* `hosts/macos/`: launchd agents for the Mac (executor, `fleet-update`, Garage, cloudflared).

Design: [data foundation](../docs/designs/fleet-data-foundation.md) Phase 0 and §10; [self-update](../docs/designs/fleet-self-update.md).

## Releases

Every push to `main` starts the release workflow ([delivery pipeline](../docs/designs/fleet-delivery-pipeline.md#4-automatic-release)); nobody tags. It runs one at a time (group `release`); merges that land meanwhile form the next batch. It waits for the commit's CI jobs `go`, `app` and `browser` (the merge queue run, else the push run), computes the version `1.<yymmdd>.<M>` (`yymmdd` the UTC date of the run, `M` one more than the highest tag that day, from 0), builds, checks migrations against each of the last 5 releases and publishes on GitHub Releases: the tarballs, `SHA256SUMS`, `SHA256SUMS.sig` (Ed25519, key in environment `release`), `latest.json` (with `checkedWith`) and `provenance.sigstore.jsonl`, tagging the commit by API. Pause: `gh variable set FLEET_RELEASE_PAUSE --body on` stops releases from pushes; `gh variable delete FLEET_RELEASE_PAUSE`, then `gh workflow run release`, releases everything merged meanwhile. `gh workflow run release -f sha=<commit on main>` releases one chosen commit, paused or not; the gate needs that commit's `test-qualification` artifact, kept 7 days; for an older commit (or after a pause longer than that) re-run all jobs of its `ci` run first (the merge-queue run; the gate takes that one), which uploads the artifact again. A failed run is re-run from the Actions page. Every host then installs the release by itself ([self-update](../docs/designs/fleet-self-update.md)): PDS18 within about 2 minutes, each other executor on its next timer run after the coordinator is healthy.

## Check

`POST /api/ops/hosts_list` (or the hosts page) shows per host `version`, `autoUpdate` and `update` (`{version, state, error, at}`; PDS18 adds `parts` for the coordinator side and its executor). `state` is `ok`, `pending` (the executor could not reach the coordinator yet), `held`, `error` (transient; the next run retries), `refused` (with reason `checksum`, `provenance`, `selftest` or `unhealthy`, the last one after a rollback) or `failed` (unhealthy with no previous release). A refused or failed update opens one needs-you question for the holder. `curl -s http://127.0.0.1:9470/api/health` on PDS18 shows the coordinator's `version` and `autoUpdate`. Logs: `journalctl -u fleet-update` (PDS18), `journalctl --user -u fleet-update` (WSL), `~/Library/Logs/fleet-update.log` (Mac).

A holder holds updates with `hosts_update {id, autoUpdate: "off"}` or `autoUpdate: "<RFC 3339 time>Z"` (hold until then), and resumes with `"on"`.

## First install: PDS18, the owner's Mac, WSL

Once per host, from a published release, each host downloading it itself from GitHub (never copied through another host). Environment files under `~/.config/fleet/` stay on the host, mode 0600, and are never printed. Paths are the defaults; no other env line is needed.

1. **Download** in an empty directory on the host (`t=linux-amd64` on PDS18 and WSL, `darwin-arm64` on the Mac):
   ```sh
   v=<version>; t=linux-amd64; u=https://github.com/taskqorg/fleet/releases/download/v$v
   curl -fL -O "$u/fleet-$v-$t.tgz" -O "$u/SHA256SUMS"
   tar -xzf "fleet-$v-$t.tgz" ./deploy/install-release.sh ./deploy/status.sh
   ```
2. **Executor hosts (Mac, WSL)**, as the executor service user:
   * Stop and remove the old executor unit or agent first (the executor holds `executor.lock` in its data directory). Move the old data directory to the default path (Linux and WSL `~/.local/state/fleet/executor`, macOS `~/Library/Application Support/Fleet/executor`) with the executor stopped; the host secret goes to `~/.config/fleet/host-secret` (`scripts/host-secret.sh`).
   * `~/.config/fleet/executor.env` holds one line, `FLEET_HOST_ID=<id>`.
   * `deploy/install-release.sh fleet-$v-$t.tgz` (unpacks into `~/releases/$v/`).
   * WSL (reach it as `ssh desktop`, then `wsl -u alexk -- bash -lc '<command>'`): `systemd=true` in `/etc/wsl.conf`, `loginctl enable-linger alexk`; copy `~/releases/current/deploy/hosts/linux/fleet-executor.service`, `fleet-update.service` and `fleet-update.timer` to `~/.config/systemd/user/`, then `systemctl --user daemon-reload && systemctl --user enable --now fleet-executor fleet-update.timer`. Each `fleet-update` run writes and enables `fleet-update.path` for the real data dir (`FLEET_DATA_DIR` or the default), so an update starts as soon as the executor's poll brings a new target.
   * Mac: from `~/releases/current/deploy/hosts/macos/`, `fleet-executor.plist` and `fleet-update.plist` with `FLEET_HOME` replaced by the home directory into `~/Library/LaunchAgents/` as `com.ufobe.fleet-executor.plist` and `com.ufobe.fleet-update.plist`, then `launchctl bootstrap gui/$(id -u)` for each. Each `fleet-update` run writes and loads `com.ufobe.fleet-update-watch.plist`, which starts `com.ufobe.fleet-update` when the executor's `update.json` changes. Macs installed before 2026-11-12 re-copy `fleet-update.plist` (it no longer has `WatchPaths`) with `launchctl bootout` and `bootstrap` once.
3. **PDS18**:
   * Before, as root (postgres cannot write the directory): `runuser -u postgres -- pg_dump -Fc -d fleet > /backup/fleet/pre-deploy/fleet-pre-$v.dump`; check its size before going on.
   * User `fleet`: `deploy/install-release.sh fleet-$v-$t.tgz` (into `/home/fleet/releases/$v/`; restarts `fleet`, `fleet-gateway`, `fleet-access-guard` and waits for health). The coordinator side keeps its env files (`coordinator.env`, `access-guard.env`, `gateway.json`); updates never touch them. `loginctl enable-linger fleet`.
   * PDS18 executor, user `fleet-agent`: as root, `FLEET_RELEASES=/opt/fleet-agent/releases deploy/install-release.sh fleet-$v-$t.tgz`; `executor.env` with `FLEET_HOST_ID=<its host id>`, data at `/home/fleet-agent/.local/state/fleet/executor`, secret at `/home/fleet-agent/.config/fleet/host-secret`; install `deploy/hosts/linux/fleet-executor.service` plus `deploy/infrastructure/fleet-executor-pds18.conf` as its drop-in `fleet-executor.service.d/pds18.conf`; `loginctl enable-linger fleet-agent`; restart `fleet-executor` as `fleet-agent` (`systemctl --user -M fleet-agent@ restart fleet-executor`). Root reads that executor's `update.json` at the default data path, so a data directory elsewhere needs a link there (`ln -s <data dir> /home/fleet-agent/.local/state/fleet/executor`).
   * Root: `install -d -m 0755 /opt/fleet/update /var/lib/fleet-update`, `install -m 0755 /opt/fleet-agent/releases/current/fleet-update /opt/fleet/update/fleet-update`, copy `deploy/infrastructure/fleet-update.service` and `.timer` to `/etc/systemd/system/`, `systemctl daemon-reload && systemctl enable --now fleet-update.timer` (PDS18 installed before 2026-11-12: copy the new `.timer`, then `systemctl daemon-reload && systemctl restart fleet-update.timer`, so the new interval and `AccuracySec=1s` apply at once; 2026-11-12: 10 seconds, installed the same way).
4. **Delete** old `fleet-<sha12>-…` release directories, and old `<unit>.pre-*` copies once the new units run.
5. **Hosts installed before the defaults** (2026-11-10: PDS18 executor `pds18-native`, the Mac `owner-mac`, WSL `windows`) keep their env files and units, which carry host values that have no release default (project roots, store config, native accounts, gateway URL). Their `fleet-update` reads the same env: the Mac agent sources `executor.env`; on WSL `executor.env` is a link to `executor-windows.env`; on PDS18 the default data path links to `/home/fleet-agent/data/executor-pds18`.
6. **Check** with `hosts_list` as above: every host at `$v`. The first `fleet-update` run marks the installed release healthy (`update.state: ok`).

## Manual rollback

When `fleet-update` itself is broken: `deploy/install-release.sh --rollback` on the host (PDS18 executor: as root with `FLEET_RELEASES=/opt/fleet-agent/releases`) points `current` at `previous` and restarts the same services. Restore a database only by hand, from `/backup/fleet/pre-deploy/fleet-pre-<version>.dump`.

## Rollback drill

Proves the three-start rollback on the live hosts. It needs a breakage on `main`, so it runs only when no other work lands on `main` (agreed with whoever else merges there), and the breakage is reverted at once.

1. `gh variable set FLEET_RELEASE_PAUSE --body on`, so the drill's merges release only when dispatched. Note the running version `N` in `hosts_list` (every host `update.state: ok`).
2. Merge a pull request whose coordinator passes `selftest` but exits at start: in `go/coordinator/main.go`, `log.Fatal("rollback drill")` right after the `selftest` branch. Release it as `D`: `gh workflow run release -f sha=<its merge commit>`.
3. Revert that pull request on `main` right after `D` is published; release the revert as `E` only after the drill.
4. Expect on PDS18 within 5 minutes (one timer tick, then three 30 s health windows before the rollback; `journalctl -u fleet-update`): three `unhealthy start` lines, `rolled back to N`, coordinator back on `N`; `hosts_list` for `pds18` shows `update.state: refused`, `error: unhealthy`, `version: D`; one needs-you question "pds18: update to D rolled back (unhealthy)."; no executor moves (the coordinator never marked `D` healthy). Later runs log nothing for `D`.
5. `gh workflow run release -f sha=<revert commit>` publishes `E`, then `gh variable delete FLEET_RELEASE_PAUSE`: every host installs `E`; the `pds18` `ok` withdraws the question.
