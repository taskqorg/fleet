# Infrastructure definitions

## Overview

Host service definitions and administrative scripts for coordinator storage, origin access and backups. File presence describes shipped configuration, not what a live host has installed. The [deployment runbook](../README.md) owns installation order.

## Interface

- [fleet-postgres.sql](fleet-postgres.sql): psql setup for the `fleet` database owner and non-owner `fleet_app` runtime role; peer-authentication setup is documented in its header.
- [fleet-backup.sh](fleet-backup.sh): nightly ClickHouse backup, Garage mirror and optional age-encrypted secret archive; invoked by the backup timer.
- [agent-access-smoke.sh](agent-access-smoke.sh): site page and API access checks, including the `site-agents` identity.
- [agent-browser-open.sh](agent-browser-open.sh): opens the production origin in Orca using host-scoped access headers and checks identity; returns a browser page ID for later commands.
- [sse-smoke.sh](sse-smoke.sh): live SSE smoke against an explicitly selected test task, using the operator's Access session.
- [pds18-ci/](pds18-ci/README.md): isolated trusted-main CI guest setup and runner registration.
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
