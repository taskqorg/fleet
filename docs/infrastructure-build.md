# Fleet infrastructure

Observed 2026-11-09. Public coordinator activation remains pending; guard preparation is installed and qualified.

## Installed

- DNS: exact proxied A `fleet.ufobe.com` → `155.103.4.2`, TTL auto (1), record `daa038f23febe515a60c204d3f8850d0`; API write/readback succeeded. No other records changed.
- Account `fleet`, UID/GID 1005, only group fleet, no sudo; home 0750, linger enabled. Private 0700 config/data/releases/runtime directories under `/home/fleet`. SQLite path `/home/fleet/data/fleet.sqlite`; no invented retention expiry.
- Node v22.18.0 at `/home/fleet/runtime/node-v22.18.0-linux-x64/bin/node`, official archive SHA256 verified against official SHASUMS256.txt; readback as fleet succeeded. Runtime root-owned, not group/other writable.
- `/home/fleet/.config/fleet/coordinator.env`, fleet-owned 0600: PORT, FLEET_DATABASE, FLEET_HOST_ID, FLEET_BROWSER_ORIGIN, FLEET_MAX_ARTIFACT_BYTES, distinct FLEET_BROWSER_SECRET/FLEET_MACHINE_SECRET. Values stay host-only. Port 9470, owner-mac, HTTPS origin, 16777216-byte artifact limit match coordinator contract.
- Coordinator user unit installed and verified, inactive; entry `/home/fleet/releases/current/dist/packages/coordinator/server.js`. Parent owns candidate release validation/activation.
- Fleet Access hostname app `d96d8ee0-0150-4f70-85d5-ac9b88af12b1`, one exact owner-email allow policy privately matched to existing Dash owner policy and read back. No personal allowlist values stored here.
- Exact Access bypass apps for `/api/hosts/poll`, `/api/hosts/receipts`, `/api/hosts/events`, `/api/hosts/artifacts`, each read back with one bypass policy. Access cannot restrict method; Caddy permits only exact POST paths past browser guard. Application must enforce dedicated bearer plus host/execution ownership.
- Dedicated Fleet-only JWT verifier on `127.0.0.1:9471`, user unit `fleet-access-guard.service`, active MainPID 100480, NRestarts 0 at readback. Isolated `/home/fleet/runtime/guard-venv`; dependencies pinned in `deploy/infrastructure/guard-requirements.lock`. Pip bootstrap wheel SHA256 matched PyPI metadata. Guard source and runtime root-owned; configuration `/home/fleet/.config/fleet/access-guard.env` mode0600. No shared guard modified.

## Guard proof

Guard derives from inspected host access_guard.py, with Fleet-specific audience, existing issuer, one private configured owner, RS256 only and required exp/iat/sub/email claims. Fixture tests passed for valid owner and rejection of wrong issuer/audience/owner/time, missing claims, forged signature, wrong algorithm, malformed/oversize/duplicate/missing headers; key-fetch outage returns503. No real authorized browser token was available, so live owner login remains unverified.

Running verifier returns403 for missing and forged JWT. Dedicated private Caddy on127.0.0.1:9472 validated the actual routing candidate: GET returned403 for all four machine paths; POST returned502 because the coordinator is intentionally inactive. Thus POST bypass reaches only the unavailable loopback upstream; bearer/ownership proof belongs to coordinator candidate checks. Private Caddy was terminated and9472 read back free. Existing system Caddy remained active.

## Prepared, not activated

Caddy candidate `deploy/infrastructure/fleet.caddy.candidate` is not installed in `/etc/caddy/sites`; no public route or Caddy reload was performed. Guard service candidate, coordinator user unit candidate and focused fixture test are in the same directory. Disk available524GiB and available RAM about26GiB at initial inspection; no unrelated workload/config/data changed.

Parent must finish corrected release validation, coordinator auth/ownership checks, current symlink and service activation, then validate exact Fleet Caddy site before graceful reload. Real owner Access browser login and integrated browser/native execution remain acceptance work. Generated previews must retain isolation from coordinator credentials.

## Repeat installation/check

Preserve protected host env files. Use dedicated root-owned guard source/runtime and fleet user units; install pinned guard dependencies into its isolated venv, never shared Hermes runtime. Run fixture with FLEET_ACCESS_OWNER loaded privately from guard env. Use `systemd-analyze --user verify` for units, then fleet user daemon-reload and enable/start only the guard. Read back active PID/restarts, `ss -ltn`, and missing/forged-JWT403. Never start coordinator/public route as part of guard setup.

Recheck DNS/Access exact identities through API without printing policy identities or tokens. Use `id fleet`, `loginctl show-user fleet -p Linger`, private-file `stat`, Node version as fleet and loopback listener inspection. Caddy must pass validation before parent-authorized graceful reload; no firewall edits or unrelated restarts.

## Browser proxy authentication update

Parent activated coordinator candidate89797774 and Fleet site. Fleet guard now reads the existing browser credential privately from its mode0600 guard EnvironmentFile and returns it as an internal Authorization response header only after valid owner JWT verification. Fleet Caddy strips incoming Authorization after the exact machine POST handle, then copies Authorization from successful forward_auth into the upstream request. Machine POST keeps its original bearer and receives no browser credential. Guard-only restart and validated graceful Caddy reload applied; coordinator was not restarted by this worker. FLEET_BROWSER_PROXY_AUTH=true is prepared in coordinator env for parent's updated build/restart.

Focused signing fixtures verify correct internal header on204 and no Authorization header on denied tokens; existing JWT negative checks still pass. Public/no-auth and forged direct-origin response checks assert the actual secret is absent from headers/body and no Authorization response header exists. Real authorized owner's browser acceptance remains with parent; this worker did not touch the interactive login.

## Phase 0 candidate

Files only; nothing below is installed. Source: `docs/designs/fleet-data-foundation.md` Phase 0 and tight D4, D5, D6, D7, D8. Secret values (ClickHouse password, Garage rpc secret, admin and metrics tokens, S3 keys, tunnel credentials) live only in host files under `/home/fleet/.config/fleet/`, mode 0600, created on the host. Validate each user unit with `systemd-analyze --user verify` (Quadlet: `/usr/libexec/podman/quadlet -dryrun -user`) before `systemctl --user daemon-reload`.

Install order. Each step passes its readback before the next starts.

1. **Postgres** (`deploy/infrastructure/fleet-postgres.sql`, root route). Add the `pg_ident.conf` map and `pg_hba.conf` line from the file header, reload PostgreSQL, run the file as `postgres`.
   - Readback: as `fleet`, `psql -d fleet -U fleet -c 'select current_user'` and the same with `-U fleet_app`; `\l fleet` shows owner `fleet`; `\du fleet_app` shows no superuser, no BYPASSRLS. The database appears in the next nightly `pg_dump`.
   - Rollback: `DROP DATABASE fleet; DROP ROLE fleet_app; DROP ROLE fleet;`, remove the map and hba lines, reload.
2. **Directories** (root). `/home/fleet/data/clickhouse`, `/home/fleet/data/garage/{meta,data}`, `/home/fleet/.config/fleet/garage/`, `/home/fleet/work`, all owned by `fleet`, 0700. `/backup/fleet` owned by `fleet`, 0700.
   - Readback: `stat` owner and mode. Rollback: remove empty directories.
3. **ClickHouse** (`fleet-clickhouse.container`, `fleet-clickhouse-backup.xml`, host-only `clickhouse.env`). As `fleet`: `podman pull` the digest, check `podman image inspect --format '{{.Digest}}'` equals the pinned digest, install the unit, daemon-reload, start.
   - Readback: `ss -ltn` shows `127.0.0.1:9480` only; `curl -fsS --user ... http://127.0.0.1:9480/ --data-binary 'select version()'` returns 26.8.36.3; files under `/home/fleet/data/clickhouse` are owned by `fleet`.
   - Rollback: `systemctl --user stop fleet-clickhouse`, remove the unit, daemon-reload; data stays until the operator removes it.
4. **Garage** (`fleet-garage.container`, `garage.toml.candidate` as `garage.toml`, host-only secret files: `openssl rand -hex 32` for rpc_secret, tokens likewise). Pull and verify the digest as in step 3, start. Then `garage layout assign -z pds18 -c <capacity> <node>` and `garage layout apply --version 1`, `garage bucket create fleet`, `garage key create fleet-coordinator`, `garage bucket allow --read --write fleet --key fleet-coordinator`, and a read-only `fleet-backup` key. Capacity is an operator setting. Key secrets go straight to host-only files.
   - Readback: `ss -ltn` shows 9490, 9491, 9492 on `127.0.0.1` only; `garage status` shows one healthy node; an S3 put, get and delete of a test object with the coordinator key succeeds over `http://127.0.0.1:9490`.
   - Rollback: stop and remove the unit; data stays until the operator removes it.
5. **Tunnel** (`fleet-cloudflared.service.candidate`). Operator creates tunnel `fleet-store-pds18`, routes `fleet-store-pds18.ufobe.com`, and adds the Access application (group `fleet-people` plus the Fleet service token). Credentials file stays host-only.
   - Readback: unit active; no new public listener in `ss -ltn`; the hostname without Access credentials returns the Access login/403; with the service token, a presigned GET for the test object succeeds (proves the Host header reaches Garage unchanged).
   - Rollback: stop and remove the unit, delete the DNS route and tunnel.
6. **Launch helper** (`fleet-launch@.service` to `/etc/systemd/system/`, `50-fleet-launch.rules` to `/etc/polkit-1/rules.d/`, root). Also create `/srv/fleet/areas`, root-owned 0755. The helper's ExecStartPre (root) checks the instance (`fleet-*` user, uid >= 1000, safe execution id) and runs `install -d -o <user> -g <user> -m 0700 /srv/fleet/areas/<user>/<execution>`. ExecStart (root) refuses unless `/home/fleet/work/<execution>/run` is a regular file owned by `fleet` and not a symlink, opens it as stdin, cd's into the area, then `setpriv` drops to the account user and runs `/bin/sh -s`. Account users never need access to `/home/fleet` (0750) or to each other's areas. The `fleet-*` prefix assumes no privileged system user carries that name; the uid check backs it up. Test with a throwaway `fleet-probe-1` user (uid >= 1000) and `/home/fleet/work/probe1/run` containing `id; pwd`.
   - Readback: as `fleet`, `systemctl start fleet-launch@fleet-probe-1:probe1.service` succeeds and the journal shows the probe user's `id` and `/srv/fleet/areas/fleet-probe-1/probe1` (owner `fleet-probe-1`, 0700); `systemctl start fleet-launch@hermes:probe1.service` and any other unit are denied; a symlinked or non-`fleet`-owned run file is refused; `systemd-cgls` shows the run in its own cgroup.
   - Rollback: remove both files, `systemctl daemon-reload`; polkit picks up rule removal by itself. Areas under `/srv/fleet/areas` are removed by the operator.
7. **Backup** (`fleet-backup.sh` to `/home/fleet/runtime/`, root-owned, `fleet-backup.service`, `fleet-backup.timer`, host-only `backup-garage.env`). Needs `rclone` from the declared package list.
   - Readback: `systemctl --user start fleet-backup.service` once, then `/backup/fleet/<today>/clickhouse` and `/backup/fleet/<today>/garage/fleet` exist; ClickHouse and Garage stayed active (no restart in `systemctl --user show -p NRestarts`); `systemctl --user list-timers` lists the timer. A restore proof (RESTORE into a scratch database, object hash check) is required before the backup counts as Shipped (tight D6).
   - Rollback: disable the timer, remove units and script; `/backup/fleet` content is removed by the operator.
8. **macOS store** (`deploy/hosts/macos/fleet-garage.plist`, `fleet-cloudflared.plist`). Replace `FLEET_HOME`, `brew install garage cloudflared`, check `garage --version` is 2.5.0, load with `launchctl bootstrap gui/$(id -u)`, then the layout, bucket and key steps from step 4.
   - Readback: `lsof -iTCP -sTCP:LISTEN` shows 9490-9492 on `127.0.0.1` only; `launchctl print gui/$(id -u)/com.ufobe.fleet-garage` is running; tunnel checks as in step 5 for `fleet-store-mac.ufobe.com`.
   - Rollback: `launchctl bootout` both agents, remove plists, delete tunnel and route.

Coordinator wiring (`fleet.service.candidate` After/Wants, `coordinator.env` URLs) is Phase 0 item 5 and not in this candidate.
