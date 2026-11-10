# Fleet operator app

* **Author:** Alex Kirs
* **Status:** Proposed (EGM draft; review applied, see [reviews/fleet-operator-app](reviews/fleet-operator-app/consolidated_review_report.md))
* **Date:** 2026-11-10

## Overview

The operator app is the web interface operators use every day. It replaces the prototype pages that the coordinator serves today as inline HTML strings. It is built only from `packages/ui`: the approved design book, its controls and patterns, and the [owner rules](../design-system/owner-rules.md) OR-1 to OR-22.

The app is a single-page Preact application (design-system D4: Preact, Signals, Wouter, esbuild). The coordinator serves it at `fleet.ufobe.com`, behind Cloudflare Access and the access guard. It reads and writes only through coordinator operations (`/api/ops/:name`).

* Live updates come from a server-sent change feed (§4).
* The last-read data is kept for offline reading (§5).
* Each main view can be installed as its own PWA window.

The first slice covers four views: Tasks (list and detail), Conversations (which replaces today's `/` session page), Overview and Hosts. Results waits for Phase 3 (artifacts) of the [data foundation](fleet-data-foundation.md).

**Terms used here:**
* **R2:** [fleet.md principle](../../fleet.md#principles): interfaces add no operations of their own.
* **Proxy mode:** the coordinator flag `browserProxyAuth`. In this mode the guard injects the browser Bearer token.
* **GLOBAL write:** the coordinator's single-writer scope lock ([data foundation §12](fleet-data-foundation.md#12-document-store-concurrency-model)).
* **Session** is the stored entity; **Conversations** is the view that shows sessions.

## The Problem

* **The prototype pages cannot carry daily operator work.**
  * They are inline Russian-language HTML strings in `ui.ts`, `project-ui.ts`, `lab-ui.ts` and `account-ui.ts`.
  * They poll whole objects every few seconds and rebuild the page, so the scroll position jumps (principles §3).
  * Lists are plain, with no filters or virtual scroll (OR-9, OR-11).
  * Raw JSON is shown instead of controls (OR-13). See [evidence/projects-desktop.png](../../evidence/projects-desktop.png).
* **The design system exists only as a book and static mockups.** Nothing runs it against real data yet.
* **There is no push channel.** The coordinator has none, so the "update in place, never jump" rules (OR-6, [principles §3](../design-system/principles.md#3-lists-and-scrolling)) cannot be met.
* **Operators want separate windows**, for example Tasks next to Conversations, each installable like an app. A native desktop app is not wanted (owner decision).

## Goals

Each goal is measured as described in §9. The numbers are targets that are reported, not gates (design-system D6).

* **G1. Daily work happens in the new app.** Operators do their daily work for Tasks, Conversations, Overview and Hosts without `/legacy/*`:
  * Tasks: list, detail, answer questions, accept results, pause, continue, cancel;
  * Conversations: read, send, steer, stop, fork, hand off;
  * Overview: needs-you, workers per host, projects;
  * Hosts: health and workers.
* **G2. Lists stay live without moving.**
  * A committed change reaches the screen within 1 s on a local or LAN connection. The `app.check.mjs` change-to-screen check measures this.
  * The first visible row keeps its position when rows arrive. The `tests/ui/scroll.check.mjs` method applies, run against app routes.
  * After each applied membership batch, an open list holds exactly the ids the server's filter returns.
* **G3. Offline reopening shows the last data, honestly labelled.**
  * A reopened view shows its last-read data at once, with its age.
  * It never presents that data as current.
  * It never queues writes.
* **G4. Each main view is its own installable window.** `/tasks`, `/conversations`, `/overview` and `/hosts` each install as a separate PWA in Chromium-based browsers (Chrome, Edge). Safari installs one app per origin, which is a documented limit.
* **G5. No app-only operations.** Every read and action the app uses is an existing or new coordinator op that agents can call too (R2). The only non-op route is `/api/stream`, a transport for the same change log that `changes_since` reads.
* **G6. Performance is measured and reported.**
  * First-load JavaScript is reported against 200 KB raw.
  * Click-to-detail is reported against 100 ms.
  * Change-to-screen is reported against 1 s.
* **G7. The old pages keep working until replaced.** They stay under `/legacy/*` until each one is replaced.

## Non-Goals

* **Out of this slice:** Results, artifact viewers and shares (Phase 3), and the architect workspaces.
* **Retry:** it needs a retry operation that does not exist yet, so it comes in a later slice.
* **Per-person identity, project grants and shares** ([data foundation §10](fleet-data-foundation.md#10-people-and-access), Phase 6). The first slice runs as the single owner with operator scope `*`, which is exactly what the guard allows today. The interfaces keep a scope parameter so that Phase 6 can plug in later.
* **A UI on hosts.** Hosts run only executors.
* **Offline writes, background sync, push notifications, and a native desktop app.**
* **Visual regression tests.**

## Open Questions

* **Phone navigation** (design-system open question (d)). The app keeps the interim icon rail until this is decided.

## Decisions (owner, 2026-11-10)

| # | Question | Choice |
|---|---|---|
| D1 | Live updates | SSE change feed with a resume cursor, plus cursor polling (`changes_since`) as the catch-up and fallback path. Both share one versioned cursor. A full refetch happens only when the cursor is older than the retained log. The heartbeat stays under Cloudflare's 100 s idle timeout. |
| D2 | Where the app lives | The app owns all of `fleet.ufobe.com`. The old pages move to `/legacy/*` and are deleted one by one. Hosts never get a UI. Each main view has its own manifest. First slice: Tasks, Conversations, Overview and Hosts; Results waits for Phase 3. |
| D3 | Offline | The service worker caches the shell. IndexedDB keeps the last-read data, read-only, with its age shown. No queued writes. The store is cleared on sign-out and on a grant change, and holds only what the grants allow. |
| D4 | Client data layer | One normalized entity store on Preact Signals, in our own code. The client never decides list membership: `list_membership` decides, in one batched read. Membership, ordering and the anchor are covered by tests. Implemented by `app/src/store.ts` ([fleet-view-urls-and-list-path.md §2](fleet-view-urls-and-list-path.md)); the first version kept lists per component, which reset them on every filter change. |
| D5 | Change-feed source | A change-log table written by triggers on every watched table, including `document`. The cursor is `seq`. Reads run under RLS. LISTEN/NOTIFY wakes the stream. Retention is an operator setting with no built-in cap. An old cursor gets `resync`. |

These are settled elsewhere:
* **Auth:** data foundation §10. Cloudflare Access sits in front of the guard, which verifies the JWT and injects the Bearer token. The browser holds no secret.
* **Stack:** design-system D4.
* **Checks only warn:** design-system D6.

## Critical User Journeys (CUJs)

### CUJ 1: Answer an acceptance question without losing the list

* **Given:** `/tasks` is grouped by priority and scrolled to row 300. A reviewer asks for acceptance of T-9989.
* **When:** the question commits.
* **Then:**
  * Within 1 s, T-9989's state icon changes in place and the needs-you counter rises.
  * Row 300 keeps its screen position. If T-9989 moves into a group above the viewport, the "N new" pill appears instead of a jump.
  * The operator opens T-9989. At 1100 px or narrower, the detail comes first and takes focus.
  * Pressing `a` calls `questions_answer`, and the block collapses to "Accepted · now".

### CUJ 2: Two windows, one state

* **Given:** the installed `/tasks` and `/conversations` windows are both open.
* **When:** the operator sends a message, which starts an execution for a task.
* **Then:**
  * The Conversations window shows the new transcript entries. They arrive as `event` changes for that session (kind map, §4).
  * The Tasks window shows the harness mark on the task row. The execution change carries `task_id`.
  * Neither window reloads or loses its scroll position.

### CUJ 3: Reopen offline

* **Given:** the operator read `/tasks` yesterday. The laptop is offline.
* **When:** the Tasks window opens.
* **Then:**
  * Data shows at once with the marker "offline · 18h". The accessible name reads "offline, last updated 18 hours ago".
  * Accept and Pause are disabled, with the reason "offline".
  * When the network returns, the feed connects at the server head and every list still showing stored rows or a failed load reloads its first page ([fleet-view-urls-and-list-path.md §2](fleet-view-urls-and-list-path.md); stored cursors are no longer used).

### CUJ 4: Coordinator restart

* **Given:** three windows have open streams.
* **When:** the coordinator restarts.
* **Then:**
  * Each stream reconnects with backoff and resumes from its cursor.
  * Cursors stay valid because the log is in Postgres, unless they are older than the retention floor, in which case the app resyncs.
  * No window reloads or loses its scroll position.

### CUJ 5: A host goes quiet

* **Given:** `/hosts` is open, and the mac executor stops polling.
* **When:** 30 s pass after its last poll.
* **Then:**
  * The `/api/hosts/poll` handler writes no new `host-observation` because no poll arrives. The client decides staleness from the host's `lastSeenAt` and the threshold that `hosts_list` returns (30 s). The client re-evaluates it every 5 s, because no write happens when a host stops polling.
  * The row shows the unconfirmed state overlay ([state badge](../design-system/controls/state-badge.md)) with "seen 30s", growing over time.
  * Worker counts become lower bounds (`≥ 1/2`).
  * The Overview shows the same values.

## Technical Plan (Architecture)

### 1. Shape

```
Browser windows (one origin)                          Coordinator (Fastify)
┌──────────────────────────────┐  ┌──────────────┐  ┌──────────────────────────────────────────┐
│ App: views, entity store,    │─▶│ Cloudflare   │─▶│ /api/ops/:name   ops (store.read / tx)     │
│ IndexedDB mirror,            │  │ Access +     │  │ /api/stream      SSE (own short reads)     │
│ service worker (shell only)  │◀─│ origin_guard │◀─│ me (op)          fixed owner (this slice)  │
└──────────────────────────────┘  │ (JWT check,  │  │ /, /tasks, ...   index.html; /assets/* ... │
                                  │ Bearer added)│  │ /legacy/*        old pages                 │
                                  └──────────────┘  └───────────────────┬──────────────────────┘
                                                                        │ statement triggers
                                Postgres: task, task_dependency, execution, execution_totals, question, document
                                          -> change(seq, xid, kind, id, task_id, session_id, host_id, project_id, op)
```

### 2. Routing and the app shell

* **Routing.** Wouter uses browser history.
  * `/` redirects to `/overview`.
  * View routes: `/overview`, `/tasks`, `/tasks/:id`, `/conversations`, `/conversations/:id`, `/hosts`, `/hosts/:id` (host detail as stacked sections, design-system D11 C3).
  * The coordinator returns `index.html` for these paths only. Any other unknown non-API path gets a 404 page.
* **List state** (filters, grouping, search, presets, the focused row and checked rows) lives in the URL in one canonical form, defined in [fleet-view-urls-and-list-path.md §1](fleet-view-urls-and-list-path.md) and implemented by `packages/view-url`; agents build and read such links with `view_link`. Per-person defaults and nav pins live in IndexedDB until Phase 6 adds people.
* **Shell.** `AppShell` from `packages/ui`, with nav items built from the registry's `workspaces`.

* **Codes.** Rows and details show the human code after the title through the `packages/ui` formatter (design-system D15). The UUID is only in a tooltip and "copy UUID". Search accepts a code, an alias or a UUID; a code goes through `entity.resolve`, and an alias opens the entity under its current code. Detail URLs keep the UUID (`/tasks/:id`) until codes exist; the view URL codec passes a code through unchanged, and the redirect direction for codes is decided by the D17 slice ([fleet-view-urls-and-list-path.md](fleet-view-urls-and-list-path.md), Non-Goals).

### 3. Data loading and the entity store (D4)

* **Rows.**
  * Rows are kept by `(kind, id)`. The kinds are `task`, `execution`, `question`, `session`, `host` and `event` (per open conversation).
  * Each row carries the `seq` it was read at. A change with a lower or equal `seq` than the row's is ignored.
* **Lists.** A list is `{ ids, keys, counts, total, groups, nextAfter, at }` under its canonical list key, plus the change cursor it reflects ([fleet-view-urls-and-list-path.md §2](fleet-view-urls-and-list-path.md)). `counts` holds per-state counts from the server.
* **Reads.**
  * Existing ops: `task_get`, `questions_list`, `executions_list` and `session_get`.
  * Extended op: `task_list`.
  * New ops (§6): `sessions_list`, `hosts_list`, `list_membership`, `changes_since` and `me`.
* **Applying changes.** Changes are applied in batches, one batch per animation frame:
  1. Map each change to the affected client rows and lists through the kind map (§4).
  2. Make one `list_membership` call for all open lists with the affected ids. It returns, for each list, the matching rows themselves, each with its sort key, plus fresh `counts`.
  3. Re-read affected rows that are open in a detail but not in any list through their existing get op (`task_get`, `session_get`).
  4. Insert, remove and move ids through `VirtualList`, which keeps the anchor in place and shows the "N new" pill.
* **Counters.** Counters (Σ, state counters, nav badges) come only from server `counts`, never from counting loaded rows. See the [list-view server note](../design-system/patterns/list-view.md).
* **Identity.**
  * The `me` op is read at start, on every stream reconnect, and when the tab regains focus.
  * If `grantsVersion` differs from the stored one, the app deletes IndexedDB (D3).
  * In this slice `grantsVersion` is the constant `"1"`.

### 4. Live updates (D1, D5)

**Change table.** Columns:
* `change(seq bigserial, xid xid8 DEFAULT pg_current_xact_id(), kind text, id text, task_id text, session_id text, host_id text, project_id text, op char(1), at timestamptz DEFAULT now())`.
* RLS: the same `project_scope` policy as the other tables. A row with a null `project_id` is visible only to scope `*`, as for every table today.

**Triggers.** One trigger function, defined on one line because the migration splitter splits on `;` at end of line (`store.ts:117`).
* It runs `AFTER INSERT OR UPDATE OR DELETE ... FOR EACH ROW`.
* It takes `r := CASE TG_OP WHEN 'DELETE' THEN OLD ELSE NEW END` and inserts one change row.
* It calls `pg_notify('fleet_change','')`. The payload is constant, so Postgres folds duplicate notifies within one transaction: one wake per commit, not per row.

**Kind map.** Written by the trigger; maintained in one server-owned table that is covered by tests.

| Source | `kind` | `id` | Parent ids filled | Client effect |
|---|---|---|---|---|
| `task` | `task` (`legacy_kind` if set) | `id` | — | Row `task`; lists of tasks; counts |
| `task_dependency` | `task` | `task_id` | `task_id` | Row `task` |
| `execution` | `execution` (`legacy_kind` if set) | `id` | `task_id`, `session_id`, `host_id` | Rows `execution`; the parent `task` (harness mark), `session` and `host` (counts) |
| `execution_totals` | `execution` | `execution_id` | — | Row `execution` (totals) |
| `question` | `question` | `id` | `task_id`, `session_id` from `data` | Rows `question`; the parent task (state, needs-you) and session |
| `document` kind `session` | `session` | `id` | `session_id` = `id` | Row `session`; lists of sessions |
| `document` kind `event`, `message` | `event` | `id` | `session_id` from `data->>'sessionId'` | The open conversation fetches `/api/sessions/:id/events?after=` |
| `document` kind `host-observation` (written by the `/api/hosts/poll` handler on every poll) | `host` | `id` | `host_id` = `id` | Row `host`. Updates where only `lastSeenAt` changed are skipped (`OLD.data - 'lastSeenAt' = NEW.data - 'lastSeenAt'`), so polls do not flood the feed; `/hosts` and the Overview refresh `lastSeenAt` by re-reading `hosts_list` on their 5 s staleness timer. |
| Other `document` kinds | — | — | — | No change row. A kind is added to this map when a view needs it, which keeps the log small. |

**Visibility watermark.** This replaces any fixed overlap window. Execution-scope ingest commits in parallel with GLOBAL writers, so `seq` order is not commit order. Every read of the log therefore returns only rows whose transaction is older than the oldest transaction still running:

```
SELECT ... FROM change WHERE seq > $cursor AND xid < pg_snapshot_xmin(pg_current_snapshot()) ORDER BY seq
```

* The new cursor is the highest `seq` returned from that contiguous visible prefix. No row is skipped, because a row from a transaction that is still open blocks the cursor until it commits or rolls back. A rolled-back transaction leaves no rows.
* The cost: one long write transaction delays the feed by its own duration. Feed delay is shown in the check report.

**Stream.**
* `GET /api/stream?cursor=N` is excluded from the GET read-snapshot wrapper (`longRoutes`, `app.ts:31-36`).
* On connect it sends the visible backlog in its own short `store.read`.
* After that, it keeps one shared `LISTEN fleet_change` per process. Each wake is debounced by 50 ms and triggers one short `store.read` per open stream: `seq > cursor`, under the watermark.
* Each event is sent as `id: <seq>`, then `data: {kind,id,taskId,sessionId,hostId,op}`.
* A `: ping` comment every 25 s keeps the connection open.
* Headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache, no-transform`, `X-Accel-Buffering: no`. The stream is not compressed.
* The client reconnects manually with backoff and always passes `?cursor=`. `Last-Event-ID` is not used.
* In this slice the scope is the process scope `*`. Phase 6 adds `SET LOCAL fleet.project_ids` per read and per stream.

**Catch-up and fallback.**
* `changes_since {cursor, limit}` is the same read in request-response form.
* The client uses it to catch up after a reconnect of the stream (from its in-memory cursor).
* It also uses it when SSE fails three times in a row, polling every 3 s until the stream comes back.

**Retention and resync.**
* Retention comes from the coordinator config `changeLogRetentionDays`, set by `FLEET_CHANGE_LOG_RETENTION_DAYS`. The proposed default is 7. There is no cap; the operator chooses the value. The `me` op reports it.
* A batch prune runs every hour in its own lock scope, `change:prune`, not GLOBAL. It deletes up to 10,000 rows per transaction in `seq` order and raises the floor `change_log_floor(seq)`.
* The server sends `resync` when the cursor is below the floor or above the current maximum, for example after a database restore. The client then marks its lists stale and re-reads them, keeping each anchor by id.

### 5. Offline, sign-in state and the PWA shell (D2, D3)

* **Service worker.** `sw.js`, scope `/`.
  * It precaches the hashed assets and `index.html`.
  * App routes are network-first with a cached fallback.
  * It never caches `/api/*`.
  * A new build shows an "Update available" notice that reloads the page.
* **IndexedDB mirror.**
  * It stores rows and lists of the open views with their `seq`, debounced to 1 s.
  * A reopened view renders from the mirror with the [offline marker](../design-system/controls/offline-marker.md). The marker has three states:
    * "offline · 18h" when there is no network;
    * "updating · 18h" when online but not yet caught up;
    * hidden once current.
* **Signed out.**
  * An API call that returns a redirect (opaque or 3xx), 401 or 403 is the "signed out" state, not offline.
  * The app shows "Session expired. Sign in again" with a link to reload through Access. It disables actions and deletes IndexedDB.
  * An explicit sign-out link (`/cdn-cgi/access/logout`) deletes IndexedDB before it navigates.
* **Manifests.**
  * There is one manifest per view: `/manifests/<view>.webmanifest`, each with its own `id`, `name`, `start_url`, `scope` and icon.
  * `index.html` picks the manifest by path.

### 6. Coordinator changes

* **Guard rule** (`app.ts:43`). A request skips the Bearer check only when its path does not start with `/api/` and either proxy mode is on or the path is outside `/legacy/`. This keeps today's rule: `/` was always public, and the legacy pages are public only in proxy mode. Every `/api/*` request, including `/api/stream`, keeps the Bearer and Origin checks. `origin_guard.py` is unchanged: it guards the whole site.
* **Read ops.** These are added to `READ_OPERATIONS` (`store.ts:143`) and served as MCP tools too:
  * `changes_since {cursor, limit≤1000}` returns `{changes, cursor, resync?}`.
  * `list_membership {lists:[{name:'tasks'|'sessions', filter, group?}], ids}` returns `{lists:[{name, rows:[{row, sortKey}], counts, groups?}]}` (`groups` for grouped task lists). Hosts are few, so `/hosts` re-reads `hosts_list` on any `host` or `execution` change instead.
  * `view_link {view} | {url}` returns `{url, path}` or `{view}`: the canonical operator-app link for a view, or the view of a link; 400 for an invalid view, a foreign origin or a path that is not a view. Also in the agent operation table, so agent MCP endpoints offer it ([fleet-view-urls-and-list-path.md §1](fleet-view-urls-and-list-path.md)).
  * `task_derived_check {}` returns `{checked, drifted}`: tasks whose stored derived values (`task_derived`) differ from a fresh computation, limited to the caller's projects.
  * `me {}` returns `{email: config.ownerEmail, roles:['operator','architect'], grantsVersion:'1', changeLogRetentionDays}`.
  * `sessions_list {filter, limit, after}` returns `{sessions, nextAfter, counts}`. Each session row has id, title, state, agent, last activity and open questions.
  * `hosts_list {}` returns `{hosts, staleAfterMs: 30000}`. The host ids are the configured ids (`config.hostId`, `integrationHostId`, `publicationHostId`, `nativeHostIds`) plus every `host-observation`. Each host row has `{id, platform, lastSeenAt, runningByHarness, slots, queued, stores: 'unavailable'}`. This is the first part of data foundation §11 `hosts.list`, with the same shape, extended in Phase 3.
  * `task_list` is extended with:
    * `{filter:{projectId?, state?: state[], search?}, group?: 'priority'|'state'|'project'|'agent'|'host'|'none', sort?: 'priority'|'created'|'updated', limit?: 1..500 (default 100), after?: string}`;
    * a response of `{tasks, nextAfter, counts:{<state>:n}}`;
    * `after`, an opaque keyset cursor `<sortValue>|<id>`, with ties broken by id.
* **Routes.**
  * `GET /api/stream`.
  * `/assets/*` (immutable), `/sw.js`, `/manifests/*` and the app routes, which serve `index.html` (`no-cache`).
  * `/` redirects to `/overview`.
  * The old pages move to `/legacy` (from `/`), `/legacy/lab`, `/legacy/projects`, `/legacy/recovery` and `/legacy/accounts`. The old paths `/lab`, `/projects`, `/recovery` and `/accounts` redirect (301) to their `/legacy/*` paths.

### 7. Build and serving

* `npm run build` runs `tsc`, then the asset copy, then `node app/build.mjs`.
* `app/build.mjs` runs esbuild in ESM mode with code splitting per view, minified, with hashed names, and writes to `dist/app`. It also writes `index.html`, `sw.js` (with the precache list) and the manifests, and prints chunk sizes (raw and gzip).
* `app/tsconfig.json` type-checks `app/` and `packages/ui` with Preact JSX.
* Fastify serves `config.appDir`, which defaults to `dist/app`, with `@fastify/static` (pinned to an exact 8.x version, for Fastify 5).
* For development, `npm run app:dev` runs esbuild watch into `dist/app`.

### 8. Coexistence and retirement

| Old page | Replaced by | Deleted when |
|---|---|---|
| `/` session page, now at `/legacy` | `/conversations/:id` | Conversations covers send, steer, stop, fork, hand off, switch profile and the results links. |
| `/projects` | Tasks, then a Projects view | A Projects view exists. |
| `/lab` | Laboratory view | The Laboratory slice exists. |
| `/recovery` | Overview problems | Its actions exist as problem rows. |
| `/accounts` | Overview accounts, then Access | The Access slice exists. |

* Each legacy page gets a first line: "Legacy view. New: <link>". Its inline links are rewritten to `/legacy/...`.
* Deleting a legacy page is a normal pull request that removes the file, the route and its tests.

### 9. Measurement

* **First-load JS.** `app/build.mjs` reports the size per chunk, and `check:design` prints it against 200 KB raw.
* **Running the app for checks.** `tests/ui/app.check.mjs` starts:
  * the coordinator on PGlite, with fixtures;
  * a local forward proxy that injects the Bearer token, the same way the guard does in production.

  It then runs the trace, UX, click-to-detail (median of 5) and change-to-screen checks against the real routes. Change-to-screen means a `task_update` commit until the row's icon changes, reporting the median and the worst case. `check:design` runs it and prints the results, as warnings only.
* **SSE through the real chain.** `deploy/infrastructure/sse-smoke.sh` opens `/api/stream` through `fleet.ufobe.com`. It confirms a ping within 30 s and an event within 2 s of a test write. It is run manually after a deploy and is not part of this slice's PR.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| The feed skips a change committed out of `seq` order. | The xid watermark (§4) lets the cursor advance only past finished transactions. A test commits two overlapping transactions in reverse order. |
| Event ingest floods the stream. | A constant notify payload (one wake per commit), a 50 ms debounce, one short read per stream per wake, and a test with a burst of 1,000 events. |
| The stream holds a snapshot or a pool connection. | It is excluded from the GET wrapper, and every read is its own short `store.read`. A test commits after the stream opens. |
| The prune blocks writers. | Batched deletes in their own lock scope `change:prune`, plus a floor marker. |
| An expired Access session looks like offline. | A separate "signed out" state, triggered by a redirect, 401 or 403. |
| A proxy buffers SSE. | No-transform and no-buffering headers, no compression, a smoke check, and the `changes_since` polling fallback. |
| A stale app shell after a deploy. | Network-first `index.html`, hashed assets, and an "Update available" notice. |
| Data left on a shared or lost laptop. | IndexedDB is deleted on sign-out, on session expiry and on a `grantsVersion` change. It is not encrypted at rest; device encryption is assumed. |
| Client and server disagree about a list. | The client never decides membership (D4). A property test compares `list_membership` with `task_list`. |

## Alternatives Considered

### Whole-list polling (rejected, D1)
* **How it works:** re-run the list ops every few seconds and compare the results on the client.
* **Rejected:** the cost grows with list size, and anchor keeping depends on client-side comparison.

### Cursor polling alone (rejected, D1)
* **Rejected:** up to 3 s of latency, and a request per tab every few seconds. It is kept only as the fallback.

### WebSocket (rejected)
* **For it:** two-way.
* **Against it:** the app needs only server-to-client notices. SSE passes through Cloudflare Access with the same cookie, needs no extra protocol, and reconnects in a simple way.

### Postgres logical decoding (wal2json or pgoutput) (rejected, D5)
* **For it:** it captures every write with no triggers.
* **Against it:** a replication slot holds WAL while a consumer is down. It also needs replication privileges and gives no RLS on the stream.

### New app beside the old one at `/app/*` (rejected, D2)
* **Rejected:** two entry points for a long time, and the old pages stay in use.

### Separate static hosting (rejected, D2)
* **Rejected:** CORS, a second Access application, and two deploys to keep in step.

### Shell-only offline (rejected, D3)
* **Rejected:** a window reopened offline is empty.

### Queued offline writes (rejected, D3)
* **Rejected:** replaying a stale decision breaks the reliability rule.

### TanStack Query (rejected, D4)
* **Rejected:** it refetches a whole query on each change and needs a React compatibility layer.

### Change log written by the store layer, or an in-memory ring (rejected, D5)
* **Rejected:** raw SQL writes are missed, or every deploy forces a resync.

## Non-Functional Requirements

* **Reliability.**
  * A change is visible only with its committed data.
  * The cursor never passes an unfinished transaction.
  * An unconfirmed host stays unconfirmed, never failed.
* **Security.**
  * The browser holds no secret.
  * `/api/*` keeps the Bearer and Origin checks.
  * Feed reads run under RLS.
  * The service worker never caches `/api/*`.
* **Load.**
  * One `LISTEN` connection per process, and at most one short indexed read per open stream per commit wake (50 ms debounce).
  * About 10 people with a few windows each means a few tens of reads per wake.
  * Log size: one row per write. Measure rows per day in the first week; retention is the operator's setting.
* **Accessibility and design.** The page specs, OR-1 to OR-22 and `check:design` apply to the app routes.

## Additional Resources

* [Design system](../design-system/README.md), including [pages](../design-system/pages/README.md) and [decisions](../design-system/decisions.md).
* [Data foundation](fleet-data-foundation.md), sections §6, §10, §11 and §12.
* Code: `packages/coordinator/app.ts`, `store.ts`, `db/schema.sql`, and `deploy/infrastructure/origin_guard.py`.

## Future Improvements

* **Priority classes in the scheduler and a queue view** (owner decision 2026-11-10; the data model is in [data foundation §2](fleet-data-foundation.md)).
  * Urgent runs first and may use accounts reserved for urgent work.
  * Normal runs first in, first out, and the operator can move a task up or down by hand.
  * Background runs only when nothing else waits for the same resource. What background work may use is an operator setting.
  * The Overview and Hosts show the queue per class: what runs next, and on which host and account. They also show who waits for what, with the reason, for example "waiting: Claude window 92%" or "waiting: reserved for urgent".
  * The first slice only stores and shows the class: chips, plus grouping and sorting by class.

* Projects, Laboratory, Agents, Access and History views, then deleting the remaining legacy pages.
* Per-person grants and per-stream scope (Phase 6).
* Results (Phase 3).
* A phone navigation variant.
* A retry operation.
* Optional web push for needs-you items.

## Detailed Implementation Plan

Order: spec items (13) first, then server items (1-7), then the app (8-12), then tests and checks (14-16).

### Target Files to Create/Modify

#### 1. `packages/coordinator/db/schema.sql` (Modify)
* **Type / Scope:** migration 8 (change log). `schema.sql` already has migration 7.
* **Proposed Changes:**
  * Table `change`, with the columns listed in §4, an index on `seq`, and an index on `(session_id, seq)`.
  * Table `change_log_floor(seq bigint)`, with one row set to `0`.
  * RLS with `FORCE` and the `project_scope` policy on `change`.
  * Function `fleet_change()`, written on one line (the splitter rule). It implements the kind map in §4 with `CASE TG_TABLE_NAME` and returns `NULL`.
  * Six row triggers, `AFTER INSERT OR UPDATE OR DELETE`, on `task`, `task_dependency`, `execution`, `execution_totals`, `question` and `document`.
* **Why:** D5, G2.

#### 2. `packages/coordinator/store.ts` (Modify)
* **Proposed Changes:**
  * `changes.since(cursor, limit)`, which applies the watermark query, the floor and the maximum check, and returns `{changes, cursor, resync}`.
  * `changes.prune(retentionDays)`, which deletes in batches of 10,000, each batch a `store.atomic` in lock scope `change:prune` (not GLOBAL, so writers never wait for it), and updates the floor.
  * `listen(channel, fn)`:
    * on Postgres, a dedicated `pool.connect()` client, which reconnects and re-runs `LISTEN` after an error;
    * on PGlite, `db.listen`, exposed through `backend()`.
  * Add the new ops to `READ_OPERATIONS`.
* **Why:** D1, D5.

#### 3. `packages/coordinator/stream.ts` (Create)
* **Proposed Changes:**
  * `registerStream(app, store, config)`, with:
    * one listener per process;
    * a set of open streams `{reply, cursor}`;
    * a 50 ms debounce;
    * per-stream reads through `store.read(() => store.changes.since(...))`;
    * the SSE framing and headers;
    * a 25 s ping;
    * an hourly prune timer.
  * Close cleanly on client abort and on `app.close`.
* **Why:** D1.

#### 4. `packages/coordinator/app.ts` (Modify)
* **Proposed Changes:**
  * Add `/api/stream` to `longRoutes`.
  * Add the new guard rule (§6).
  * Add `typedOps` for `changes_since`, `list_membership`, `sessions_list`, `hosts_list`, `me`, and the extended `task_list`.
  * Call `registerStream`.
  * Serve the app with `@fastify/static` from `config.appDir`, plus the app routes.
  * Redirect `/` to `/overview` and the old paths to `/legacy/*`.
  * Add the new config fields `appDir`, `ownerEmail` and `changeLogRetentionDays` to the `createApp` config type and to `server.ts`.
* **Why:** D1, D2, G5, G7.

#### 5. `packages/coordinator/tasks.ts`, `executions.ts` (Modify); `sessions.ts` (Create)
* **Proposed Changes:**
  * `tasks.list` gets keyset paging, the sort options, the group key and `counts`.
  * `tasks.membership(filter, ids)` uses the same `WHERE` builder as `list`.
  * `executions.runningByHostHarness()` returns counts of `running` and `preparing` executions per `(host_id, harness)`.
  * `sessions.list` and `sessions.membership` go in a small `sessions.ts` (Create), over `document` kind `session`.
* **Why:** D4, G1, G2.

#### 6. `packages/coordinator/ui.ts`, `project-ui.ts`, `lab-ui.ts`, `account-ui.ts`, `lab.ts`, `projects.ts`, `accounts.ts`, `recovery.ts` (Modify)
* **Proposed Changes:**
  * Move the page routes to `/legacy/*` (`app.ts:53`, `lab.ts:17`, `projects.ts:71`, `accounts.ts:23`, `recovery.ts:17`).
  * Rewrite the inline links: the header nav in `ui.ts:1`, `/?session=` in `ui.ts:6`, `lab-ui.ts:1-2`, `project-ui.ts:1,8,14` and `account-ui.ts:1`.
  * Add the "Legacy view. New: <link>" line.
  * The API calls do not change.
* **Why:** D2, G7.

#### 7. `deploy/infrastructure/sse-smoke.sh` (Create)
* **Proposed Changes:** a manual post-deploy check through `fleet.ufobe.com`: one ping within 30 s, and one event within 2 s of a `task_update` on a test task.
* **Why:** the SSE proxy-chain risk.

#### 8. `app/build.mjs`, `app/tsconfig.json`, `app/index.html` (Create)
* **Proposed Changes:**
  * esbuild build to `dist/app` (ESM, splitting, hashed names).
  * `index.html` with a script that picks the manifest.
  * `sw.js` with the precache list.
  * Four manifests, with icons rendered from registry icons.
  * A size report.
* **Why:** §7, G4, G6.

#### 9. `app/src/main.tsx`, `app/src/routes.tsx` (Create)
* **Proposed Changes:**
  * `AppShell` with nav from `workspaces`.
  * Wouter routes (§2) with the `/` redirect.
  * Service worker registration and the update notice.
  * The signed-out screen.
* **Why:** §2, §5.

#### 10. `app/src/api.ts`, `store.ts`, `feed.ts`, `offline.ts` (Create)
* **Proposed Changes:**
  * `api.ts`: `op()`, same-origin, with online, offline and signed-out detection.
  * `store.ts`: rows, lists and batched apply (§3).
  * `feed.ts`: `EventSource` with `?cursor=`, backoff, polling fallback and `resync`.
  * `offline.ts`: the mirror, the lowest-cursor restore, and the delete triggers.
* **Why:** D1, D3, D4.

#### 11. `app/src/views/overview.tsx`, `tasks.tsx`, `task-detail.tsx`, `conversations.tsx`, `conversation-detail.tsx`, `hosts.tsx`, `host-detail.tsx` (Create)
* **Proposed Changes:**
  * **Overview** reads `questions_list {state:'open'}` (needs-you), `hosts_list` (workers per host), and `task_list` counts per project. Problems come from `executions_list {problem:true}`.
  * **Tasks** composes `TaskListView`, `ListDetail`, `OutcomeBlock` and `DecisionBlock` (→ `questions_answer`). It wires pause, continue and cancel to `task_update`.
  * **Conversations** composes `Transcript` and `Composer`, and wires the session actions from today's `/` through their existing ops: `session_create`, `/api/sessions/:id/messages`, `execution_stop`, `session_fork`, `session_transition` and `session_profile`.
  * **Hosts and host detail** follow `pages/hosts.md`, with stores marked "unavailable".
* **Why:** G1. The views use only `packages/ui` (OR-13).

#### 12. `packages/ui/src/*` (Modify)
* **Proposed Changes:**
  * `VirtualList` gains `onEnd` for infinite scroll.
  * `TaskListView` takes `counts` and groups from props.
  * New controls: `Composer` and `OfflineMarker`.
* **Why:** G1, G2. The specs come first (item 13).

#### 13. `docs/design-system/pages/conversations.md`, `controls/composer.md`, `controls/offline-marker.md`, `decisions.md` (Create/Modify)
* **Proposed Changes:**
  * The Conversations page spec, mapped to the inventory's Sessions items.
  * Specs for the composer and the offline marker.
  * Design-system D15, which links this doc.
  * Book sections for the new controls.
* **Why:** spec first.

#### 14. `tests/coordinator/stream.test.ts`, `ops-lists.test.ts`, `legacy-routes.test.ts` (Create); `recovery.test.ts`, `lab.test.ts` (Modify)
* **Proposed Changes:**
  * **Stream and feed:**
    * the trigger and kind map for each source;
    * no change row after a rollback;
    * the watermark with two overlapping transactions;
    * the floor and `resync`, including an empty log and a cursor ahead of the maximum;
    * the stream delivers a commit made after it opened;
    * a 1,000-event burst produces one wake per commit;
    * RLS on change rows, tested with a second store at `{scope:'p1'}`.
  * **Lists:** `list_membership` matches `task_list` and `sessions_list` for each fixture.
  * **Host polls:** a poll that only changes `lastSeenAt` writes no change row.
  * **`hosts_list`:** counts per harness.
  * **Legacy routes:** `/legacy/*` pages load; app routes need no Bearer; `/api/*` still does.
  * **Existing tests to update:**
    * `recovery.test.ts:28` and `lab.test.ts:3` use the old paths.
    * The browser scripts `lab-browser.ts:2`, `projects-browser.ts:2`, `accounts-browser.ts:8`, `live-ui-ops.ts:11,17`, `recovery-browser.ts` and `proxy-browser.ts` move to `/legacy/*`.
* **Why:** D1, D4, D5, G7.

#### 15. `tests/ui/store.test.ts`, `tests/ui/app.check.mjs` (Create); `package.json`, `packages/ui/scripts/check-design.mjs` (Modify)
* **Proposed Changes:**
  * **Store unit tests:** membership insert and remove; order by sort key; the anchor kept when rows are inserted above; a lower `seq` ignored; the mirror round trip; lowest-cursor restore.
  * **`package.json`:** the `test` script runs `tests/coordinator/*.test.ts tests/ui/*.test.ts`, so these tests block CI like the others. Add `@fastify/static` (exact 8.x) and the `app:dev` script.
  * **`app.check.mjs`:** as in §9.
  * **`check-design.mjs`:** runs it next to `ux.check.mjs`. The CI workflow does not need to change.
* **Why:** G2, G6, D4.

### Verification Plan

* `npm run build` builds the coordinator and the app. `npm test` passes, including the new tests.
* `npm run check:design` reports the values against their targets: trace on the app routes, UX checks, first-load JS, click-to-detail and change-to-screen. Warnings do not fail.
* CUJ 1 to CUJ 5 are run on a local coordinator behind the test proxy, with screenshots in the PR:
  * two installed windows;
  * reopening offline;
  * a restart while streams are open;
  * a quiet host.
* CI must pass on build and test. Nothing is deployed as part of this PR.

<!-- Provenance: Indicates initial structural drafting via EGM. -->
