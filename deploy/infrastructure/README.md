# Infrastructure definitions

## Overview

Host service definitions and administrative scripts for coordinator storage, origin access and backups. File presence describes shipped configuration, not what a live host has installed. The [deployment runbook](../README.md) owns installation order.

## Interface

- [fleet-postgres.sql](fleet-postgres.sql): psql setup for the `fleet` database owner and non-owner `fleet_app` runtime role; peer-authentication setup is documented in its header.
- [fleet-backup.sh](fleet-backup.sh): nightly ClickHouse backup, Garage mirror and optional age-encrypted secret archive; invoked by the backup timer.
- [agent-access-smoke.sh](agent-access-smoke.sh): site page and API access checks, including the `site-agents` identity.
- [fleet-project-publisher.socket](fleet-project-publisher.socket) owns the host Unix listener for [fleet-project-publisher.service](fleet-project-publisher.service), which mediates scoped GitHub App publication on PDS18.
- [fleet-browser-chrome.apparmor](fleet-browser-chrome.apparmor): the AppArmor profile that lets the pinned Chromium on PDS18 create its user-namespace sandbox.
- [agent-browser-open.sh](agent-browser-open.sh): opens the production origin in Orca using host-scoped access headers and checks identity; returns a browser page ID for later commands.
- [sse-smoke.sh](sse-smoke.sh): live SSE smoke against an explicitly selected test task, using the operator's Access session.
- [pds18-ci/](pds18-ci/README.md): isolated trusted-main CI guest setup and runner registration.
- PDS18 memory (owner decision 2026-11-26): [fleet-postgres-memory.conf](fleet-postgres-memory.conf) goes to `/etc/postgresql/18/main/conf.d/10-fleet-memory.conf` (`shared_buffers`, applied by a PostgreSQL restart); [postgresql-memory.conf](postgresql-memory.conf) to `/etc/systemd/system/postgresql@18-main.service.d/fleet-memory.conf` and [fleet-user-memory.conf](fleet-user-memory.conf) to `/etc/systemd/system/user-1005.slice.d/fleet-memory.conf` (`MemoryLow`, applied by `systemctl daemon-reload`). `MemoryLow` spares a cgroup from reclaim while others have reclaimable memory; it is no reservation and no cap.
- Service/socket, Quadlet, Caddy, tunnel and launch-helper definitions in this directory are deployment inputs; `.candidate` files remain proposed configuration.

## Invariants

- `fleet-postgres.sql` gives the runtime role no table ownership or RLS bypass; migrations use the owner role. Peer rules must be configured separately.
- `fleet-backup.sh` uses private host env files, sends ClickHouse credentials through stdin headers, and removes its temporary plaintext archive on exit. A repeated backup to the same day's existing ClickHouse destination fails; it is not an overwrite operation.
- `agent-browser-open.sh:call` removes Access credentials from the CLI child's environment. Its temporary header configuration is deleted after navigation; identity must be `site-agents` before success.
- `sse-smoke.sh` checks readiness, event arrival and pings; it requires an explicit test-task ID because it changes the task title.

## Side Effects

SQL creates roles/database and changes grants. Backup writes `/backup/fleet`, mirrors Garage data, optionally encrypts secrets and prunes older backup day directories. Access checks make network requests; the browser helper opens a tab which its caller must close. SSE smoke edits a test task. Applying service or network definitions is a separate administrative operation.

## Verification

Documentation-only work uses link validation and `git diff --check`. `python3 deploy/infrastructure/agent-browser-open-test.py` is the existing isolated browser-helper check. Live access/SSE checks require authorized site access and, for SSE, a disposable test task. Backups and service installation must not be run as documentation checks.


## Production project browser

Install the existing `ufobe-site-agents` Access pair once on the coordinator. In its configured `FLEET_SECRET_DIR`, create `project-browser/` owned by the coordinator user with mode `0700`. Place a coordinator-owned `site-agents.json` with mode `0600` there. Use the JSON keys `clientId` and `clientSecret`, with string values from the approved service-token store. Use a private input file and an atomic rename for rotation. Keep values out of command arguments, output and deployment artifacts. The coordinator reads the file on demand.

The standard native release updates the coordinator and executor. The host channel already carries `POST /api/hosts/*`, including `/api/hosts/browser/access`. The route returns credentials only to registered owner-held hosts named in the Fleet project's browser-enabled environments. Existing executor units and per-host preview grants need no change.

After landing, read the affected host's `problems` in `hosts_list` or `host_diagnose`. In a fresh Fleet coding run on that host, open the deployed site with project-browser, verify `/api/ops/me` returns `site-agents`, and close the browser. After verifying the new executor, remove obsolete production entries from its preview binding file. Retain any local credentials still used by standalone Orca or smoke scripts under `~/.config/ufobe-agents/`. At a custom path, retain the legacy binding’s `accessFile` entry for upload protection until the credential is moved to that protected directory or retired. A rolled-back executor uses its previous host-local browser configuration.

## Fleet publishing identity

The Fleet repository uses the private `taskqorg-fleet-publisher` GitHub App.
The installation is `186749078` for App `5963672`. GitHub must report exactly
`taskqorg/fleet` (`1450484391`) with Contents, Pull requests and Commit statuses
write and Checks and Metadata read. The design approval verifier has its own
App and credential. Existing CI diagnostics and CI rerun use their separate grant.

To qualify an isolation change on a host, use throwaway socket paths and
temporary units without credentials. Compare a service self-binding under
`PrivateTmp` in its runtime directory with a systemd `ListenStream` passed
to the same service. Use the same confined coordinator-equivalent client
for both. Record the client profile and connect result, the kernel audit
reason for any denial, and the successful connection's `SO_PEERCRED`
UID and GID. Remove the temporary units and sockets.

1. The operator provisions the matching App key as a host-encrypted
   systemd credential at `/etc/credstore.encrypted/fleet-project-publisher.github-app.pem`.
   Keep the encrypted file root-only. The service uses the signed release's
   root-owned executor binary and its own service UID. The
   service accepts systemd's root-owned `0440` file inside its exact read-only
   credential directory, or a service-owned `0400` file in that namespace.
2. Drain Fleet publishing runs and retain the verified previous signed
   release and its publisher unit for rollback. On upgrade from the self-binding
   service, run `systemctl disable --now fleet-project-publisher.service` before
   replacing its unit. Check that its old socket and service-owned runtime
   directory are gone. If the stopped service left an empty directory, remove
   it with `rmdir /run/fleet-project-publisher`. If other content remains,
   resolve ownership and the stale runtime state before proceeding.
3. With the matching signed release installed on `pds18-native`, install
   both shipped publisher units as system units. Run `systemctl daemon-reload`
   and `systemctl enable --now fleet-project-publisher.socket`. Check that
   systemd is listening and `stat` reports the directory as root-owned
   `0755` and the socket as root-owned `0666`. The first request starts
   the dedicated service and receives its own authorization check. The
   executor requires its existing repository grant for `taskqorg/fleet`.
4. Read the project publishing capability. It must name the App actor
   `taskqorg-fleet-publisher[bot]` and list only `pds18-native` as eligible for
   this repository. In an authorized coding execution, read MCP `capabilities`
   for App IDs, readiness, verification time and expiry.
5. From an authorized execution, run `project-publishing/status` for its
   managed branch. This exercises the coordinator's confined connection and
   operation authorization. Record the scoped provider response and check
   the kernel audit log for a corresponding AppArmor denial. A readiness
   response alone leaves the coordinator path unverified. For a separately
   authorized delivery, verify the PR author, live head, matching stored
   review status creator and merge-queue state.

For ordinary binary updates and key rotation, retain the active socket and
restart only `fleet-project-publisher.service` after the release switch or
atomic replacement of the encrypted credential. The socket unit retains
the endpoint and queues new connections while the service restarts.
Wait for a new installation verification timestamp and repeat the
confined status check. Save the nonsecret observations with the delivery.

For operator-approved rollback to the self-binding publisher:

1. Drain publishing. Disable and stop the socket unit. Its dependency
   stops the activated service, and systemd removes the socket pathname.
   Remove the empty activation directory if it remains.
2. Restore the prior service unit from the verified signed release and
   remove the activation socket unit. As root, copy that same release's
   `fleet-executor` to `/opt/fleet/publisher-rollback/fleet-executor`, a
   root-owned file with mode `0555` in a root-owned `0755` directory.
   This path is outside the executor updater's managed release tree.
3. Create the root-owned
   `/etc/systemd/system/fleet-project-publisher.service.d/publisher-rollback.conf`
   with this content:

   ```ini
   [Service]
   ExecStart=
   ExecStart=/opt/fleet/publisher-rollback/fleet-executor project-publisher
   ```

   Reload systemd, verify the effective `ExecStart` using
   `systemctl show -p ExecStart fleet-project-publisher.service`, then enable
   and start the service. Verify the running executable using the service's
   `MainPID` and `/proc/<MainPID>/exe` and repeat the confined status check.
   Rolling back to the self-binding service can restore the disconnected-path
   denial.
4. Keep the rollback drop-in and pinned binary for the life of the restored
   unit. To return to socket activation, repeat the cutover with a compatible
   signed release and both matching units. Remove the rollback drop-in after
   stopping the restored service and before reloading systemd. Verify confined
   status before removing the pinned rollback binary.

If readiness fails, use the reported reason to check the unit, App binding
and provider permissions. An installation failure blocks Fleet repository
publication. Restoring a prior legacy deployment requires a separate owner
decision. GitHub's installation token lasts up
to one hour, and the publisher reacquires it after ten minutes or upon the
next call after provider authentication rejection. GitHub rejects workflow-file
writes with the approved permissions. Such a change requires its own
authorized delivery path.
