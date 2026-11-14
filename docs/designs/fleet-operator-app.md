# Fleet operator app

Current target for the operator app, interface workflows and live change feed. The [view URL and list contract](fleet-view-urls-and-list-path.md) and [Conversations contract](fleet-conversations.md) define their own topics.

## Overview

The operator app is the web interface operators use every day. The coordinator serves the app; the prototype pages and `/legacy/*` routes are retired. It is built only from `packages/ui`: the approved design book, its controls and patterns, and the [owner rules](../design-system/owner-rules.md) OR-1 to OR-22.

The app is a single-page Preact application (design-system D4: Preact, Signals, Wouter, esbuild). The coordinator serves it at `fleet.ufobe.com`, behind Cloudflare Access and the access guard. It uses the shared core operations (`/api/ops/:name`); the stream and result bytes/previews use their coordinator transport routes rather than UI-only business operations.

* Live updates come from a server-sent change feed (§4).
* The last-read data is kept for offline reading (§5).
* Each main view can be installed as its own PWA window.

The installed screens are selected by `workspaces` with `app: true` in `packages/ui/src/registry.ts`: Overview, Tasks, Conversations, Results, Agents, Pipelines, Catalogue, Hosts, Laboratory and API keys. This list describes built routes, not complete acceptance of every workflow. Projects and Access remain unbuilt screens. The [data foundation](fleet-data-foundation.md) owns the stored objects.

**Terms used here:**
* **R2:** [fleet.md principle](../../fleet.md#principles): interfaces add no operations of their own.
* **Proxy mode:** production behind the Access origin guard (`fleet-gateway guard`), which injects the browser Bearer token.
* **GLOBAL write:** the coordinator's single-writer scope lock ([data foundation §12](fleet-data-foundation.md#12-document-store-concurrency-model)).
* **Session** is the stored entity; **Conversations** is the view that shows sessions.

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
* **G5. No app-only operations.** Every read and action the app uses is an existing or new coordinator op that agents can call too (R2). `/api/stream` transports the same change log that `changes_since` reads; result bytes/previews use the shared artifact reader routes. Neither transport adds app-only business operations.
* **G6. Performance is measured and reported.**
  * First-load JavaScript is reported against 200 KB raw.
  * Click-to-detail is reported against 100 ms.
  * Change-to-screen is reported against 1 s.
* **G7. One app entrypoint.** The prototype pages and `/legacy/*` routes are retired; no coexistence or redirect period remains. Surviving requirements are in §8.

## Non-Goals

* Detailed Results, artifact and architect workflows belong to their linked topic specifications; a built route does not establish every target there.
* Recovery actions follow their canonical operations and contracts; this app specification does not authorize replay of uncertain effects.
* Identity, roles, project grants and holder exceptions follow [people and access](fleet-people-access.md). The app does not define a second authority model.
* **A UI on hosts.** Hosts run only executors.
* **Offline writes, background sync, push notifications, and a native desktop app.**
* **Visual regression tests.**

## Open Questions

* **Phone navigation** (design-system open question (d)). The app keeps the interim icon rail until this is decided.

## Decisions

| # | Question | Choice |
|---|---|---|
| D1 | Live updates | SSE change feed with a resume cursor, plus cursor polling (`changes_since`) as the catch-up and fallback path. Both share one versioned cursor. A missing cursor requests resync; mounts load fresh data and lists with restored rows or failed loads revalidate on reconnect (§3–§4). The heartbeat stays under Cloudflare's 100 s idle timeout. |
| D2 | Where the app lives | The app owns all of `fleet.ufobe.com`; the prototype pages are deleted (G7). Hosts never get a UI. Each main view has its own manifest. The built-screen registry determines the available routes. |
| D3 | Offline | The service worker caches the shell. IndexedDB keeps the last-read data, read-only, with its age shown. No queued writes. The store is cleared on sign-out and on a grant change, and holds only what the grants allow. |
| D4 | Client data layer | One normalized entity store on Preact Signals, in our own code. The client never decides list membership: `list_membership` decides, in one batched read. Membership, ordering and the anchor are covered by tests. Implemented by `app/src/store.ts` ([fleet-view-urls-and-list-path.md §2](fleet-view-urls-and-list-path.md)); the first version kept lists per component, which reset them on every filter change. |
| D5 | Change-feed source | A change-log table written by triggers on every watched table, including `document`. The cursor is `seq`. Reads run under RLS. LISTEN/NOTIFY wakes the stream. Change rows older than 24 hours are pruned in bounded batches; durable work and transcripts are retained. An old cursor gets `resync`. |

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
│ service worker (shell only)  │◀─│ Access guard │◀─│ me (op)          current person and grants  │
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
* **List state** (filters, grouping, search, presets, the focused row and checked rows) lives in the URL in one canonical form, defined in [fleet-view-urls-and-list-path.md §1](fleet-view-urls-and-list-path.md) and implemented by `packages/view-url`; agents build and read such links with `view_link`. Section pins, row pins, saved views, the project focus and column widths are person settings (`person_settings_get` / `person_settings_set`, `app/src/prefs.ts`; [people access §8](fleet-people-access.md)); the browser keeps none of them. IndexedDB holds the read-only last-view mirror.
* **Shell.** `AppShell` from `packages/ui`, with nav items built from the registry's `workspaces`, the one list of screens in nav order. A workspace marked `app: true` is a built screen: the app nav and routes (`app/src/main.tsx`, whose view map the type checker requires for every such screen), the manifests and service-worker routes (`app/build.mjs`, which writes `dist/app/screens.json`) and the coordinator's `index.html` routes (`appRoutes` in `go/coordinator/artifact_routes.go`, from that file) all follow the flag, so a new screen appears everywhere once it is set and its view is added. The stored entity is a session; the workspace, nav item and view are named Conversations everywhere.

* **Codes.** Rows and details show the human code after the title through the `packages/ui` formatter (design-system D17). The UUID is only in a tooltip and "copy UUID". Search accepts a code, an alias or a UUID; a code goes through `entity.resolve`, and an alias opens the entity under its current code. Not built: human entity code allocation and resolution; detail URLs currently keep the UUID (`/tasks/:id`); the view URL codec passes a code through unchanged, and the redirect direction for codes is decided by the D17 slice ([fleet-view-urls-and-list-path.md](fleet-view-urls-and-list-path.md), Non-Goals).

### 3. Data loading and the entity store (D4)

* **Rows.**
  * Rows are kept by `(kind, id)`. The kinds are `task`, `execution`, `question`, `session`, `host` and `event` (per open conversation).
  * Each row carries the `seq` it was read at. An older read answer is ignored; equal-cursor row replacements are allowed (`app/src/store.ts` `putRows`).
* **Lists.** A list is `{ ids, keys, counts, total, groups, nextAfter, at }` under its canonical list key, plus the change cursor it reflects ([fleet-view-urls-and-list-path.md §2](fleet-view-urls-and-list-path.md)). `counts` holds per-state counts from the server.
* **Reads.**
  * Existing ops: `task_get`, `questions_list`, `executions_list` and `session_get`.
  * Paged op: `task_page` (`task_list` keeps its existing shape for agents; design-system D15).
  * Other read ops (§6): `task_page`, `sessions_list`, `hosts_list`, `list_membership`, `changes_since` and `me`.
* **Applying changes.** Changes are applied in batches, one batch per animation frame:
  1. Map each change to the affected client rows and lists through the kind map (§4).
  2. Make one `list_membership` call for all open lists with the affected ids. It returns, for each list, the matching rows themselves, each with its sort key, plus fresh `counts`.
  3. Re-read affected rows that are open in a detail but not in any list through their existing get op (`task_get`, `session_get`).
  4. Insert, remove and move ids through `VirtualList`, which keeps the anchor in place and shows the "N new" pill.
* **Counters.** Counters (Σ, state counters, nav badges) come only from server `counts`, never from counting loaded rows. See the [list-view server note](../design-system/patterns/list-view.md).
* **Identity.**
  * The `me` op is read at start, on every stream reconnect, and when the tab regains focus.
  * If `grantsVersion` differs from the stored one, the app deletes IndexedDB (D3).
  * `grantsVersion` is a hash of the current person's roles, owner flag and grants (`go/coordinator/people.go`). Not built: an additional `me` re-read on each stream reconnect; the app currently reads it on mount and focus.

### 4. Live updates (D1, D5)

**Change table.** Columns:
* `change(seq bigserial, xid xid8 DEFAULT pg_current_xact_id(), kind text, id text, task_id text, session_id text, host_id text, project_id text, op char(1), at timestamptz DEFAULT now())`, with the later `actor` and `holder` columns used by audit visibility.
* RLS: the same `project_scope` policy as the other tables. Visibility includes ordinary project scope and the people/access holder rules; a null project id is not an unrestricted public row.

**Triggers.** Trigger functions in `go/coordinator/schema.sql` write the change rows; their formatting must remain compatible with the migration reader in `go/coordinator/store.go`.
* It runs `AFTER INSERT OR UPDATE OR DELETE ... FOR EACH ROW`.
* It takes `r := CASE TG_OP WHEN 'DELETE' THEN OLD ELSE NEW END` and inserts one change row.
* It calls `pg_notify('fleet_change','')`. The payload is constant, so Postgres folds duplicate notifies within one transaction: one wake per commit, not per row.

**Kind map.** Written by the trigger. The kinds are `ChangeKind` in `contracts/ops.schema.json`; `go/coordinator/change_kind_test.go` fails when the triggers and that list differ, and the app's subscribers are typed by it.

| Source | `kind` | `id` | Parent ids filled | Client effect |
|---|---|---|---|---|
| `task` | `task` | `id` | — | Row `task`; lists of tasks; counts |
| `task_dependency` | `task` | `task_id` | `task_id` | Row `task` |
| `execution` | `execution` | `id` | `task_id`, `session_id`, `host_id` | Rows `execution`; the parent `task` (harness mark), `session` and `host` (counts); `kind = 'tool_run'` writes are skipped by `fleet_change()` so high-rate gateway calls do not flood the log |
| `execution_totals` | `execution` | `execution_id` | `task_id`, `session_id`, `host_id` from the execution | Row `execution` (totals); `tool_run` rows are skipped in `fleet_change()` |
| `question` | `question` | `id` | `task_id`, `session_id` | Rows `question`; the parent task (state, needs-you) and session |
| `document` kind `session` | `session` | `id` | `session_id` = `id` | Row `session`; lists of sessions |
| `document` kind `event`, `event-delta`, `message` | `event` | `id` | `session_id` from `data->>'sessionId'` | The open conversation fetches `/api/sessions/:id/events?after=` |
| `document` kind `host-observation` (written by the `/api/hosts/poll` handler on every poll) | `host` | `id` | `host_id` = `id` | Row `host`. Updates where only `lastSeenAt` changed are skipped (`OLD.data - 'lastSeenAt' = NEW.data - 'lastSeenAt'`), so polls do not flood the feed; `/hosts` and the Overview refresh `lastSeenAt` by re-reading `hosts_list` on their 5 s staleness timer. |
| `document` kind `command` | `command` | `id` | `host_id`, `session_id` from `data` | The open conversation and host re-read their commands |
| `document` kind `artifact` (legacy artifacts) | `artifact` | `id` | `session_id` from `data`, `task_id` from the execution | Results and task detail |
| `artifact`, `artifact_copy` (migration 9, `fleet_artifact_change`) | `artifact` | artifact `id` | `task_id` (artifact or execution), `session_id`, `project_id` | Results and task detail. Copy updates that only move `lease_expires_at` or `updated_at` are skipped. |
| `store` (migration 9) | `host` | `host_id` | `host_id` | Row `host` (stores). Updates that only move `observed_at` or `updated_at` are skipped. |
| `document` kind `managed-account`, `api-connection`, `connection-key`, `setting-change`, `project-recommendation` | the document kind | `id` | `project_id` and `holder` from the document; the holder sees the change, as they see the document | Subscription limits on `/hosts`; the Keys screen |
| `task_derived` | `task` | `task_id` | `task_id` | Task rows and list-derived fields |
| `experiment`, `score` | the table name | `id` | `project_id` | Laboratory and catalogue subscribers |
| `definition` | `definition` | `kind/id` | `project_id` | Definition editor subscribers |
| Other `document` kinds | — | — | — | No change row. A kind is added to this map when a view needs it, which keeps the log small. |

**Visibility watermark.** This replaces any fixed overlap window. Execution-scope ingest commits in parallel with GLOBAL writers, so `seq` order is not commit order. Every read of the log therefore returns only rows whose transaction is older than the oldest transaction still running:

```
SELECT ... FROM change WHERE (xid, seq) > ($cursor_xid, $cursor_seq)
  AND xid < pg_snapshot_xmin(pg_current_snapshot()) ORDER BY xid, seq
```

Kinds are always the client kinds above, never legacy kinds (design-system D15). Rows are read in `(xid, seq)` order, so a transaction's rows stay together.

* The cursor is the `seq` of the last row returned in `(xid, seq)` order; the next read looks up its xid. It is not the numeric maximum `seq`. No row is skipped, because a row from a transaction that is still open blocks the cursor until it commits or rolls back. A rolled-back transaction leaves no rows.
* The cost: one long write transaction delays the feed by its own duration. Feed delay is shown in the check report.

**Stream.**
* `GET /api/stream?cursor=N` is excluded from the GET read-snapshot wrapper (`longRoute`, `go/coordinator/registry.go`).
* On connect it sends the visible backlog in its own short read snapshot.
* One shared `LISTEN fleet_change` per process wakes a feed loop with 75 ms coalescing. The loop reads `since()` once, preserves the XID barrier, and retains at most 4096 change rows in memory. A common 250 ms held-transaction timer handles older transactions that end without another notification. New batches are filtered by PostgreSQL RLS once per distinct current scope/person, grants version, authorization generation, cursor and feed epoch; tabs share that result. The filtered cache holds at most 128 entries of up to 1000 changes each. Catch-up outside the ring uses a current scoped transaction; expired cursors receive `resync`. Internal cursor advancement past hidden rows is never emitted to the viewer.
* Each event is sent as `id: <seq>`, then `data: {kind,id,taskId,sessionId,hostId,op}`.
* A `: ping` comment every 25 s keeps the connection open.
* Headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache, no-transform`, `X-Accel-Buffering: no`. The stream is not compressed.
* The client reconnects manually with backoff and always passes `?cursor=`. `Last-Event-ID` is not used.
* Each identity batch and reconnect catch-up uses `scopeOf(q.person)` and the ordinary row-level policies in [people and access](fleet-people-access.md#2-visibility); an app stream does not inherit unrestricted process scope.

**Catch-up and fallback.**
* `changes_since {cursor, limit}` is the same read in request-response form.
* The client uses it to catch up after a reconnect of the stream (from its in-memory cursor).
* It also uses it when SSE fails three times in a row, polling every 3 s until the stream comes back.

**Retention and resync.**
* The root [history requirement](../../fleet.md#observation-and-evaluation) retains every execution in full without default expiry. The `change` table is a derived notification index (object ids, parent ids, operation, xid and seq), not the retained transcript, action or result payload. The earlier D5 prune proposal applies only to that index; it cannot authorize deletion of execution history. Not built: its configurable retention and hourly, separately locked batch. The current `me` reply reports a literal `changeLogRetentionDays: 7`, but no prune implementation or configured expiry was found. Unresolved authority: the old seven-day default is not adopted as a current expiry rule; any future index pruning must establish that required history remains accessible and follow ordinary owner settings. There is no floor table.
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
  * Not built: an explicit sign-out link (`/cdn-cgi/access/logout`) that deletes IndexedDB before it navigates. Detected session expiry already clears the mirror.
* **Manifests.**
  * There is one manifest per view: `/manifests/<view>.webmanifest`, each with its own `id`, `name` and `start_url`, and shared scope `/` so sibling workspace navigation stays in the installed window. All views share the navigation’s stylized Fleet F as their favicon and installation icon (`packages/ui/brand/`): SVG, 192/512 px PNG, and a 180 px Apple touch icon.
  * `index.html` picks the manifest by path and requests it with `crossorigin="use-credentials"` through the existing Access sign-in. Installation adds no access grant or authenticated-data cache.

### 6. Coordinator changes

* **Guard rule.** The app routes (`/`, `/assets/*`, `/sw.js`, `/manifests/*`, `/icons/*` and the view paths) are public (`publicRoute`, `go/coordinator/artifact_routes.go`). Every `/api/*` request except `GET /api/health`, including `/api/stream`, carries the Bearer. The Access origin guard (`go/gateway/guard.go`) guards the whole site. `go/coordinator/browser_checks.go` enforces the configured Origin on proxy-auth writes and rejects a supplied foreign Origin; host authentication follows its separate path.
* **Read ops.** These are read operations (`operation(name, true, …)`, one read snapshot) and are served as MCP tools too:
  * `changes_since {cursor, limit≤1000}` returns `{changes, cursor, resync?}`.
  * `list_membership {lists:[{name:'tasks'|'sessions', filter, group?}], ids}` returns `{lists:[{name, rows:[{row, sortKey}], counts, groups?}]}` (`groups` for grouped task lists). Hosts are few, so `/hosts` re-reads `hosts_list` on any `host` or `execution` change instead.
  * `view_link {view} | {url}` (`go/coordinator/view_link.go`, a port of `packages/view-url` checked against it by `tests/go/view.test.ts`) returns `{url, path}` or `{view}`: the canonical operator-app link for a view, or the view of a link; 400 for an invalid view, a foreign origin or a path that is not a view. Also in the agent operation table, so agent MCP endpoints offer it ([fleet-view-urls-and-list-path.md §1](fleet-view-urls-and-list-path.md)).
  * `task_derived_check {}` returns `{checked, drifted}`: tasks whose stored derived values (`task_derived`) differ from a fresh computation, limited to the caller's projects.
  * `me {}` returns the current person's e-mail, name, roles, owner flag, `grantsVersion` and `changeLogRetentionDays` (`go/coordinator/people.go`), rather than an unconditional owner identity.
  * `sessions_list {filter, limit, after}` returns `{sessions, nextAfter, counts}`. Each session row has id, title, state, agent, last activity and open questions.
  * `hosts_list {}` returns `{hosts, staleAfterMs: 30000}`. Host metadata, observations, command counts and stored-copy data come from `go/coordinator/hosts.go` under the caller's visibility. The reply includes `serverNow`; the client uses it to judge age despite browser clock skew. See [host settings and visibility](fleet-people-access.md).
  * `task_page` (`task_list` keeps its agent contract):
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
* The coordinator serves `FLEET_APP_DIR`, default `dist/app` (`appRoutes`, `go/coordinator/artifact_routes.go`): hashed assets immutable, entry files revalidated.
* For development, `pnpm run app:dev` reruns `app/build.mjs` into `dist/app` whenever `app/src`, `packages/ui` or `packages/view-url` changes (`node --watch-path`).

### 8. Coexistence and retirement

The prototype pages and `/legacy/*` routes are removed. Their historical implementation and API lists remain in Git history; the app does not restore the old pages or bypass ordinary operations.

| Surviving activity | Current app and remaining work |
|---|---|
| Read, create, send, steer, stop and fork sessions | Conversations; profile/model switch and handoff use `session_transition`. Questions use `questions_answer`. Detailed transcript and remaining gaps follow [Conversations](fleet-conversations.md). |
| Related work: delegations with parent, child and continuation state | `session_get` returns `delegations` (`go/coordinator/sessions.go`); Not built: their display in Conversations. Parent/child relationship and continuation state must remain visible when this UI is built. |
| Inspect result materials and checks, compare changed files | Results and task/execution context use the [result viewers](#result-viewing). The `execution_differences` read remains a core operation (`go/coordinator/executions.go`, `tests/go/inspection.test.ts`); it is not a reason to retain a legacy page. Not built: an app action for execution-to-execution file comparison. |
| Profiles, experiments, comparison, notes, recommendations and result previews | Laboratory and the definition editors; current actions and gaps follow [Lab](fleet-lab.md) and [definition editors](fleet-definition-editors.md). Retired Lab REST names are not today's canonical API. |
| Projects, readiness, context, granted catalogue, project sessions, pipeline and delivery history/actions, questions and result links | Tasks and core operations cover parts; [project configuration](../configuration.md) remains the authority. Not built: the Projects screen that brings these project activities together. A list operation does not establish project creation/onboarding. |
| Account records, per-host readiness, discovery, connect, enable, preference and login handoff | [People and access](fleet-people-access.md) and Hosts/limits expose parts through ordinary operations. Not built: the Access workspace; API keys is a separate built screen. No replacement may copy subscription credentials between hosts. |
| Inspect uncertain transport, authorize retained transfer without repeating native work | Core recovery operations remain available. Not built: app controls for `transport_inspect` / `transport_authorize` in Hosts. Retained transfer authorization never authorizes rerunning uncertain external effects. |

A rebuilt action uses the canonical core operation under the same grants as an agent. Its availability does not establish completion, acceptance or publication.

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
| Event ingest floods the stream. | A constant notify payload (one wake per commit), the unbuilt 50 ms debounce target (§4), short snapshot reads per stream, and a test with a burst of 1,000 events. |
| The stream holds a snapshot or a pool connection. | It is excluded from the GET wrapper, and every read is its own short read snapshot. A test commits after the stream opens. |
| Future change-index pruning blocks writers or loses history. | No prune is implemented. Resolve the authority boundary in §4 before adding it; a missing cursor already requests resync. |
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
  * One `LISTEN` connection per process; short indexed reads per open stream and batched 1,000-row draining. The specified 50 ms debounce remains unbuilt as noted in §4.
  * About 10 people with a few windows each means a few tens of reads per wake.
  * Log size: one row per mapped write. Measure its growth; the unresolved notification-index pruning proposal in §4 does not authorize execution-history expiry.
* **Accessibility and design.** The page specs, the [owner rules](../design-system/owner-rules.md) and `check:design` apply to the app routes.

## Additional Resources

* [Design system](../design-system/README.md), including [pages](../design-system/pages/README.md) and [decisions](../design-system/decisions.md).
* [Data foundation](fleet-data-foundation.md), sections §6, §10, §11 and §12.
* Code: `go/coordinator/registry.go`, `stream.go`, `store.go`, `artifact_routes.go`, `go/coordinator/schema.sql`, and `go/gateway/guard.go`.

## Future Improvements

* **Priority classes in the scheduler and a queue view** (owner decision 2026-11-10; the data model is in [data foundation §2](fleet-data-foundation.md)).
  * Urgent runs first and may use accounts reserved for urgent work.
  * Normal runs first in, first out, and the operator can move a task up or down by hand.
  * Background runs only when nothing else waits for the same resource. What background work may use is an operator setting.
  * The Overview and Hosts show the queue per class: what runs next, and on which host and account. They also show who waits for what, with the reason, for example "waiting: Claude window 92%" or "waiting: reserved for urgent".
  * The first slice only stores and shows the class: chips, plus grouping and sorting by class.

* Projects, Access and History workspaces; Laboratory and Agents are built screens with their detailed contracts elsewhere.
* Per-person grants and per-stream scope are implemented by the people/access and stream code; their remaining revocation requirements stay in the access contract.
* Results is a built screen; detailed requirements and remaining gaps stay in the Conversations/result contract.
* A phone navigation variant.
* Recovery controls in the app, following the existing operations and their unknown-outcome guards.
* Optional web push for needs-you items.

## Implementation

Built: the change feed (`go/coordinator/stream.go`, `changes_since` in `artifact_routes.go`), the list operations (`task_page` and `task_derived_check` in `tasks.go`, `sessions_list` and `list_membership` in `sessions.go`, `hosts_list` in `hosts.go`, `me` in `people.go`), the app routes (`artifact_routes.go`), the app (`app/build.mjs`, `app/src/`) and the checks (`tests/ui/store.test.ts`, `tests/ui/app.check.ts`).

### Verification

* `pnpm run build` builds the app and coordinator; the required CI jobs run the test suites. Record the actual run and head rather than treating this specification as a current pass result.
* `pnpm run check:design` reports the values against their targets: trace on the app routes, UX checks, first-load JS, click-to-detail and change-to-screen. Warnings do not fail.
* `tests/ui/app.check.ts` supplies fixtures behind the test proxy; qualify only the journeys and assertions actually exercised. Native PWA installation, offline behavior and the deployed SSE chain require their own retained observations.



## Interface workflows

Workflows for the separate [operator and architect interfaces](../../fleet.md#operator-interface). These define activities and information, not menus or screen layouts.

## Interaction model

Support both conversation-led work and autonomous assignments. Sessions show messages, tool activity, results and questions, including answer choices, while preserving supported harness capabilities. Tasks summarize outcomes, progress and decisions and link to relevant sessions and executions; neither view replaces the other.

People may discuss, inspect results and request rework within a session. Background work continues without an open conversation. Show questions in the work overview and relevant conversations as views of the same record. Reveal execution details when needed rather than requiring navigation through every object.

The objects below identify shared information and actions, not mandatory forms, screens or operator steps. Domain-specific content belongs to project tools and materials; Fleet does not require a universal schema for every kind of work.

## Operator workflow

| Workspace | Information and actions |
|---|---|
| Overview | Tasks by project, progress, results and situations needing attention; manage priorities and balance work across hosts and harnesses with visible load, quotas and placement reasons. |
| Projects | Purpose, materials, workflow rules, connections and selected capabilities; open related tasks and results. |
| Sessions | Ordinary conversations with a selected agent, including PM; related projects and tasks; continuation through another agent. |
| Tasks | Expected result, requirements, executions, steps, questions, files, checks and costs; metered costs and subscription list-price equivalents stay separate by `(currency, basis)`, and unrecorded metrics show `unavailable` rather than zero; control work and accept results or return them for rework under project rules. |
| Results | Find and inspect materials, code changes and checks; open the work that produced them. |

Surface questions and problems in the overview while keeping answers and actions attached to the affected work. Show interruption causes and available actions: answer, change account, hand off, retry or cancel within permissions.

A session need not belong to one project; it may discuss and create tasks for several projects. Not built: the exact project association and scoped history model for projectless architect or multi-project conversations (the existing coverage gap); this target does not grant cross-project access. A task presents its outcome and progress with access to nested pipeline steps, agent executions and tool calls when needed.

A representative path is: interrupted task -> cause -> another available agent -> continued work -> result in the same task. These workspaces do not prescribe the final menu.

### Operator objects

| Object | Detail and actions |
|---|---|
| Project | Purpose, description location, rules, available methods and tasks. Discuss, configure and open related work. |
| Session | Conversation, selected agent, related projects and tasks. Attach materials, assign work and hand off to another agent. |
| Task | Expected outcome, inputs, requirements, progress, questions and results. Clarify, prioritize, pause, continue, cancel, accept or request rework. |
| Result | Material, producing execution, checks and comments. Inspect, compare, annotate and reuse as input. |

## Architect workflow

| Workspace | Information and actions |
|---|---|
| Agents | Configurations, harnesses, models, instructions, skills and assigned tools; inspect changes and usage. |
| Capabilities and methods | Expected outcomes, alternative implementations, tools, pipelines and recommendations. |
| Environments and resources | Register hosts and environments; inspect availability, software, working areas, preparation, cleanup, capacity, consumption and allocation rules. |
| Connections and access | Services, accounts, limits and project access. |
| History and analysis | Project-wide time, costs, resources, waits and repeated work; drill down to executions and discuss findings with an agent. |
| Laboratory | Compare variants from new or recorded work, inspect numeric and qualitative results, control budgeted improvement runs and apply selected improvements. |

These workspaces organize the architect's work, not services or repositories. Their objects link across workspaces; exact menus and screen layouts remain unspecified.

The catalogue follows [shared and project scopes](../configuration.md#catalogue-scope). A project view includes its entries and available shared methods; the cross-project overview identifies each entry's scope. Ordinary sessions support [pipeline development](../configuration.md#pipeline-development).

The laboratory workflow is:

1. Configure an agent, method or pipeline.
2. Run experiments or benchmarks; retain each run's conditions and results.
3. Add comments, tags, comparisons, limitations and suitability conclusions.
4. Recommend a method for specific tasks within a project or across the system.

Follow the [experiment contract](fleet-lab.md) when applying improvements. Assessments alone do not change recommendations or product results.

### Architect objects

| Object | Detail and actions |
|---|---|
| Agent configuration | Purpose, harness, model, instructions, skills, assigned tools and environment requirements. Edit, inspect diffs, test and open executions. |
| Skill | Reusable content and the agents using it; browse independently or open from a configuration. |
| Capability | Input and result contracts, recommended methods and alternatives. Open a method to inspect its implementation. |
| Method | Implementation, settings, requirements, check results and real examples. Open its pipeline or agent configuration where applicable. |
| Tool or pipeline | Independently discoverable and editable. Pipeline detail exposes steps and transitions. |
| Environment | Supported work, available resources, active work and working areas. Open an area's users, occupied space, retention reason and available cleanup action. |
| Connection | Service, configured accounts, state, available limits and consuming projects and tools. Configure access; agent configurations reference tools without receiving copied credentials. |
| Execution | Task where applicable, caller, selected method, effective configuration, nested steps, history, results and costs. Open source objects, continue work or create an experiment. |
| Experiment | Source work, variants, configuration differences, criteria and run results. Repeat trials, compare, assess and apply an improvement; show its effect on future work and selected existing results. |

Cross-workspace navigation opens the same objects rather than independently maintained copies. A skill reached from an agent is the same skill shown in its catalogue. Result viewers remain available in execution context.

A pipeline-backed method may use one integrated detail view; distinct concepts do not require separate forms. Every interface action uses the same system operations available to agents under equivalent permissions.

A representative path is: expensive model generation -> execution -> costly step -> method and agent -> experiment with a different skill -> comparison -> application of the improvement.

## Character workflow prototype

The [CSGO storyboard](../prototypes/csgo-workflow.html) is a historical navigation study with synthetic data. It overemphasizes sequential screens and does not establish the interaction model above, native harness fidelity or project acceptance. Do not use it as an approved layout or required workflow.

Inspect the same result across operator and architect views. Compare a replacement with its predecessor, then distinguish applying it to existing work from recommending its method for future work. Application follows project checks; it does not imply immediate publication.

## Result viewing

Both interfaces reuse viewers suited to the [result type](../configuration.md#results-and-transfer): readable text and reports, zoomable images, interactive 3D models, and file changes with diffs.

Results may be stored materials or references to an external source; provide convenient inspection in either case. For a working product, provide access to its run or preview and checks where applicable; a code diff alone does not show product behavior.

## Contextual actions

From a problem, expose its cause, supporting data and available actions without losing the associated work context. Use the [shared operations](../system-operations.md), permissions and state.

- Exhausted quota: show the affected work and available continuation options.
- Large retained worktree: show its users, retention reason, occupied space and whether cleanup is available.
- Execution failure: show the failed operation, retained evidence and applicable recovery actions.

Supported actions and uncertain state remain visible; a displayed action does not establish that its operation has completed.
