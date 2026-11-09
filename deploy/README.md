# deploy

Service files and scripts for running Fleet. Nothing here is applied automatically.

* `infrastructure/`: PDS18 units following tight: the coordinator service, ClickHouse and Garage Quadlets, Postgres role setup (`fleet-postgres.sql`), backups, the root-owned `fleet-launch@.service` helper, the Caddy and Cloudflare tunnel candidates, and `origin_guard.py` (Access JWT check that adds the Bearer) with its test. Files ending in `.candidate` are proposed, not installed.
* `hosts/macos/`: launchd files for a macOS host (Garage, cloudflared).

Design: [data foundation](../docs/designs/fleet-data-foundation.md) Phase 0 and §10.
