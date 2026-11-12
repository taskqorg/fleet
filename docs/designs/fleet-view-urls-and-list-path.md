# Fleet view URLs and list data path

## Overview

This specification defines the operator app's URL codec and live list path for filter, grouping, search and selection changes. It has four parts:

1. **URL view contract.** One grammar for filters, grouping, search, presets, the focused row and the checked rows. A shared codec (`parseView` / `formatView`) used by the app and the coordinator, and a `view_link` operation for agents.
2. **List data path.** The normalized entity store implemented in `app/src/store.ts`, following [operator-app](fleet-operator-app.md) decision D4. Lists keep their rows on screen while new data loads, the list element is created once, and search sends one request per typing pause.
3. **`task_page` query.** A side table `task_derived`, kept by Postgres triggers, so a page read no longer computes four subqueries for every task in the table.
4. **Asserting check.** `tests/ui/filter.check.ts` gains an asserting mode that fails when any of the above regresses.

The [stack toolchain](fleet-stack.md#toolchain) is canonical for Node/pnpm, exact dependencies and shared stores; this topic does not retain the superseded vendoring/Bun proposal.

### Terms

* **Task:** a unit of production work (`task` table). **Execution:** one run of an agent for a task (`execution` table); the latest execution is the one with the highest `seq`. **Harness:** the agent runtime of an execution (`codex`, `claude`, `antigravity`); the list column "Agent" shows it. **Host:** the machine an execution runs on. **Question:** an open decision addressed to the operator (`question` table).
* **Conversation:** the operator app's name for a session (`session` document); the Conversations view reads `sessions_list`.
* **Operation:** a named coordinator function in one table, served as `POST /api/ops/<name>` to the browser and as MCP tools to agents. The browser calls it through `op()` in `app/src/api.ts`, typed by name from `contracts/ops.schema.json`; the coordinator dispatches it in `runOp`. Read operations (`READ_OPERATIONS`) run in a read transaction without a write lock.
* **`task_page`:** the operation that returns one page of the task list, plus counts per state and group summaries. **`list_membership`:** the operation that takes up to 1,000 changed ids and a list definition and returns which of them belong to that list now, with their rows and the list's counts. The client never decides membership itself (D4).
* **Change feed:** every write to a watched table adds a row to `change` through the trigger `fleet_change` (migration 8); the browser receives these over `/api/stream` and asks `list_membership` about the touched ids.
* **Scope:** the coordinator's write lock. Most writers hold GLOBAL; host ingest of one execution holds only that execution's scope and runs in parallel ([data foundation §12](fleet-data-foundation.md)). Row-level security limits every query to the projects in `fleet.project_ids`; request reads use the person scope under [people and access](fleet-people-access.md#2-visibility); background work uses its documented service authority.
* **Entity codes** such as `T-FLT-42` are decided in [design-system D17](../design-system/decisions.md) and allocated per [data foundation §2](fleet-data-foundation.md); they do not exist in the database yet.

## Rationale

A filter change must not clear counters, remove the list or discard the reader's anchor. The normalized store keeps the previous rows while the requested list loads, and the server remains the authority for membership. Derived task values avoid recomputing execution/question subqueries across the whole task history for every page; counts and search still depend on the matching set.

The earlier TypeScript/PGlite measurements explained this design choice; they do not establish current Go/PostgreSQL production performance. Current measurements name their fixture, head and environment, following §4.

## Goals

* A filter, grouping, search, preset or selection change keeps the filter bar, counters and current rows on screen. New data replaces them in place; the list element is never re-created after the first load.
* Back, Forward and returning to a view through the nav rail show the last known rows in the first frame, then refresh in place.
* Search sends one request per typing pause (no key for 300 ms); a newer request supersedes the older one.
* `task_page` over HTTP: median under 300 ms at 20,000 tasks for each filter and grouping case of the check, on the operator's machine against the coordinator binary (asserted by `pnpm run check:filter`). The 20,000 → 100,000 growth factor is reported, not asserted, because counts and search stay proportional to the matching set (see §3).
* Every view state is in the URL in one canonical form. A link opens the same list, the same focused row and the same checked rows for any operator with access.
* Agents build and read view links through one operation, with the same code the app uses.
* `pnpm run check:filter` fails on any regression of the asserted goals.

## Non-Goals

* Holding the full active task set in the browser for local filtering.
* Derived columns for `sessions_list` (same pattern; next slice). The brief's goal for `sessions_list` is narrowed to the client part (Conversations uses the new store).
* Server-stored short links (`?s=<hash>`): added only when a real URL length limit is hit.
* Bulk actions. This design defines `sel`, a checkbox per row and keeps checked rows in the URL; the actions themselves are separate work.
* Entity code allocation and resolution. The codec passes codes through unchanged; resolving them (`entity.resolve`) and the redirect direction are the D17 slice's work.
* Node toolchain changes (vendoring, shared binaries, Bun trial).
* Full-text search indexes. `q` stays a substring match.

## Open Questions

* **Eviction of cached lists.** The store keeps the ordered ids of visited lists in memory. Whether and when to drop them is decided from a measurement of real memory use, not a number chosen now; until then a list's ids are dropped when the page reloads.
* **Live Postgres numbers.** Targets are set on a local database. Production is re-measured after rollout with per-operation timing.

## Critical User Journeys (CUJs)

### CUJ 1: Change a filter without losing the screen

* **Given:** `/tasks` shows 100 rows grouped by priority, with counters.
* **When:** the operator clicks the "Failed" counter.
* **Then:** the URL becomes `/tasks?state=failed` (a new history entry); all counters stay visible with their counts; rows stay visible with an "updating" mark; within one server round trip the rows are replaced in place; the list element is the same DOM node; no skeleton appears.

### CUJ 2: Send a view to another operator

* **Given:** the operator has the "Needs me" preset active, grouped by agent, with task A focused and tasks B and C checked.
* **When:** they copy `/tasks/<A>?group=agent&preset=Needs%20me&sel=<B>,<C>&state=failed,waiting` and another operator opens it.
* **Then:** the second operator sees the same filters, grouping, focused row and checked rows, whatever presets they have saved. Rows they cannot see are absent; the link does not fail.

### CUJ 3: An agent links to a view

* **Given:** an agent wants to point the operator at failed tasks in project `fleet`.
* **When:** it calls `view_link` with `{ view: { view: "tasks", state: ["failed"], project: ["fleet"] } }`.
* **Then:** it receives `{ url: "https://fleet.ufobe.com/tasks?project=fleet&state=failed", path: "/tasks?project=fleet&state=failed" }`; calling `view_link` with `{ url }` returns the same view.

### CUJ 4: Type a search

* **Given:** `/tasks` is open.
* **When:** the operator types "hero" with less than 300 ms between keys.
* **Then:** one `task_page` request is sent after the pause; rows stay on screen while typing; the URL ends as `/tasks?q=hero`; one Back returns to `/tasks`.

### CUJ 5: Back and forward

* **Given:** the operator went from `/tasks` to `/tasks?state=failed` to `/tasks/<A>?state=failed`.
* **When:** they press Back twice.
* **Then:** each step shows the earlier list in the first frame from the store, then refreshes in place.

## Technical Plan (Architecture)

### 1. URL view contract

```text
/<view>[/<focus>]?<key>=<value>&...      keys in alphabetical order, defaults omitted
```

* **Views:** `tasks`, `conversations`, `hosts`, `overview`, `results`. Tasks, Conversations and Results have codec keys; other built workspace routes are selected by the app registry, not silently added to this codec.
* **Focus (path segment):** the row shown in the detail pane. A UUID today. A code is passed through unchanged; the D17 slice decides resolution.
* **Keys, tasks view:**

  | Key | Value | Default (omitted) | `task_page` input |
  | --- | --- | --- | --- |
  | `group` | `priority`, `state`, `project`, `agent`, `host`, `none` | `priority` | `group` |
  | `preset` | preset name, a label only | none | not sent |
  | `project` | comma list of project ids; `-` means "no project" | all | `filter.projectId` (array; `-` becomes `null`) |
  | `q` | search text | empty | `filter.search` |
  | `sel` | comma list of checked rows | empty | not sent |
  | `state` | comma list of task states | all | `filter.state` |

* **Keys, conversations view:** `archived` (`1` or omitted → `filter.archived`), `preset`, `project` (as in the tasks view → `filter.projectId`), `q` (→ `filter.search`), `sel`, `state` (→ `filter.state`).
* **Keys, results view:** `project`, `q` and artifact `state` (`stored`, `pending`, `failed`, `deleted`). Additional Results filters (`task`, `execution`, `media`, `since`) travel in `extra`, per the [Results contract](fleet-conversations.md). The codec does not grant artifact access.
* **Lists:** comma-separated. A comma, `%` or `&` inside a value is percent-encoded (`%2C`, `%25`, `%26`). List values are sorted and de-duplicated.
* **Canonical form:** keys in alphabetical order, list values sorted, defaults and empty lists omitted, no trailing `?`. Two equal views produce byte-identical URLs.
* **List key:** the canonical query string without `focus`, `sel` and `preset`, prefixed by the view (`tasks?group=agent&state=failed`). It identifies a list in the store and in IndexedDB. Checking rows, focusing and the preset label never create a new list.
* **Focus and `sel` are independent.** The focused row need not be in `sel`; focusing does not change `sel`; checking does not change focus.
* **Presets:** a link carries the preset expanded into explicit keys, plus `preset=<name>` as an inert label. The codec never validates it. The app's preset bar highlights a preset only when the receiver has a preset of that name whose keys equal the URL's keys (the matching that `tasks.tsx` does today); otherwise the label is ignored.
* **History:** a discrete change (counter, grouping, preset, focus, checking a row) pushes a new entry. Search pushes one entry when the operator starts typing and replaces it while they keep typing, so one Back leaves the search. `app/src/url.ts` implements this history behavior.
* **Unknown keys** are kept in `extra`, written back in alphabetical order with the known keys, not part of the list key. Truly unknown keys are ignored by views; Results explicitly consumes its `task`, `execution`, `media` and `since` filters from `extra`, even though the codec table does not define them. No version key is added until an incompatible change happens.
* **Meaning:** a link is a live query, not a snapshot. Access is the receiver's.
* **Length:** no fixed limit. If a real limit is hit (chat tools, Cloudflare Access, MCP transport), it is recorded and short links are designed then. URLs are never cut.
* **Privacy:** URLs carry search text and preset names into chats and logs; the app renders them as text only.

**Codec.** `packages/view-url/view-url.ts`, the import-free browser codec. The coordinator implements the equivalent codec in `go/coordinator/view_link.go`; `tests/go/view.test.ts` compares the two. They are separate implementations with a parity test, not one runtime module:

```ts
type ViewName = 'tasks' | 'conversations' | 'hosts' | 'overview' | 'results';
type View = { view: ViewName; focus?: string; group?: string; preset?: string; project?: (string | null)[];
  q?: string; sel?: string[]; state?: string[]; archived?: boolean; extra?: Record<string, string> };
parseView(input: string, opts?: { origin?: string | null }): View | null  // a path or full URL; null if not a view or a foreign origin
viewProblem(v: unknown): string | null     // checks a view from outside (view_link): unknown keys or bad values are errors
formatView(v: View): string                // canonical path + query, no origin; defaults omitted
listKey(v: View): string                   // view + canonical query without focus, sel, preset, extra
toPageParams(v: View): { filter: { state?: string[]; search?: string; projectId?: (string | null)[]; archived?: boolean }; group?: string }
```

The allowed keys, defaults and values per view are one constant table in the module.

**Agent operation `view_link`.** In the shared operation table and in the agent operation table (`agent.operations`), so it is available on `/api/ops`, `/api/tools` and the agent MCP endpoints. It is a read operation without database access.

* Input `{ view }` → `{ url, path }`. Every field is checked with `viewProblem`; a bad field is a 400, never dropped. `url` uses the configured `browserOrigin`; without one, `url` is `null` and `path` is returned.
* Input `{ url }` → `{ view }`. A path that is not a view, or a URL on another origin than `browserOrigin`, is rejected with 400 `Not a Fleet view URL` (with no origin configured, any origin is accepted). Both or neither input → 400.

### 2. List data path (entity store, D4)

```text
 URL ─parseView─▶ View ─listKey─▶ lists[key]   : Entry { view, params, state: signal<{ ids, keys, counts, total, groups, nextAfter, at, restored, error }>, seq, ... }
                                  records[id]  : { row, seq }       (one per task or session, shared by all lists)
                                  holders[id]  : list keys          (which lists hold the id: only those re-render on a change)
                                  docs[name]   : signal<data>       (task_get / session_get details, overview, hosts)
  task_page / sessions_list ─loadList─▶ records at the request's start cursor, lists[key] replaced in one write
  feed changes ─applyChanges─▶ list_membership (two lists per call, 1,000 ids per call) ─▶ records at the batch cursor; ids placed by keys[id]
  IndexedDB (D3, app/src/offline.ts) ◀── list:<key> and doc:<name>, written lazily at most once a second
```

* **`app/src/store.ts`** holds `records`, `lists` (an `Entry` per canonical list key: its view, page params, a state signal, a busy signal, the cursor it reflects, mount count) and `docs`, plus the `holders` index. A list holds ordered `ids`, a sort key per id (`keys[id]`, because the key depends on the grouping and cannot live in the shared record), counts, group summaries, `nextAfter`, `at` (time of the last server answer, 0 for stored data) and `error`. `app/src/url.ts` holds the URL hooks.
* **`useList(kind, view)`** returns `{ rows, counts, total, groups, at, error, pending, shownKey, shownView, more, reload, state }`. `shownKey` is the list on screen, derived during render: the wanted list (from the URL) as soon as it has data, else the last shown one; `pending` is true while they differ or a request runs. `shownView` is the view of the rows on screen; group headers use it while a new grouping is pending. The skeleton shows only when nothing was ever shown.
* **Never reset.** A page answer replaces `ids`, counts and groups in one signal write; nothing is cleared first.
* **Order.** Every record and every list remembers the change cursor its data reflects. A page answer is applied at the cursor its request started from, a membership answer at the cursor of its change batch; an older answer never overwrites a newer record or list. Changes that arrive while a list's page is loading are kept and asked about once the page has landed. A new request of a view aborts the previous one (`op(name, body, { signal })`; an abort, also while the body is read, is not "offline").
* **Paging.** `nextPage(key)` pages the shown list with that list's own params and does nothing while its first page loads.
* **Changes.** One feed subscription for the whole store (`startStore()` in `main.tsx`). Only mounted lists are asked; unmounted lists keep their data in memory, so Back renders in the first frame, and revalidate when mounted again. If membership fails, the list reloads its first page. `list_membership` returns `groups` for grouped task lists.
* **Offline and start.** The feed starts at the server head: every mounted list and doc loads fresh data on mount, and stored data only fills the first frame (also after a failed load, marked as not current). When the feed turns live, lists still showing stored rows or a failed load reload. Stored cursors are not used; keys of the old per-view layout are deleted once on start.
* **Clearing.** Sign-out and a grants change clear IndexedDB and the store's memory; mounted lists and docs load again (entries and doc signals stay, because mounted hooks hold them).
* **Memory.** Records no list holds are dropped after each page. Lists are not evicted: no limit is set until a memory measurement asks for one.
* **Docs.** `useDoc(name, fetch, touches, refreshMs?)` replaces `useLiveRecord` with the same parameters and keeps the previous value visible while re-reading.
* **One mounted view.** `main.tsx` routes `/tasks/:id?` and `/conversations/:id?` as one route each, so focus changes never unmount the view.
* **URL changes** are built from the URL at the moment of the change (`app/src/url.ts`), never from a captured value, so a debounced search cannot undo a click made meanwhile. A search session ends on any navigation the view did not make.
* **Search.** The input updates its own text at once; the URL and the request follow after 300 ms without a key press.
* **Visible states.** Pending: counters and rows keep their values, dimmed (`--fg-muted`), under a thin progress bar (`ListFrame` and `StateCounters` take `pending`). Error with rows: rows stay with the error banner. Answered and empty: the empty state.
* **Checkboxes.** Task and conversation rows get a native checkbox (`RowCheck`) in the last cell; the state badge stays the first cell (OR-22). Checked rows are the URL's `sel`.

### 3. `task_page` query: side table `task_derived`

Migration 10 in `go/coordinator/schema.sql` (migration 9 is the artifact store):

* **Table** `task_derived(task_id text PRIMARY KEY REFERENCES task(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED, project_id, agent, host, started_at, open_questions integer NOT NULL DEFAULT 0)`, with the `project_scope` policy on `project_id` (copied from the task).
  * `agent`: harness of the latest (highest `seq`) execution in `preparing` or `running`; null when none runs.
  * `host`: host of the latest execution in any state.
  * `started_at`: earliest `started_at` of the task's executions.
  * `open_questions`: number of questions of the task in state `open`.
* **Functions.**
  * `task_derived_values(p_task)` (SQL) is the one definition of the four values plus the task's `project_id`. It is revoked from PUBLIC.
  * `task_derived_recompute(p_task)` locks the task's `task_derived` row (`SELECT ... FOR UPDATE`, inserting it first when missing), reads `task_derived_values` and updates the row only when a value differs. It does nothing when the task row does not exist (deleted, or not yet inserted: the task's own insert trigger computes it). Revoked from PUBLIC.
  * `fleet_task_derived(p_task)` (`SECURITY DEFINER`) is the entry point the triggers call. It saves `fleet.project_ids`, sets it to `*` with `set_config(..., true)`, runs the recompute and sets the saved value back. A function-level `SET fleet.project_ids` is superuser-only on PostgreSQL 15+ (it failed on PDS18 as the owner role `fleet`). There is no exception block: a failed call's transaction or savepoint rollback restores the setting, and an exception block would open one subtransaction per write.
  * `task_derived_drift()` (`SECURITY DEFINER`, same save and restore) recomputes every task with all projects visible and returns only the tasks the caller may see.
  * The migration drops every earlier variant of these functions before creating them.
* **Concurrency.** Only GLOBAL writers change the trigger columns: execution-scope ingest writes `observation`, `last_seen_at`, `harness_version` and totals ([data foundation §12](fleet-data-foundation.md), where this rule is recorded). GLOBAL writers run one at a time, so recomputes cannot race. The row lock is a second guard in case the rule is broken later; a `task_id` move recomputes the two tasks in ascending id order. *An advisory lock per task was the first choice; it exhausts the shared lock table when one transaction writes 20,000 tasks.*
* **Triggers:** `AFTER INSERT OR DELETE OR UPDATE OF task_id, state, host_id, harness, started_at, seq ON execution` (`seq` because agent and host are "latest by seq"), `AFTER INSERT OR DELETE OR UPDATE OF task_id, state ON question`, and `AFTER INSERT OR UPDATE OF project_id ON task`. The column lists keep heartbeats and output writes from firing the recompute.
* **Change feed:** a `task_derived_change` trigger calls `fleet_change`, replaced (`CREATE OR REPLACE`) with one more branch: table `task_derived` gives kind `task`, id and task id `task_id`, and the row's `project_id`. `task.updated_at` does not move on derived changes.
* **Backfill:** the migration sets `fleet.project_ids` to `*` itself and inserts one row per existing task from `task_derived_values`, before the change trigger exists, so it writes no change row per task.
* **Indexes:** `execution_task_seq ON execution(task_id, seq DESC)`, which replaces `execution_task`, and `task_priority_order ON task((CASE priority ...), seq DESC, id)` for the default page order. No indexes on `task_derived(agent|host)`: the group order uses `coalesce(...)`, which plain indexes cannot serve.
* **Query** (`taskPage`, `go/coordinator/tasks.go`). The CTE with correlated subqueries is removed. Filters: `state`, `projectId` (one id, null, or a list of both), `search`.
  * The page joins `task_derived` (LEFT JOIN, so a task missing its row still lists with empty values) only where it is needed: for agent and host grouping the order needs it; otherwise the page is chosen on `task` alone and only its rows are joined.
  * `taskSummary(q, filter, group, groupCounts)` returns per-state counts (the `WHERE` without the state filter, so every counter keeps its number) and group summaries (joined only for agent and host). `page` and `list_membership` both use it.
  * The group expression comes from the fixed Go `taskGroups` map (`taskGroup` and `groupOf`); an unknown group is rejected, and only the selected map values are interpolated.
* **Cost after the change.** The page read is an index-ordered read of `n` rows. Counts and group summaries remain a scan of the matching rows on plain columns. This is why the growth factor is reported, not asserted.
* **Drift check:** `task_derived_check` reads `task_derived_drift()` and returns `{ checked, drifted: [{ taskId, stored, expected }] }` for the caller's projects; `checked` counts the same tasks. Used by tests and exposed as the read operation `task_derived_check`.

### 4. Asserting filter check

`tests/ui/filter.check.ts [--assert] [--sizes 3000,20000,100000]`. Each size gets a fresh Go coordinator and PostgreSQL fixture through `tests/ui/seed.ts` and `tests/harness.ts`. Without `--assert` it only prints. With `--assert` it exits 1 when a rule fails at 3,000 or 20,000 tasks; the 100,000-task run is always report-only. On CI (env `CI` set) the two 300 ms budgets are written to the job summary and do not fail the job, because a shared runner's timing is noise; locally they are asserted. Rules:

* the `[data-ui=data-list]` element is the same node before and after a filter, grouping, "All", preset, focus and check change;
* no `.skeleton` appears after the first load;
* every state counter that was visible stays visible during a change (checked each animation frame);
* typing "hero" with 60 ms between keys sends exactly one `task_page`;
* each filter and grouping case settles within 300 ms at 20,000 tasks;
* after Back, rows are present in the first animation frame after `popstate`;
* focusing a row does not change `sel`, and checking a row does not change focus;
* a URL copied from one page and opened in a second page reproduces focus, `sel`, filters and grouping;
* `task_page` over HTTP: median under 300 ms at 20,000 tasks per case.

It prints fixture load time and, when the larger sizes are requested, the growth factor. Not built: a separate migration10 backfill timing in this check.

## Alternatives Considered

### Alternative A: Selection only in the path, or only in `sel=`

* **Pros:** one concept.
* **Cons:** the path alone cannot hold checked rows for bulk actions; `sel=` alone breaks every `/tasks/:id` link and the detail route.
* **Reason for Rejection:** Rejected by author: focus and checked rows are both needed and kept independent.

### Alternative B: Rewrite the query only, no stored values

* **Pros:** no schema change; computing values for page rows only is enough for display fields.
* **Cons:** grouping or ordering by agent or host needs the value for every matching task, so the unfiltered grouped list keeps a subquery per task and grows with execution history.
* **Reason for Rejection:** Rejected by author. The review (zoom-out lens) contests this and asks for a measurement first; the owner kept stored values, accepting their upkeep (triggers, row-level security on the side table, a drift check).

### Alternative C: Stored columns on `task`, written by triggers

* **Pros:** no join.
* **Cons:** every derived change would run `task_touch` (moving `task.updated_at`, which the list shows and the rollback export selects on) and add task change rows with no task edit; derived writes would contend with operator edits on the same task row; `task` row-level security and derived values would be coupled in one table.
* **Reason for Rejection:** Rejected by author after review: the side table keeps derived writes off the task row.

### Alternative D: Stored values written by coordinator code

* **Cons:** every write path, including legacy ones, must remember the update; a missed path drifts silently.
* **Reason for Rejection:** Rejected by author: triggers cover every path, as the change log does.

### Alternative E: Deferred recompute by the coordinator after commit

* **Pros:** no lock coupling.
* **Cons:** one more loop that must not stall; lists lag behind executions.
* **Reason for Rejection:** Rejected by author: triggers keep values current at commit with no extra loop.

### Alternative F: Per-component list state, or a page cache keyed by URL

* **Cons:** per-component state still starts from IndexedDB or a skeleton on Back; a URL-keyed page cache updates a changed task in each cached page only after membership runs for that page.
* **Reason for Rejection:** Rejected by author: D4's normalized store updates a record once for every list.

### Alternative G: Another front-end stack

* **Pros:** see the brief (fine-grained updates, no npm for WASM).
* **Cons:** the causes are the data path and the query, which carry over; the rewrite costs `packages/ui`, the design book and `check:design`.
* **Reason for Rejection:** Rejected by author.

--------------------------------------------------------------------------------

## Non-Functional Requirements

* **Performance:** targets in Goals; the check measures them.
* **Write cost:** an execution or question write that changes a watched column adds one function call, one row lock and at most one write of `task_derived`. The filter check records fixture load time; it does not currently measure a before/after migration10 comparison.
* **Security:** `fleet_task_derived` sets `fleet.project_ids` to `*` for its own call, so it reads all of one task's rows whatever the writer's scope. It takes only a task id, writes only `task_derived`, and copies the task's `project_id`, so reads of `task_derived` and its change rows stay scoped. `view_link` builds URLs only on the configured origin and never includes credentials. A shared link grants nothing.
* **Rollout and rollback:** migration 10 is additive. The reading code ships in the same release. If values drift, `task_derived_check` finds them; re-running the recompute for the drifted ids repairs them: `SELECT fleet_task_derived(id) FROM unnest(ARRAY[...]) AS id` as the owner role. The old query stays as the reference in the tasks test.

--------------------------------------------------------------------------------

### Additional Resources

* [fleet-operator-app.md](fleet-operator-app.md) (D3 offline copy, D4 entity store, routing)
* [fleet-data-foundation.md](fleet-data-foundation.md) (§2 task, §12 concurrency model, G2 derived values)
* [design-system decisions](../design-system/decisions.md) (D17 entity codes, OR-22 via owner rules)

### Future Improvements

These targets remain in the backlog; polishing them is not the critical path for project/PM/first-task and Lab control breadth. Existing acceptance targets above are unchanged.

* Not built: a Conversations list filter-path browser assertion equivalent to `filter.check.ts` (the current check exercises Tasks; transcript paging checks are a separate concern).
* Not qualified: the deployed SSE list-update path after a real write. Source and fixture checks alone do not establish that live-chain observation; a read-only URL smoke does not either.

* **Toolchain:** follow [fleet-stack.md](fleet-stack.md#toolchain). Its selected Node22/pnpm10 and native TypeScript paths supersede the old vendoring/Bun proposal; those rejected options are not remaining work for this topic.
* Not built: optimizing the per-session subqueries in `go/coordinator/sessions.go` with the proposed side-table pattern. This remains performance work, not a new requirement to launch a first supervised task.
* **Overview** already uses one grouped `task_page` call with `group: "project"`, `groupCounts: true` and `limit: 1` (`app/src/views/overview.tsx`); the old per-project-call follow-up is complete.
* **Per-operation timing:** coordinator request spans are implemented in `go/coordinator/telemetry.go`; their live availability is a separate [telemetry](fleet-telemetry.md) observation. Not built: the separately proposed slow-query log. A span mechanism alone does not establish production query latency.
* **Counts at very large sizes:** cached counters if the reported growth factor shows counts dominate.

--------------------------------------------------------------------------------

## Implementation

Built: the codec `packages/view-url/view-url.ts` (`tests/ui/view-url.test.ts`); `task_derived` and its triggers in `go/coordinator/schema.sql`; the query in `taskPage` (`go/coordinator/tasks.go`); the entity store `app/src/store.ts` with `app/src/offline.ts` (`tests/ui/store.test.ts`, `tests/ui/lists.test.ts`); the filter check `tests/ui/filter.check.ts`.

Built: `view_link` is registered by `agentTypedOp` in `go/coordinator/view_link.go`, with generated request/reply types; `tests/go/view.test.ts` checks parity and validation.

### Verification

* Build and tests: `pnpm run build`; `pnpm test`.
* Filter check: `pnpm run check:filter` (Chromium) uses the default 3,000/20,000 sizes. A 100,000-task run is report-only and happens only when explicitly included with `--sizes`; CI timing is report-only as described in §4. Browser checks run in CI under the current local no-launch guard, not through a local wrapper.
* Design: `pnpm run check:design`.

--------------------------------------------------------------------------------
