# Fleet infrastructure

Observed 2026-11-09. Public coordinator activation remains pending; guard preparation is installed and qualified.

## Installed

- DNS: exact proxied A `fleet.ufobe.com` → `155.103.4.2`, TTL auto (1), record `daa038f23febe515a60c204d3f8850d0`; API write/readback succeeded. No other records changed.
- Account `fleet`, UID/GID 1005, only group fleet, no sudo; home 0750, linger enabled. Private 0700 config/data/releases/runtime directories under `/home/fleet`. SQLite path `/home/fleet/data/fleet.sqlite`; no invented retention expiry.
- Node v22.18.0 at `/home/fleet/runtime/node-v22.18.0-linux-x64/bin/node`, official archive SHA256 verified against official SHASUMS256.txt; readback as fleet succeeded. Runtime root-owned, not group/other writable.
- `/home/fleet/.config/fleet/coordinator.env`, fleet-owned 0600: PORT, FLEET_DATABASE, FLEET_HOST_ID, FLEET_BROWSER_ORIGIN, FLEET_MAX_ARTIFACT_BYTES, FLEET_BROWSER_SECRET and FLEET_PEOPLE_FILE (host secrets are per host, see configuration.md Hosts). Values stay host-only. Port 9470, owner-mac, HTTPS origin, 16777216-byte artifact limit match coordinator contract.
- Coordinator user unit installed and verified, inactive; entry `/home/fleet/releases/current/dist/packages/coordinator/server.js`. Parent owns candidate release validation/activation.
- Release layout (docs/designs/fleet-stack.md): a release directory holds `package.json`, `deploy/start-coordinator.sh` and the `dist/` tree from `pnpm run build` plus `scripts/build-go-release.sh` (the Go front door in `dist/go/fleet-coordinator-<os>-<arch>`, linux/amd64 and darwin/arm64), and no `node_modules`. `dist/packages/coordinator/server.js` enables source maps and loads `server-main.js`, one esbuild bundle that includes every runtime dependency, with `server-main.js.map` beside it. `migrate-sqlite.js` is the one-off migration CLI. The assets sit at the same paths as before: `dist/packages/coordinator/{db/,connection_call.py,delivery_worker.py}`, `dist/config/{agents,projects,pipelines,lab,skills,catalogue.json}`, `dist/contracts/lab-config.json` and `dist/app`. Start it with `ExecStart=/home/fleet/releases/current/deploy/start-coordinator.sh` and `Environment=FLEET_NODE=/home/fleet/runtime/node-v22.18.0-linux-x64/bin/node`, with WorkingDirectory unchanged: the Go front door listens on `PORT` and the TypeScript coordinator (`node --enable-source-maps dist/packages/coordinator/server.js`) on `PORT+1` behind it; `FLEET_FRONT_DOOR=typescript` in the environment file runs the TypeScript coordinator alone on `PORT`. CI checks this layout through `tests/coordinator/release-smoke.ts`, then, only on a push to `main` in `taskqorg/fleet`, uploads it as the artifact `fleet-release-<commit sha>` (`fleet-release.tgz` and its `.sha256`, also in the job summary). Pull request runs publish nothing. A deploy takes that artifact for the merged commit, never a local build or a pull request run. It checks that the run's event is `push`, its branch is `main` and its repository is `taskqorg/fleet`, and that the archive's SHA-256 matches the summary. PDS18 then runs exactly the release CI started and checked.
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

Preserve protected host env files. Use dedicated root-owned guard source/runtime and fleet user units; install pinned guard dependencies into its isolated venv, never shared Hermes runtime. Run the fixture with FLEET_BROWSER_SECRET loaded privately from guard env; the guard accepts any verified Access e-mail and returns it as X-Fleet-Person. Use `systemd-analyze --user verify` for units, then fleet user daemon-reload and enable/start only the guard. Read back active PID/restarts, `ss -ltn`, and missing/forged-JWT403. Never start coordinator/public route as part of guard setup.

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
6. **Launch helper** (`fleet-launch@.service` to `/etc/systemd/system/`, `50-fleet-launch.rules` to `/etc/polkit-1/rules.d/`, root). Also create `/srv/fleet/areas`, root-owned 0755. The helper's ExecStartPre (root) checks the instance (`fleet-*` user, uid >= 1000, safe execution id) and makes `/srv/fleet/areas/<user>` root:root 0711 (the account user cannot create entries there), then `mkdir -m 0700 /srv/fleet/areas/<user>/<execution>` without `-p`, refusing any existing path or symlink, and chowns only that new directory to the user. ExecStart (root) refuses unless `/home/fleet/work/<execution>/run` is a regular file owned by `fleet` and not a symlink, re-checks that the area is a directory, not a symlink, owned by the user, opens the run file as stdin, cd's into the area, then `setpriv` drops to the account user and runs `/bin/sh -s`. Account users never need access to `/home/fleet` (0750) or to each other's areas. The `fleet-*` prefix assumes no privileged system user carries that name; the uid check backs it up. Test with a throwaway `fleet-probe-1` user (uid >= 1000) and `/home/fleet/work/probe1/run` containing `id; pwd`.
   - Readback: as `fleet`, `systemctl start fleet-launch@fleet-probe-1:probe1.service` succeeds and the journal shows the probe user's `id` and `/srv/fleet/areas/fleet-probe-1/probe1` (owner `fleet-probe-1`, 0700); `systemctl start fleet-launch@hermes:probe1.service` and any other unit are denied; a symlinked or non-`fleet`-owned run file is refused; starting the same execution id twice is refused (area exists); `systemd-cgls` shows the run in its own cgroup.
   - Rollback: remove both files, `systemctl daemon-reload`; polkit picks up rule removal by itself. Areas under `/srv/fleet/areas` are removed by the operator.
7. **Backup** (`fleet-backup.sh` to `/home/fleet/runtime/`, root-owned, `fleet-backup.service`, `fleet-backup.timer`, host-only `backup-garage.env`). Needs `rclone` from the declared package list.
   - Readback: `systemctl --user start fleet-backup.service` once, then `/backup/fleet/<today>/clickhouse` and `/backup/fleet/<today>/garage/fleet` exist; ClickHouse and Garage stayed active (no restart in `systemctl --user show -p NRestarts`); `systemctl --user list-timers` lists the timer. A restore proof (RESTORE into a scratch database, object hash check) is required before the backup counts as Shipped (tight D6).
   - Rollback: disable the timer, remove units and script; `/backup/fleet` content is removed by the operator.
8. **macOS store** (`deploy/hosts/macos/fleet-garage.plist`, `fleet-cloudflared.plist`). Replace `FLEET_HOME`, `brew install garage cloudflared`, check `garage --version` is 2.5.0, load with `launchctl bootstrap gui/$(id -u)`, then the layout, bucket and key steps from step 4.
   - Readback: `lsof -iTCP -sTCP:LISTEN` shows 9490-9492 on `127.0.0.1` only; `launchctl print gui/$(id -u)/com.ufobe.fleet-garage` is running; tunnel checks as in step 5 for `fleet-store-mac.ufobe.com`.
   - Rollback: `launchctl bootout` both agents, remove plists, delete tunnel and route.

Coordinator wiring (`fleet.service.candidate` After/Wants, `coordinator.env` URLs) is Phase 0 item 5 and not in this candidate.

## Phase 0 installed on PDS18, 2026-11-09

Observed by readback after each step. Secret values stay in host files under `/home/fleet/.config/fleet/`, mode 0600.

| Step | Result |
|---|---|
| 0. `rclone` | Installed from Ubuntu (`rclone v1.60.1-DEV`), declared in tight `packages.txt` (MR 19). |
| 1. Postgres | `pg_ident.conf` map `fleet` and `pg_hba.conf` line `local fleet fleet,fleet_app peer map=fleet` added before the general local line (backups `*.pre-fleet-<timestamp>`), reload. Unix user `fleet` connects as `fleet` and `fleet_app`; database `fleet` owned by `fleet`; neither role is superuser or BYPASSRLS; `hermes` is refused. |
| 2. Directories | Data, config, work and `/backup/fleet` directories owned by `fleet`, 0700. |
| 3. ClickHouse | Image digest matches the pin; `fleet-clickhouse.service` active; listens on `127.0.0.1:9480` only; `select version()` returns 26.8.36.3; data owned by `fleet`; NRestarts 0. |
| 4. Garage | Image digest matches the pin; `fleet-garage.service` active; 9490-9492 on `127.0.0.1` only; one node in zone `pds18`, starting capacity 200 GB (an operator setting, to be managed through Fleet); bucket `fleet`; key `fleet-coordinator` read-write, key `fleet-backup` read-only (write refused); 1 MiB put, get and delete with matching SHA-256. |
| 5. Tunnel | `cloudflared` 2026.10.7 in `/home/fleet/runtime` (root-owned, SHA-256 matches the release notes). Remote-managed tunnel `fleet-store-pds18` (status healthy, 4 connections), ingress `fleet-store-pds18.ufobe.com` to `http://127.0.0.1:9490`, proxied CNAME. Access app `Fleet store pds18` allows group `fleet-people` (include copied from the Fleet owner policy). Unauthenticated request returns 302 to Access login. No new public listener; cloudflared metrics on `127.0.0.1:20241`. The unit runs with `--token-file`; the token passed once through the operator session log. |
| 6. Launch helper | `fleet-launch@.service` and `50-fleet-launch.rules` installed; `systemd-analyze verify` clean. Probe user (uid 2000): run as that user in `/srv/fleet/areas/<user>/<execution>` (0700); same execution id again refused (area exists); `hermes:` instance and `caddy.service` denied by polkit; symlinked run file refused. Probe user and areas removed. |
| 7. Backup | `fleet-backup.sh` root-owned in `/home/fleet/runtime`; timer enabled (03:30 UTC). First run wrote `clickhouse` and `garage` under `/backup/fleet/<day>/`; no service restarts. Restore proof: ClickHouse probe table backed up and restored (1000 rows, sums match); Garage mirror of a 3 MB object matches by SHA-256. |

Not done yet: a Fleet service token for machine access to the store tunnel (needed in Phase 3), the macOS store (step 8), the Windows store (Q2), and coordinator wiring (item 5).

## Mac and Windows stores, and the cross-host proof, 2026-11-09

| Host | Store | Tunnel |
|---|---|---|
| Mac (`owner-mac`, user `kirs`) | Homebrew `garage` 2.5.0 under launchd `com.ufobe.fleet-garage`; data in `~/.local/share/fleet/garage`; zone `mac`, starting capacity 200 GB; bucket `fleet`, keys `fleet-coordinator` (rw) and `fleet-backup` (ro). 1 MiB put and get match. | `fleet-store-mac.ufobe.com`, launchd `com.ufobe.fleet-cloudflared` (`cloudflared` 2026.10.4, `--token-file`), 4 connections, unauthenticated 302. |
| Windows (`desktop`, WSL2 Ubuntu, user `alexk`) | `garage` 2.5.0 static binary (SHA-256 equals the binary in the image pinned on PDS18) as a systemd user unit in WSL; zone `windows`, starting capacity 60 GB; bucket and keys as above. | `fleet-store-windows.ufobe.com`, `cloudflared` 2026.10.7 user unit in WSL, 4 connections, unauthenticated 302. |

Cloudflare cache: the zone cache rule `ebb0f27ead614cc3af06a2531192bd6e` on ufobe.com sets `cache: false` for the hosts `fleet-store-pds18`, `fleet-store-mac` and `fleet-store-windows.ufobe.com`. Without it, Cloudflare served the first HEAD of a key with a cacheable extension (for example `.bin`) as a GET to the store, which broke the SigV4 signature (403), and could cache the object at the edge. Added 2026-11-10 with the owner's approval (docs/designs/fleet-stack.md). Checked the same day from PDS18 through all three tunnels: put, three HEADs and two presigned GETs of a `.bin` key all returned 200 with `cf-cache-status: DYNAMIC` (no 403, no HIT), and the test prefix was empty after the delete.

Access: each store app allows group `fleet-people` and, for machines, the service token `fleet-machines` (`non_identity`). The token is in `~/.config/fleet/cf-access-machine.env` (0600) on every host. Store keys for the coordinator role are in `/home/fleet/.config/fleet/stores/<host>.key` on PDS18 (0600). The Windows key was copied directly from WSL to PDS18. A Mac hook blocks relaying data between servers through the Mac.

Cross-host proof (PDS18 acting as coordinator, presigning for every store):

1. An 80 MiB object was uploaded from PDS18 to the Windows store through its tunnel, as a multipart upload in 3 parts of 32 MiB. It took 19.4 s.
2. The Mac, acting as the target executor, downloaded it from the Windows tunnel (2.3 s). It then uploaded the 3 parts to its own store on `127.0.0.1` with presigned part URLs (0.5 s).
3. PDS18 read the Mac copy through the Mac tunnel (5.8 s). The SHA-256 matches the original.

The test objects were deleted afterwards.

Findings to carry into Phase 3:

* Cloudflare Access removes the `CF-Access-Client-*` headers before the request reaches the origin. These headers must be added after S3 signing and must never be signed. Otherwise Garage returns `signed header cf-access-client-id is not present`.
* Control-plane calls to another host's store (create, complete or abort a multipart upload, `HEAD`, delete) go through that store's tunnel. Presigned part URLs can target the local `127.0.0.1` endpoint of the executor that uses them.
* Store credentials must reach the coordinator directly from the store's host. In the design this happens when the executor registers its store through the outbox.
* On this Mac, Python from python.org has no CA bundle, so use `curl` or the system trust for HTTPS.
* The Windows store runs only while the user is logged in: WSL is started by the `wsl-keepalive` logon task. Its real capacity is bounded by free space on `C:` (73 GB), because the WSL disk image lives there.

## Note: backups with FORCE row-level security, 2026-11-09

From migration 6, every Fleet table has `FORCE ROW LEVEL SECURITY`, so `pg_dump` run as the owner role `fleet` fails with `query would be affected by row-level security policy`. Run dumps as `postgres`, which bypasses row-level security: for example, `runuser -u postgres -- pg_dump -Fc -d fleet`. The nightly `tight-pg-backup` service already runs as `User=postgres` and is not affected. Pre-deploy dumps are in `/backup/fleet/pre-deploy/`.

## API key connections and fleet-gateway

- Key values: `/home/fleet/.config/fleet/secrets/connections/<connection>/<fingerprint>` (fleet, 0600), backed up encrypted by `fleet-backup.sh` (`backup-age.env`). The Tavily keys were moved there from Pipe on 2026-11-10 ([configuration](configuration.md#coordinator-settings-api-keys)).
- `fleet-gateway` runs as user units of `fleet` (`fleet-gateway.socket` on `127.0.0.1:8742`, `fleet-gateway.service`), binary `/home/fleet/runtime/fleet-gateway` from the CI `gateway-release` artifact, config `/home/fleet/.config/fleet/gateway.json`; the coordinator sets `FLEET_GATEWAY_DIR` and `FLEET_GATEWAY_URL`.
- Remote hosts call `https://fleet.ufobe.com/v1/call/...`: Cloudflare Access app "Fleet gateway" (path `/v1/call`) admits only the `fleet-machines` service token; Caddy forwards the path to the gateway.
- Pipe (its read lane, grants, units and public route) was deleted in stage C4 of [fleet-connections.md](designs/fleet-connections.md).

## Executor release files (L2)

The `executor-release` CI matrix publishes native Linux x86_64 and macOS arm64
binaries plus adjacent SHA-256 files, as separate Actions artifacts beside the
coordinator artifact. PR runs publish candidates for qualification; main push
runs supply manually selected host files. See [manual download and verification](executor-build.md#fetch-a-ci-release-manually-l2).
The matrix summary records build seconds and file bytes per target.
Retention is the same default as the coordinator artifact. Expiry removes the
ability to fetch that revision from that run; lasting publication remains stack
question 7. `deploy/build-release.sh` continues to pack the coordinator only:
executor files come from the native CI matrix, not the coordinator's build host.
No deployment or auto-update is added.
