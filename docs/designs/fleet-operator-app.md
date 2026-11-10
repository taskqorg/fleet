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
* **Proxy mode:** production behind the Access origin guard (`fleet-gateway guard`), which injects the browser Bearer token.
* **GLOBAL write:** the coordinator's single-writer scope lock ([data foundation §12](fleet-data-foundation.md#12-document-store-concurrency-model)).
* **Session** is the stored entity; **Conversations** is the view that shows sessions.

## The Problem

* **The prototype pages cannot carry daily operator work.**
  * They were inline Russian-language HTML strings in the TypeScript coordinator.
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
* **G7. The old pages keep working until replaced.** (Superseded 2026-11-10: the owner removed all legacy pages in one cut with [Lab Stage 0](fleet-lab.md); gaps are in [legacy-inventory.md](legacy-inventory.md).) They stay under `/legacy/*` until each one is replaced.

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
| D2 | Where the app lives | The app owns all of `fleet.ufobe.com`; the prototype pages are deleted (G7). Hosts never get a UI. Each main view has its own manifest. First slice: Tasks, Conversations, Overview and Hosts; Results waits for Phase 3. |
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
  * The operator opens T-9989. At 1100 px or narrower, the opened task takes the place of the list and takes focus.
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
  * Cursors stay valid because the log is in Postgres, unless their change row has been pruned, in which case the app resyncs.
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
Browser windows (one origin)                          Coordinator (Go)
┌──────────────────────────────┐  ┌──────────────┐  ┌──────────────────────────────────────────┐
│ App: views, entity store,    │─▶│ Cloudflare   │─▶│ /api/ops/:name   ops (Store.Tx read/write) │
│ IndexedDB mirror,            │  │ Access +     │  │ /api/stream      SSE (own short reads)     │
│ service worker (shell only)  │◀─│ Access guard │◀─│ me (op)          fixed owner (this slice)  │
└──────────────────────────────┘  │ (JWT check,  │  │ /, /tasks, ...   index.html; /assets/* ... │
                                  │ Bearer added)│  │ /api/health      public, no secrets        │
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
* **Shell.** `AppShell` from `packages/ui`, with nav items built from the registry's `workspaces`, the one list of screens in nav order. A workspace marked `app: true` is a built screen: the app nav and routes (`app/src/main.tsx`, whose view map the type checker requires for every such screen), the manifests and service-worker routes (`app/build.mjs`, which writes `dist/app/screens.json`) and the coordinator's `index.html` routes (`appRoutes` in `go/coordinator/d_routes.go`, from that file) all follow the flag, so a new screen appears everywhere once it is set and its view is added. The stored entity is a session; the workspace, nav item and view are named Conversations everywhere.

* **Codes.** Rows and details show the human code after the title through the `packages/ui` formatter (design-system D15). The UUID is only in a tooltip and "copy UUID". Search accepts a code, an alias or a UUID; a code goes through `entity.resolve`, and an alias opens the entity under its current code. Detail URLs keep the UUID (`/tasks/:id`) until codes exist; the view URL codec passes a code through unchanged, and the redirect direction for codes is decided by the D17 slice ([fleet-view-urls-and-list-path.md](fleet-view-urls-and-list-path.md), Non-Goals).

### 3. Data loading and the entity store (D4)

* **Rows.**
  * Rows are kept by `(kind, id)`. The kinds are `task`, `execution`, `question`, `session`, `host` and `event` (per open conversation).
  * Each row carries the `seq` it was read at. A change with a lower or equal `seq` than the row's is ignored.
* **Lists.** A list is `{ ids, keys, counts, total, groups, nextAfter, at }` under its canonical list key, plus the change cursor it reflects ([fleet-view-urls-and-list-path.md §2](fleet-view-urls-and-list-path.md)). `counts` holds per-state counts from the server.
* **Reads.**
  * Existing ops: `task_get`, `questions_list`, `executions_list` and `session_get`.
  * New paged op: `task_page` (`task_list` keeps its existing shape for agents; design-system D15).
  * New ops (§6): `task_page`, `sessions_list`, `hosts_list`, `list_membership`, `changes_since` and `me`.
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
| `task` | `task` | `id` | — | Row `task`; lists of tasks; counts |
| `task_dependency` | `task` | `task_id` | `task_id` | Row `task` |
| `execution` | `execution` | `id` | `task_id`, `session_id`, `host_id` | Rows `execution`; the parent `task` (harness mark), `session` and `host` (counts) |
| `execution_totals` | `execution` | `execution_id` | `task_id`, `session_id`, `host_id` from the execution | Row `execution` (totals) |
| `question` | `question` | `id` | `task_id`, `session_id` | Rows `question`; the parent task (state, needs-you) and session |
| `document` kind `session` | `session` | `id` | `session_id` = `id` | Row `session`; lists of sessions |
| `document` kind `event`, `message` | `event` | `id` | `session_id` from `data->>'sessionId'` | The open conversation fetches `/api/sessions/:id/events?after=` |
| `document` kind `host-observation` (written by the `/api/hosts/poll` handler on every poll) | `host` | `id` | `host_id` = `id` | Row `host`. Updates where only `lastSeenAt` changed are skipped (`OLD.data - 'lastSeenAt' = NEW.data - 'lastSeenAt'`), so polls do not flood the feed; `/hosts` and the Overview refresh `lastSeenAt` by re-reading `hosts_list` on their 5 s staleness timer. |
| `document` kind `command` | `command` | `id` | `host_id`, `session_id` from `data` | The open conversation and host re-read their commands |
| `document` kind `artifact` (legacy artifacts) | `artifact` | `id` | `session_id` from `data`, `task_id` from the execution | Results and task detail |
| `artifact`, `artifact_copy` (migration 9, `fleet_artifact_change`) | `artifact` | artifact `id` | `task_id` (artifact or execution), `session_id`, `project_id` | Results and task detail. Copy updates that only move `lease_expires_at` or `updated_at` are skipped. |
| `store` (migration 9) | `host` | `host_id` | `host_id` | Row `host` (stores). Updates that only move `observed_at` or `updated_at` are skipped. |
| Other `document` kinds | — | — | — | No change row. A kind is added to this map when a view needs it, which keeps the log small. |

**Visibility watermark.** This replaces any fixed overlap window. Execution-scope ingest commits in parallel with GLOBAL writers, so `seq` order is not commit order. Every read of the log therefore returns only rows whose transaction is older than the oldest transaction still running:

```
SELECT ... FROM change WHERE seq > $cursor AND xid < pg_snapshot_xmin(pg_current_snapshot()) ORDER BY xid, seq
```

Kinds are always the client kinds above, never legacy kinds (design-system D15). Rows are read in `(xid, seq)` order, so a transaction's rows stay together.

* The new cursor is the highest `seq` returned from that contiguous visible prefix. No row is skipped, because a row from a transaction that is still open blocks the cursor until it commits or rolls back. A rolled-back transaction leaves no rows.
* The cost: one long write transaction delays the feed by its own duration. Feed delay is shown in the check report.

**Stream.**
* `GET /api/stream?cursor=N` is excluded from the GET read-snapshot wrapper (`longRoute`, `go/coordinator/registry.go`).
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
* A batch prune runs every hour in its own lock scope, `change:prune`, not GLOBAL. It deletes up to 10,000 rows per transaction in `(xid, seq)` order. There is no floor table.
* The server sends `resync` when the cursor's own change row is missing: it was pruned, or the cursor is unknown, for example after a database restore (design-system D15). The client then marks its lists stale and re-reads them, keeping each anchor by id.

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

* **Guard rule.** The app routes (`/`, `/assets/*`, `/sw.js`, `/manifests/*`, `/icons/*` and the view paths) are public (`publicRoute`, `go/coordinator/d_routes.go`). Every `/api/*` request except `GET /api/health`, including `/api/stream`, carries the Bearer. The Access origin guard (`go/gateway/guard.go`) guards the whole site. Not built: the coordinator's own `Origin` check on browser writes.
* **Read ops.** These are read operations (`operation(name, true, …)`, one read snapshot) and are served as MCP tools too:
  * `changes_since {cursor, limit≤1000}` returns `{changes, cursor, resync?}`.
  * `list_membership {lists:[{name:'tasks'|'sessions', filter, group?}], ids}` returns `{lists:[{name, rows:[{row, sortKey}], counts, groups?}]}` (`groups` for grouped task lists). Hosts are few, so `/hosts` re-reads `hosts_list` on any `host` or `execution` change instead.
  * Not built in Go: `view_link {view} | {url}` returns `{url, path}` or `{view}`: the canonical operator-app link for a view, or the view of a link; 400 for an invalid view, a foreign origin or a path that is not a view. Also in the agent operation table, so agent MCP endpoints offer it ([fleet-view-urls-and-list-path.md §1](fleet-view-urls-and-list-path.md)).
  * `task_derived_check {}` returns `{checked, drifted}`: tasks whose stored derived values (`task_derived`) differ from a fresh computation, limited to the caller's projects.
  * `me {}` returns `{email: config.ownerEmail, roles:['operator','architect'], grantsVersion:'1', changeLogRetentionDays}`.
  * `sessions_list {filter, limit, after}` returns `{sessions, nextAfter, counts}`. Each session row has id, title, state, agent, last activity and open questions.
  * `hosts_list {}` returns `{hosts, staleAfterMs: 30000}`. The host ids are the configured ids (`config.hostId`, `integrationHostId`, `publicationHostId`, `nativeHostIds`) plus every `host-observation`. Each host row has `{id, platform, lastSeenAt, runningByHarness, slots, queued, stores: 'unavailable'}`. This is the first part of data foundation §11 `hosts.list`, with the same shape, extended in Phase 3.
  * `task_page` (new; `task_list` is unchanged for agents):
    * `{filter:{projectId?, state?: state[], search?}, group?: 'priority'|'state'|'project'|'agent'|'host'|'none', limit?: 1..500 (default 100), after?: [groupKey, seq, id]}`; there is no separate sort option;
    * a response of `{tasks, nextAfter, counts:{<state>:n}}`;
    * order is group key, then newest first, then id; `after` is that triple.
* **Routes.**
  * `GET /api/stream`.
  * `/assets/*` (immutable), `/sw.js`, `/manifests/*` and the app routes, which serve `index.html` (`no-cache`).
  * `/` redirects to `/overview`.

### 7. Build and serving

* `pnpm run build` type-checks, then runs `node app/build.mjs`, then builds the coordinator.
* `app/build.mjs` runs esbuild in ESM mode with code splitting per view, minified, with hashed names, and writes to `dist/app`. It also writes `index.html`, `sw.js` (with the precache list) and the manifests, and prints chunk sizes (raw and gzip).
* `app/tsconfig.json` type-checks `app/` and `packages/ui` with Preact JSX.
* The coordinator serves `FLEET_APP_DIR`, default `dist/app` (`appRoutes`, `go/coordinator/d_routes.go`): hashed assets immutable, entry files revalidated.
* For development, `pnpm run app:dev` reruns `app/build.mjs` into `dist/app` whenever `app/src`, `packages/ui` or `packages/view-url` changes (`node --watch-path`).

### 8. Coexistence and retirement

The prototype pages were deleted with the TypeScript coordinator (owner, 2026-11-10). [legacy-inventory.md](legacy-inventory.md) lists what each one offered and which screen covers it.

### 9. Measurement

* **First-load JS.** `app/build.mjs` reports the size per chunk, and `check:design` prints it against 200 KB raw.
* **Running the app for checks.** `tests/ui/app.check.ts` starts:
  * the coordinator binary on a fresh local Postgres database (`tests/harness.ts`), with fixtures;
  * a local forward proxy that injects the Bearer token, the same way the guard does in production.

  It then runs the trace, UX, click-to-detail (median of 5) and change-to-screen checks against the real routes. Change-to-screen means a `task_update` commit until the row's icon changes, reporting the median and the worst case. `check:design` runs it and prints the results, as warnings only.
* **SSE through the real chain.** `deploy/infrastructure/sse-smoke.sh` opens `/api/stream` through `fleet.ufobe.com`. It confirms a ping within 30 s and an event within 2 s of a test write. It is run manually after a deploy and is not part of this slice's PR.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| The feed skips a change committed out of `seq` order. | The xid watermark (§4) lets the cursor advance only past finished transactions. A test commits two overlapping transactions in reverse order. |
| Event ingest floods the stream. | A constant notify payload (one wake per commit), a 50 ms debounce, one short read per stream per wake, and a test with a burst of 1,000 events. |
| The stream holds a snapshot or a pool connection. | It is excluded from the GET wrapper, and every read is its own short `store.read`. A test commits after the stream opens. |
| The prune blocks writers. | Batched deletes in their own lock scope `change:prune`, plus resync when a cursor's row is gone. |
| An expired Access session looks like offline. | A separate "signed out" state, triggered by a redirect, 401 or 403. |
| A proxy buffers SSE. | No-transform and no-buffering headers, no compression, a smoke check, and the `changes_since` polling fallback. |
| A stale app shell after a deploy. | Network-first `index.html`, hashed assets, and an "Update available" notice. |
| Data left on a shared or lost laptop. | IndexedDB is deleted on sign-out, on session expiry and on a `grantsVersion` change. It is not encrypted at rest; device encryption is assumed. |
| Client and server disagree about a list. | The client never decides membership (D4). A property test compares `list_membership` with `task_page`. |

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
* **Accessibility and design.** The page specs, the [owner rules](../design-system/owner-rules.md) and `check:design` apply to the app routes.

## Additional Resources

* [Design system](../design-system/README.md), including [pages](../design-system/pages/README.md) and [decisions](../design-system/decisions.md).
* [Data foundation](fleet-data-foundation.md), sections §6, §10, §11 and §12.
* Code: `go/coordinator/registry.go`, `stream.go`, `store.go`, `d_routes.go`, `packages/coordinator/db/schema.sql`, and `go/gateway/guard.go`.

## Future Improvements

* **Priority classes in the scheduler and a queue view** (owner decision 2026-11-10; the data model is in [data foundation §2](fleet-data-foundation.md)).
  * Urgent runs first and may use accounts reserved for urgent work.
  * Normal runs first in, first out, and the operator can move a task up or down by hand.
  * Background runs only when nothing else waits for the same resource. What background work may use is an operator setting.
  * The Overview and Hosts show the queue per class: what runs next, and on which host and account. They also show who waits for what, with the reason, for example "waiting: Claude window 92%" or "waiting: reserved for urgent".
  * The first slice only stores and shows the class: chips, plus grouping and sorting by class.

* Projects, Laboratory, Agents, Access and History views.
* Per-person grants and per-stream scope (Phase 6).
* Results (Phase 3).
* A phone navigation variant.
* A retry operation.
* Optional web push for needs-you items.

## Implementation

Built: the change feed (`go/coordinator/stream.go`, `changes_since` in `d_routes.go`), the list operations (`task_page` and `task_derived_check` in `b_tasks.go`, `sessions_list` and `list_membership` in `b_sessions.go`, `hosts_list` in `a_hosts.go`, `me` in `b_people.go`), the app routes (`d_routes.go`), the app (`app/build.mjs`, `app/src/`) and the checks (`tests/ui/store.test.ts`, `tests/ui/app.check.ts`).

### Verification

* `pnpm run build` builds the app and the coordinator; `pnpm test` passes.
* `pnpm run check:design` reports the values against their targets: trace on the app routes, UX checks, first-load JS, click-to-detail and change-to-screen. Warnings do not fail.
* CUJ 1 to CUJ 5 run on a local coordinator behind the test proxy: two installed windows, reopening offline, a restart while streams are open, a quiet host.

<!-- Provenance: Indicates initial structural drafting via EGM. -->
