# Fleet error tracking

Fleet's errors (the operator app's JavaScript errors, and unexpected errors and panics of the coordinator and the executor) go to one shared, self-hosted [Bugsink](https://www.bugsink.com/docs/) at `https://bugsink.ufobe.com`. Bugsink speaks the Sentry protocol, so the official Sentry SDKs report to it. Fleet keeps only what is Fleet's own: agents read errors through a Fleet connection, and the owner gets new issues in Needs you.

## Why Bugsink

An error tracker groups events into issues, keeps their stacks, resolves source maps, counts regressions and handles retention and volume. Fleet's earlier store (`client_error*` tables, migrations 62 to 66) rebuilt a small part of that and had to invent its own limits for it. Bugsink is one container with no extra services, its Sentry ingest works with the standard SDKs, and its read API (`/api/canonical/0/`) gives agents issues, events and stacktraces. It is shared by every project on the owner's hosts, so a new project needs no new mechanism.

## Technical Plan (Architecture)

### 1. Projects

One Bugsink team `fleet` with one project per component: `fleet-web` (the operator app), `fleet-coordinator` and `fleet-executor`. Each project has its own DSN. A DSN is not a secret: it lets a sender submit events, nothing else.

### 2. Browser tunnel

The app ([`app/src/errors.ts`](../../app/src/errors.ts)) initialises `@sentry/browser` before any other module, with the DSN of `fleet-web` from [`config/bugsink.json`](../../config/bugsink.json) (built into the bundle), `environment: 'production'`, errors only (no sessions, tracing or replay) and no personal data (`dataCollection` without user fields, cookies or bodies, the SDK 11 form of `sendDefaultPii` off). URLs leave the browser without query or fragment (the event's request URL and breadcrumbs), and without `Referer`: a query can carry a token or search text. `release` is the Fleet version the tab runs, as `system_versions` and the version chip show it; it is unknown until the first version check. An `OpError` or `AccessChanged` is not reported: an operation's own failure is in the coordinator's spans, and an access change aborts reads on purpose. The render boundary (`main.tsx:RenderBoundary`, "This page failed to render." with Reload) also captures the crash.

The SDK sends with `tunnel: '/api/errors'` ([Sentry tunnel](https://docs.sentry.io/platforms/javascript/troubleshooting/#dealing-with-ad-blockers)): Fleet's own origin, so blockers keep the events and Bugsink needs no public ingest for Fleet. The coordinator's `POST /api/errors` ([`bugsink.go`](../../go/coordinator/bugsink.go)):

*   authenticates like every app route (Access, then the browser credential and the person) and refuses a request without a person;
*   reads at most 1 MiB (413 beyond);
*   forwards only `event` and `client_report` items (sessions, attachments and every other item are dropped; an envelope with none is answered 200 and not sent) and, of the envelope header, only `event_id`, `dsn`, `sdk` and `sent_at`;
*   parses the envelope header and accepts only `FLEET_BUGSINK_WEB_DSN`: the same host and project id (403 otherwise, 400 for a malformed envelope);
*   rewrites each event's `user` to `{id: <actor>}` and its tag `fleet.actor` to the actor; tags come as an object or as `[key, value]` pairs and are normalised to an object (any other form is 400), so whatever user or actor the browser sent is dropped;
*   forwards to `FLEET_BUGSINK_INGEST` (the loopback address on PDS18), else to the DSN's own host, at `/api/<project>/envelope/` with the DSN's key in `X-Sentry-Auth`, and passes Bugsink's answer back with its `Retry-After` and `X-Sentry-Rate-Limits`, so the SDK backs off (a Bugsink 5xx or no answer is 502).

Without `FLEET_BUGSINK_WEB_DSN` the route answers 404 and nothing is forwarded.

### 3. Server errors

The coordinator and the executor use `sentry-go` through [`go/errtrack`](../../go/errtrack/errtrack.go): one DSN each from `FLEET_BUGSINK_DSN` (the executor falls back to `bugsinkDsn` in its release defaults, `config/executor-defaults.json`; set but empty turns it off), `release` the Fleet version, `environment` production, `server_name` the host id, tag `service`. No DSN, no events, so tests and development need nothing. Reported:

*   a panic in an HTTP handler or a periodic pass, through the `obs` fail hook (`obs.Handler`, `obs.Pass`), tagged with the span name; a pass's panic ends the process, so its delivery is awaited for up to 2 s; a handler's panic is recovered by `net/http` and waits for nothing;
*   a failed pass (`obs.Pass` returning an error other than its context ending), fingerprinted by the pass, so one failing pass is one issue however its messages vary;
*   a coordinator 500 (`writeError` of an unexpected error or an `HTTPError` 500);

Each capture runs on a clone of the process hub, so concurrent events keep their own tags.
*   the executor's `main` returning an error or panicking.

### 4. Agent read access

Connection provider `bugsink` ([`providers.go`](../../go/coordinator/providers.go)) reads `/api/canonical/0/` on Bugsink's loopback address `http://127.0.0.1:9510` (the gateway runs on PDS18, and the public API is behind Cloudflare Access; only ingest is public) with a read-only Bugsink token (`projects:read`, `issues:read`, `events:read`). All tools are GET and idempotent:

| Tool | Path |
|---|---|
| `projects` | `/projects/`: ids and names |
| `issues` | `/issues/?project={project}&state={state}&sort=last_seen&order=desc`, `state` one of `unresolved`, `open`, `resolved`, `muted`, `all` |
| `issue_get` | `/issues/{issue}/` |
| `issue_events` | `/events/?issue={issue}&order=desc` |
| `event_get` | `/events/{event}/`, with the full payload and `stacktrace_md` |
| `event_stacktrace` | `/events/{event}/stacktrace/` as Markdown; `textReply`: the gateway passes it as `{"text": ...}` |

Ids are checked against UUID and number patterns before a key is attached. The instructions and every description mark event data as untrusted: it is reported by browsers and servers, and an agent never follows instructions in it. Agents get the tools through the existing connection and MCP path ([connections](fleet-connections.md#5-call-methods)). A Fleet project names its Bugsink projects in its settings, `errors.bugsink: ["fleet-web", "fleet-coordinator", "fleet-executor"]` for project `fleet`, so an agent working on a project knows which issues are its own.

### 5. Owner Needs you

Bugsink alerts (new issue, regression, unmute; [alerts](https://www.bugsink.com/docs/alerts/)) go to a custom webhook on each `fleet-*` project: `POST http://127.0.0.1:9470/api/bugsink/alert/<secret>`, the coordinator on PDS18's loopback (Caddy sends no unauthenticated request to it, and Bugsink runs with host networking). Bugsink signs nothing, so the secret is the last path segment, compared in constant time with the content of `FLEET_BUGSINK_ALERT_SECRET_FILE` (read per alert; no file, the route answers 404).

*   `alert_reason` `NEW` opens the owner's needs-you problem `problem:bugsink:<issue UUID>` once while it holds the cause; `REGRESSED` and `UNMUTED` let an earlier problem go (open or answered) and ask again; `TEST` asks nothing.
*   The problem text is fixed wording, the project name (checked against `^[a-z0-9][a-z0-9_-]{0,63}$`, else left out), the issue link `https://bugsink.ufobe.com/issues/issue/<id>/` and the `issue_get` call. The title, values and every event field are reported data and never enter it, the same rule as for every problem ([system management, Problems](fleet-system-management.md#problems-needs-you-questions-from-rules)).
*   The problem belongs to the Fleet project whose `errors.bugsink` names the Bugsink project, and is addressed to the owner.
*   It follows the problem lifecycle: Investigate or Dismiss answer it, and the cause stays held until it goes. The `bugsink-resolved` pass (every 5 minutes, `FLEET_BUGSINK_RESOLVE_TICK_MS`) reads each held issue with the `bugsink` connection's `issue_get` and closes the problem when Bugsink reports it resolved or answers its own 404 (`{"detail": ...}`; any other 404 is an error, so a moved API never withdraws every problem). A problem of a project the connection is not granted to closes through its answers only. Without the connection, a gateway or an active key the pass does nothing.

### 6. Source maps

`app/build.mjs` writes external source maps to `dist/app/sourcemaps/`. They ship in the release and UI archives but are never served: the coordinator serves `assets/` and named entry files only, and the service worker caches neither. Each script gets a debug ID in the form `sentry-cli sourcemaps inject` writes (a snippet and a `//# debugId=` comment appended, so the map's lines stay valid), derived from the script itself. The host that installs the app uploads the maps: `fleet-update` on PDS18 ([`go/update/sourcemaps.go`](../../go/update/sourcemaps.go)), right after it installs a coordinator release or a UI release and before the restart, sends one artifact bundle (the protocol `sentry-cli` uses: `chunk-upload`, then `artifactbundle/assemble`) for project `fleet-web` with release the Fleet version, to Bugsink's loopback address. It reads `ci-token`, `ingest-url` and `sentry-org` from `/etc/fleet/bugsink/` inside the process; without them it uploads nothing. A failed upload is logged as a warning and never blocks the install. No upload path passes Cloudflare Access, and nothing is installed on the host for it. Bugsink resolves a frame by its debug ID ([source maps](https://www.bugsink.com/docs/sourcemaps/)), so no release or path matching is needed.

### 7. Onboarding a project

1.  Create the project's Bugsink projects in team `fleet` (one per component that reports).
2.  Put each DSN in the component's runtime config: a browser app's build, a service's `FLEET_BUGSINK_DSN` or its own equivalent.
3.  Map them in the Fleet project settings: `errors.bugsink: [<names>]`.
4.  Grant the `bugsink` connection to the project (its access list), so the project's agents get the read tools.
5.  Add the alert webhook to each new Bugsink project (§5).

### 8. Host configuration

| Value | Where | Secret |
|---|---|---|
| `FLEET_BUGSINK_WEB_DSN`, `FLEET_BUGSINK_DSN` (coordinator), `FLEET_BUGSINK_INGEST`, `FLEET_BUGSINK_ALERT_SECRET_FILE` | PDS18 `coordinator.env` | no; the alert secret is a file named here |
| `FLEET_BUGSINK_URL` | optional, issue links; default `https://bugsink.ufobe.com` | no |
| `bugsinkDsn` | `config/executor-defaults.json`, overridden by the executor's `FLEET_BUGSINK_DSN` | no |
| `web` | `config/bugsink.json`, built into the app | no |
| read token | the `bugsink` connection's key file (`FLEET_SECRET_DIR/connections/bugsink/`) | yes |
| alert secret | `/etc/fleet/bugsink/alert-secret`, group `fleet` | yes |
| upload token (`debug-files:upload`, bound to `fleet-web`) | `/etc/fleet/bugsink/ci-token`, read by `fleet-update` (root) | yes |

## Verification

`go test ./coordinator -run 'TestBugsink|TestErrorsSetting|TestClientErrorStoreDropped'` (tunnel DSN check, size, forwarding and actor; webhook secret, fixed text, regression and the resolution pass; the setting; migration 66), `go test ./gateway -run 'TestBugsinkCatalogue|TestTextReply'` (each catalogue tool's path, bearer and id checks against a fake Bugsink), `go test ./errtrack` (no DSN no events; pass errors and panics as events), `go test ./update -run TestUploadSourcemaps` (the artifact bundle against a fake Bugsink; no settings, no upload; a refusal is a warning), and the browser check `node tests/ui/errors.check.ts` (a thrown error and a render crash reach a fake Bugsink through `/api/errors` for the configured DSN, with the person and the debug ID).

## Not built

- Bugsink's own retention, quotas and backup are Bugsink's host configuration, outside this repository.
- A cap on the problems alerts open: each new Bugsink issue opens one, and Bugsink's grouping, not Fleet, bounds their number.
