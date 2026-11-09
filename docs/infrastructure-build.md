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
