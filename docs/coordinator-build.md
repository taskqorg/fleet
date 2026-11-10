# Coordinator build and operation

The coordinator is one Go binary, `fleet-coordinator`, built from `go/coordinator`. It serves the HTTP API, the SSE stream and the built app, and stores everything in Postgres.

## Build

- `pnpm run go:build` writes `go/bin/fleet-coordinator`, `fleet-executor`, `fleet-gateway` and `fleet-update`. Generated Go types (`go/contract`) and `go/coordinator/schema.sql` are committed; after a change to `contracts/*.schema.json` run `cd go && go generate ./...` and commit the result (CI fails on drift).
- `pnpm run app:build` type-checks and bundles the app into `dist/app`, which the coordinator serves; `pnpm run build` runs both.
- `deploy/build-release.sh` builds the release tarball with all three binaries; [deploy/README.md](../deploy/README.md) owns release and install.

## Run

`pnpm start` runs `go/bin/fleet-coordinator`. It applies the embedded schema with the migration role, opens the store, loads the people file, starts its background loops and listens on `127.0.0.1:$PORT`. Open http://127.0.0.1:8787. Every browser request carries `Authorization: Bearer <FLEET_BROWSER_SECRET>` and `X-Fleet-Person: <email>` of a listed person; host requests carry only their own host secret (hosts register their secrets, [configuration.md](configuration.md) Hosts). The machine credential never reaches the browser.

| Variable | Default | Meaning |
|---|---|---|
| `FLEET_DATABASE_URL` | required | Postgres URL of the application role `fleet_app`. |
| `FLEET_MIGRATION_DATABASE_URL` | `FLEET_DATABASE_URL` | Owner role that applies the schema on start. |
| `FLEET_PEOPLE_FILE` | required | `people.json`, outside Git; exactly one owner. Read once at start. |
| `FLEET_BROWSER_SECRET` | empty: every browser request is 401 | Browser bearer. |
| `FLEET_HOST_ID` | `owner-mac` | Default executor host. |
| `PORT` | `8787` | Loopback port (PDS18 uses 9470). |
| `FLEET_APP_DIR` | `dist/app` | Built app. |
| `FLEET_PROJECT_CONFIG_DIRECTORY` | `config` | Project settings. |
| `FLEET_PROFILES_DIRECTORY` | `config/agents` | Agent profiles. |
| `FLEET_LAB_DIRECTORY` | `config/lab` | Lab configuration. |
| `FLEET_DEFAULT_ACCOUNT_BINDING` | empty | Account binding used for Codex when a request names none. |
| `FLEET_DEFAULT_ARTIFACT_LOCATIONS` | `pds18` | Artifact locations when a project names none. |
| `FLEET_STORE_SECRETS` | `var/stores` | Artifact store credentials. |
| `FLEET_STORE_ENDPOINT_HOSTS` | empty | Store endpoint hosts (exact, or a `.suffix`) reached through the Access tunnel with the service token. |
| `FLEET_ARTIFACT_RETRY_WINDOW_MS` | `86400000` | Upper bound of the backoff between attempts of a failed artifact copy. |
| `FLEET_S3_IDLE_TIMEOUT_MS` | `60000` | Idle timeout of one store request. |
| `FLEET_SECRET_DIR` | empty | Connection key values, `connections/<connection>/<fingerprint>`, mode 0600. |
| `FLEET_GATEWAY_DIR` | empty | Where the gateway snapshot (`snapshot.json`, no key values) is published. |
| `FLEET_GATEWAY_URL` | empty | Gateway base URL. |
| `FLEET_CF_ACCESS_FILE` | `~/.config/fleet/cf-access-machine.env` | Cloudflare Access service token for machine calls. |
| `FLEET_CONNECTION_TICK_MS`, `FLEET_PIPELINE_TICK_MS`, `FLEET_WATCH_TICK_MS`, `FLEET_RECONCILE_MS`, `FLEET_STREAM_PING_MS` | 3000, 5000, 15000, 600000, 25000 | Loop periods; test seams. `FLEET_CONNECTION_TICK_MS=0` turns the connection loop off. |
| `FLEET_TEST_SITE_ROOT` | empty: route answers 404 | Example sites served under `/test-sites/` ([website-example.md](website-example.md)). |

## HTTP and storage

Routes are registered in `go/coordinator/registry.go` (the one router) from the domain files; `main.go` holds the credential check. `GET /api/health` is public and shows `commit`, `built` and `database`. `GET /api/stream` is the SSE change stream. A request runs in one transaction: a read snapshot for GET, a write otherwise, scoped to the person's projects by row-level security (a write outside scope answers 403).

The schema is `go/coordinator/schema.sql` (embedded in the binary); [fleet-data-foundation.md](designs/fleet-data-foundation.md) is its specification.

## Tests

`tests/go/run.sh` builds nothing; build first, then it runs `tests/trial` and `tests/go` against `go/bin/fleet-coordinator` on a fresh local Postgres database per file. Go unit tests: `cd go && go test ./...`.

## Deployment

[deploy/README.md](../deploy/README.md) owns units, environment files and Caddy. The Access origin guard (`fleet-gateway guard`, port 9471) validates Access and adds the browser bearer; the coordinator does not duplicate it and trusts no identity header without that bearer.
